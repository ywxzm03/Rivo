package sendmessagetool

import (
	"context"
	"errors"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/subagent"
)

// fakeMessenger 记录投递参数,用于校验工具契约
type fakeMessenger struct {
	taskID  string
	message string
	result  subagent.SendMessageResult
	err     error
}

func (f *fakeMessenger) SendMessage(_ context.Context, taskID, message string) (subagent.SendMessageResult, error) {
	f.taskID, f.message = taskID, message
	if f.err != nil {
		return subagent.SendMessageResult{}, f.err
	}
	return f.result, nil
}

func TestToolSatisfiesLLMToolContract(t *testing.T) {
	var _ llm.Tool = New(&fakeMessenger{})
}

func TestToolMetadata(t *testing.T) {
	tool := New(&fakeMessenger{})
	if got := tool.Name(); got != "SendMessage" {
		t.Fatalf("Name() = %q, want SendMessage", got)
	}
	if tool.Description() == "" || tool.PromptSnippet() == "" {
		t.Fatal("description or snippet is empty")
	}
	if got := tool.ExecutionMode(); got != llm.ToolExecutionParallel {
		t.Fatalf("ExecutionMode() = %q, want parallel", got)
	}
	schema := tool.Parameters()
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 || required[0] != "to" || required[1] != "message" {
		t.Fatalf("required = %#v, want to and message", schema["required"])
	}
}

func TestExecuteDeliversMessage(t *testing.T) {
	messenger := &fakeMessenger{result: subagent.SendMessageResult{TaskID: "agent-7", Status: "queued", Message: "queued for agent-7"}}
	tool := New(messenger)
	result, err := tool.Execute(context.Background(), map[string]any{"to": "agent-7", "message": "keep going"})
	if err != nil {
		t.Fatal(err)
	}
	// 1.参数要原样透传给执行面
	if messenger.taskID != "agent-7" || messenger.message != "keep going" {
		t.Fatalf("delivered %q -> %q", messenger.taskID, messenger.message)
	}
	// 2.正文使用执行面返回的消息,details 携带完整结果
	if text := result.Content[0].(llm.TextContent).Text; text != "queued for agent-7" {
		t.Fatalf("result text = %q", text)
	}
	if details, ok := result.Details.(subagent.SendMessageResult); !ok || details.Status != "queued" {
		t.Fatalf("result details = %#v", result.Details)
	}
}

func TestExecuteRejectsMissingFields(t *testing.T) {
	messenger := &fakeMessenger{}
	tool := New(messenger)
	// 缺字段时仍应调用执行面,由 Manager 统一给出错误
	if _, err := tool.Execute(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if messenger.taskID != "" || messenger.message != "" {
		t.Fatalf("unexpected delivery %q -> %q", messenger.taskID, messenger.message)
	}
}

func TestExecutePropagatesError(t *testing.T) {
	wantErr := errors.New("no such task")
	tool := New(&fakeMessenger{err: wantErr})
	if _, err := tool.Execute(context.Background(), map[string]any{"to": "agent-1", "message": "hi"}); err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
