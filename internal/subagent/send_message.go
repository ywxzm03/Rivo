package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// ResumeSource 是磁盘侧描述,指向已不在易失 store 中的任务。
// 凭它足以从 sidechain transcript 重建子 runtime。
type ResumeSource struct {
	ID           string
	AgentType    string
	Description  string
	OutputFile   string
	WorktreePath string
}

// ResumeResolver 查找已被 store 淘汰的任务。返回 false 表示 transcript 已不存在,
// 任务无法恢复。
type ResumeResolver func(taskID string) (ResumeSource, bool)

type SendMessageResult struct {
	TaskID  string `json:"taskId"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// SendMessage 把输入投递给任务。运行中的任务把消息排入其下一轮工具调用;
// 终态任务按 continuation 恢复;store 中已缺失的任务则从 sidechain transcript 重建。
func (m *Manager) SendMessage(ctx context.Context, taskID, message string) (SendMessageResult, error) {
	taskID, message = strings.TrimSpace(taskID), strings.TrimSpace(message)
	if taskID == "" || message == "" {
		return SendMessageResult{}, errors.New("subagent: task ID and message are required")
	}
	task, ok := m.store.Task(taskID)
	if !ok {
		return m.resumeEvicted(ctx, taskID, message)
	}
	if task.Base().Status == StateRunning {
		user := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: message}}}
		if err := m.store.EnqueuePendingMessage(taskID, user); err != nil {
			return SendMessageResult{}, err
		}
		return SendMessageResult{TaskID: taskID, Status: "queued", Message: fmt.Sprintf("Message queued for %s; it will be delivered at the task's next tool round.", taskID)}, nil
	}
	if _, ok := task.(*AgentTaskState); !ok {
		return SendMessageResult{}, &StopTaskError{Code: "unsupported_type", Message: fmt.Sprintf("Task %s is not an agent task and cannot be resumed", taskID)}
	}
	if _, err := m.Continue(ctx, taskID, message); err != nil {
		return SendMessageResult{}, err
	}
	return SendMessageResult{TaskID: taskID, Status: "resumed", Message: fmt.Sprintf("Task %s was %s; resumed it in the background with your message. You will be notified when it finishes.", taskID, task.Base().Status)}, nil
}

// resumeEvicted 从 sidechain transcript 重建 store 已不再跟踪的任务,
// 然后在其上执行 continuation prompt。
func (m *Manager) resumeEvicted(ctx context.Context, taskID, prompt string) (SendMessageResult, error) {
	if m.resumeResolver == nil || m.factory == nil {
		return SendMessageResult{}, &StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s", taskID)}
	}
	source, ok := m.resumeResolver(taskID)
	if !ok {
		return SendMessageResult{}, &StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s and no transcript to resume it from", taskID)}
	}
	m.continuationMu.Lock()
	defer m.continuationMu.Unlock()

	agentType := strings.TrimSpace(source.AgentType)
	if agentType == "" {
		agentType = "work"
	}
	// 1.重新注册稳定 task ID,使后续进度、通知与 TUI 投影回到同一任务
	stateID, _, err := m.store.Register(RegisterSpec{
		ID: taskID, Type: TaskTypeAgent, Description: source.Description,
		AgentType: agentType, OutputFile: source.OutputFile, WorktreePath: source.WorktreePath,
	})
	if err != nil {
		return SendMessageResult{}, err
	}
	// 2.按 continuation 语义装配 runtime:factory 会打开已有 transcript
	runtime, err := m.factory(ctx, AgentTaskSpec{
		ID: stateID, Description: source.Description, Prompt: prompt, AgentType: agentType,
		OutputFile: source.OutputFile, WorktreePath: source.WorktreePath, Continuation: true,
	})
	if err != nil {
		_ = m.store.failReserved(stateID, err)
		return SendMessageResult{}, err
	}
	if runtime == nil || runtime.Agent == nil {
		_ = m.store.failReserved(stateID, errors.New("subagent: factory returned no agent"))
		return SendMessageResult{}, errors.New("subagent: factory returned no agent")
	}
	if runtime.Done == nil {
		runtime.Done = make(chan struct{})
	}
	lifecycleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	runtime.Cancel = cancel
	token, err := m.store.attachAgentRuntime(stateID, runtime, prompt)
	if err != nil {
		cancel()
		return SendMessageResult{}, err
	}
	m.store.setRuntimeMetadata(stateID, token, runtime)
	m.installPendingDrain(stateID, token, runtime)
	go m.runAgent(lifecycleCtx, stateID, token, runtime, AgentToolInput{
		Description: source.Description, Prompt: prompt, SubagentType: &agentType,
	})
	return SendMessageResult{TaskID: stateID, Status: "resumed", Message: fmt.Sprintf("Task %s was no longer active; resumed it from its transcript with your message. You will be notified when it finishes.", stateID)}, nil
}

type SendMessageTool struct{ manager *Manager }

func (m *Manager) SendMessageTool() *SendMessageTool { return &SendMessageTool{manager: m} }

func (t *SendMessageTool) Name() string { return "SendMessage" }

func (t *SendMessageTool) Description() string {
	return "Send a message to a subagent task. A running task queues the message; a stopped task is resumed with it."
}

func (t *SendMessageTool) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"to", "message"},
		"properties": map[string]any{
			"to":      map[string]any{"type": "string", "description": "Target subagent task ID"},
			"message": map[string]any{"type": "string", "description": "Message to deliver to the subagent"},
		},
	}
}

func (t *SendMessageTool) PromptSnippet() string                { return "Send a message to a subagent task" }
func (t *SendMessageTool) PromptGuidelines() []string           { return nil }
func (t *SendMessageTool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (t *SendMessageTool) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	to, _ := args["to"].(string)
	message, _ := args["message"].(string)
	result, err := t.manager.SendMessage(ctx, to, message)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: result.Message}}, Details: result}, nil
}
