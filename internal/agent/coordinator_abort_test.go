package agent

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// Coordinator 取消短路也应走 aborted 事件收场（与其他取消路径一致的 UI 语义）
func TestRunLoopCompactionCancelSettlesWithAborted(t *testing.T) {
	events := &[]llm.AgentEvent{}
	a := NewAgent(llm.AgentConfig{Model: llm.Model{ID: "m1"}}, llm.Context{}, silentCancelStreamFn(), func(event llm.AgentEvent) error {
		*events = append(*events, event)
		return nil
	})
	a.PrepareRequest = func(ctx context.Context, r PreparedRequest) (PreparedRequest, error) { return r, context.Canceled }
	ctx := context.Background()
	message, err := a.Run(ctx, "hello")
	if err != nil {
		t.Fatalf("compaction cancel should settle via events: %v", err)
	}
	if message == nil || message.StopReason != llm.StopReasonAborted {
		t.Fatalf("stopReason = %+v, want aborted", message)
	}
	assertAbortedEventSequence(t, *events)
}
