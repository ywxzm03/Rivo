package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// forkMaxTurns 限制 fork 子 agent 的轮次,避免继承长 transcript 后无限跑
const forkMaxTurns = 200

// Runner 是子 agent 的执行内核:装配 prompt、工具、transcript 与 runtime
type Runner struct {
	ParentCwd   string
	SessionDir  string
	AgentDir    string
	Thinking    llm.ThinkingLevel
	ParentTools []llm.Tool
	TaskStore   *taskpkg.Store

	// mu 保护模型与 stream:主 agent 切换模型时会写,子任务启动时会读
	mu          sync.RWMutex
	parentModel llm.Model
	streamFn    llm.StreamFn
}

// SetModel 更新子 agent 的默认模型与 stream,供主 agent 切换模型时调用
func (r *Runner) SetModel(model llm.Model, streamFn llm.StreamFn) {
	r.mu.Lock()
	r.parentModel = model
	r.streamFn = streamFn
	r.mu.Unlock()
}

// modelAndStream 返回当前默认模型与 stream 的一致快照
func (r *Runner) modelAndStream() (llm.Model, llm.StreamFn) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.parentModel, r.streamFn
}

// Run 按 spec 装配并返回一个已就绪的子 agent runtime
func (r *Runner) Run(_ context.Context, spec taskpkg.AgentSpec) (*taskpkg.AgentRuntime, error) {
	cwd := r.ParentCwd
	worktreePath := spec.WorktreePath
	worktreeBase := ""
	createdWorktree := false
	runnerSucceeded := false
	// 1.失败路径要回收刚创建的空 worktree,避免留下垃圾目录
	defer func() {
		if createdWorktree && !runnerSucceeded {
			_ = cleanupSubagentWorktree(worktreePath, r.ParentCwd, worktreeBase, metadataPathFor(worktreePath, r.SessionDir, spec.ID))
		}
	}()
	if spec.Worktree && worktreePath == "" {
		var err error
		worktreePath, worktreeBase, err = createSubagentWorktree(r.ParentCwd, r.SessionDir, spec.ID)
		if err != nil {
			return nil, err
		}
		createdWorktree = true
	}
	if worktreePath != "" {
		cwd = worktreePath
	}
	if r.SessionDir == "" {
		return nil, fmt.Errorf("subagent: session directory is unavailable")
	}
	// 2.transcript 与元数据落在 sessionDir/subagents 下
	transcriptPath := filepath.Join(r.SessionDir, "subagents", "agent-"+spec.ID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		return nil, err
	}
	metadataPath := strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"
	if !spec.Continuation || createdWorktree {
		meta := metadata{AgentType: spec.AgentType, Description: spec.Description, WorktreePath: worktreePath, WorktreeOwned: createdWorktree}
		if err := writeMetadata(metadataPath, meta); err != nil {
			return nil, err
		}
	}
	sm, err := r.openSession(spec, transcriptPath, cwd)
	if err != nil {
		return nil, err
	}
	// 3.解析 model / thinking:内置与定义默认值可被显式参数覆盖
	fork := strings.EqualFold(spec.AgentType, "fork")
	model, streamFn := r.modelAndStream()
	if !fork && spec.Model.ID != "" {
		model = spec.Model
	}
	definition, err := r.loadDefinition(spec, cwd, fork)
	if err != nil {
		return nil, err
	}
	if !fork && definition != nil && spec.Model.ID == "" && definition.Model != "" {
		if parts := strings.SplitN(definition.Model, "/", 2); len(parts) == 2 {
			model.Provider, model.ID = parts[0], parts[1]
		} else {
			model.ID = definition.Model
		}
	}
	// 4.写类工具要绑定子 transcript 的 file history,必须先挂载再装配:
	// 晚挂载会让工具拿到 typed-nil store,nil 接口判空失效后在写文件时崩溃
	if sm.FileHistory() == nil {
		sm.AttachFileHistory(r.AgentDir)
	}
	tools := childTools(r.ParentTools, fork, sm, r.TaskStore)
	if definition != nil {
		tools, err = filterAgentTools(tools, definition.Tools)
		if err != nil {
			return nil, err
		}
	}
	if !fork && (strings.EqualFold(spec.AgentType, "explore") || strings.EqualFold(spec.AgentType, "plan")) {
		tools = readOnlyTools(tools)
	}
	messages, pendingToPersist, err := r.buildMessages(spec, sm, cwd, tools, definition, fork, worktreePath)
	if err != nil {
		return nil, err
	}
	if len(pendingToPersist) > 0 {
		if _, err := sm.AppendMessages(pendingToPersist); err != nil {
			return nil, err
		}
		messages = append(messages, pendingToPersist...)
	}
	if len(messages) > 0 && !spec.Continuation {
		if _, err := sm.AppendMessages(messages); err != nil {
			return nil, err
		}
	}
	thinking := r.Thinking
	if definition != nil && spec.Thinking == "" && definition.Thinking != "" {
		thinking = definition.Thinking
	}
	if !fork && spec.Thinking != "" {
		thinking = spec.Thinking
	}
	cfg := llm.AgentConfig{Model: model, ThinkingLevel: thinking, SessionID: sm.ID()}
	if fork && spec.ParentConfig != nil {
		cfg = *spec.ParentConfig
		cfg.SessionID = sm.ID()
	}
	// 5.子事件由 Manager 的 task 级 handler 消费。把它们送到父 sink 会把
	// sidechain transcript 更新混进主 TUI 流。
	child := agent.NewAgent(cfg, llm.Context{Tools: tools, Messages: messages}, streamFn, nil)
	child.WorkingDir = cwd
	child.ForkChild = fork
	child.AgentID = spec.ID
	if fork {
		child.MaxTurns = forkMaxTurns
	}
	if definition != nil {
		child.MaxTurns = definition.MaxTurns
	}
	child.CommitMessages = sm.AppendMessages
	runtime := &taskpkg.AgentRuntime{
		Agent: child, OutputFile: transcriptPath, WorktreePath: worktreePath, Done: make(chan struct{}),
		MessageStart: len(messages),
	}
	// 只有本生命周期新建的 worktree 才装回收钩子:恢复沿用的 worktree 里可能已有
	// 用户工作,与 Claude Code 一致,一律保留到用户手动清理
	if createdWorktree && worktreePath != "" {
		runtime.WorktreeCleanup = func() bool { return cleanupSubagentWorktree(worktreePath, r.ParentCwd, worktreeBase, metadataPath) }
	}
	// 6.fork 首轮续跑父上下文;continuation 追加一条 user 消息后继续
	if fork && !spec.Continuation && spec.ParentContext != nil {
		runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
			return child.Continue(ctx)
		}
	} else if spec.Continuation {
		runtime.Run = func(ctx context.Context, promptText string) (*llm.AssistantMessage, error) {
			return child.RunMessage(ctx, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: promptText}}})
		}
	}
	runnerSucceeded = true
	return runtime, nil
}

// writeMetadata 原子写入任务元数据,供 eviction 后恢复。
// 先写临时文件再 rename,避免中断留下半截 JSON 让任务变成不可恢复
func writeMetadata(path string, meta metadata) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-meta-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows 的 os.Rename 不允许覆盖已存在文件;continuation 会重写同一份元数据
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		return os.Rename(tmpPath, path)
	}
	return nil
}

// openSession 打开或创建子 agent 的 transcript
func (r *Runner) openSession(spec taskpkg.AgentSpec, transcriptPath, cwd string) (*session.SessionManager, error) {
	if !spec.Continuation {
		return session.NewAt(cwd, transcriptPath), nil
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		return nil, fmt.Errorf("subagent: transcript %q is unavailable: %w", transcriptPath, err)
	}
	return session.Open(transcriptPath, cwd)
}

// loadDefinition 读取非内置、非 fork 类型对应的 agent 定义。
// 项目定义始终从父项目根解析:worktree 基于 origin/HEAD,未跟踪的
// .rivo/agent/agents 文件不会出现在里面
func (r *Runner) loadDefinition(spec taskpkg.AgentSpec, _ string, fork bool) (*Definition, error) {
	if fork || isBuiltinAgent(spec.AgentType) {
		return nil, nil
	}
	return LoadDefinition(r.ParentCwd, r.AgentDir, spec.AgentType)
}

// buildMessages 决定子 agent 的初始消息,并返回需要随首轮一起落盘的 pending 消息
func (r *Runner) buildMessages(spec taskpkg.AgentSpec, sm *session.SessionManager, cwd string, tools []llm.Tool, definition *Definition, fork bool, worktreePath string) ([]llm.Message, []llm.Message, error) {
	var messages []llm.Message
	forkInitial := fork && !spec.Continuation && spec.ParentContext != nil
	switch {
	case spec.Continuation:
		// 1.continuation 从磁盘 transcript 重建上下文
		view := sm.BuildContext()
		messages = cloneMessages(view.Messages())
		var pending []llm.Message
		if len(spec.PendingMessages) > 0 {
			pending = make([]llm.Message, len(spec.PendingMessages))
			for i, message := range spec.PendingMessages {
				pending[i] = message
			}
		}
		return messages, pending, nil
	case forkInitial:
		// 2.fork 继承父 transcript,补齐缺失的 tool result 后追加指令
		return buildForkMessages(spec.ParentContext.Messages, spec.Prompt, worktreePath, r.ParentCwd), nil, nil
	default:
		// 3.新建子 agent:按 agent 类型决定是否加载项目上下文与 skills
		sections := r.systemSections(cwd, tools, spec.AgentType, definition)
		messages = append(messages, llm.SystemMessage{Sections: sections})
		return messages, nil, nil
	}
}

// systemSections 构建子 agent 的 system sections,并把 agent prompt 注入 instructions
func (r *Runner) systemSections(cwd string, tools []llm.Tool, agentType string, definition *Definition) map[string]string {
	omitInstructions := strings.EqualFold(agentType, "explore") || strings.EqualFold(agentType, "plan")
	if definition != nil && definition.OmitAgentsMd {
		omitInstructions = true
	}
	var contextFiles []prompt.ContextFile
	var skills []prompt.Skill
	if !omitInstructions {
		loader := prompt.NewLoader(cwd, r.AgentDir)
		loader.Reload()
		contextFiles, skills = loader.ContextFiles(), loader.Skills()
	}
	sections := prompt.BuildSections(prompt.Options{Cwd: cwd, Tools: tools, ContextFiles: contextFiles, Skills: skills})
	agentPrompt := builtinPrompt(agentType)
	if definition != nil {
		agentPrompt = definition.Prompt
	}
	if existing := sections["instructions"]; existing != "" {
		sections["instructions"] = existing + "\n\n<agent-instructions>\n" + agentPrompt + "\n</agent-instructions>"
	} else {
		sections["instructions"] = "<instructions>\n" + agentPrompt + "\n</instructions>"
	}
	return sections
}
