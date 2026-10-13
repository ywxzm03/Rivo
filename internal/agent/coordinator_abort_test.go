package agent

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// fakeCoordinator 模拟编排层在取消时返回错误
type fakeCoordinator struct {
	cancelErr error
}

func (f *fakeCoordinator) CompactIfNeeded(ctx context.Context) (bool, error) {
	return false, f.cancelErr
}

func (f *fakeCoordinator) NotifyOverflow() {}

// Coordinator 取消短路也应走 aborted 事件收场（与其他取消路径一致的 UI 语义）
func TestRunLoopCompactionCancelSettlesWithAborted(t *testing.T) {
	events := &[]llm.AgentEvent{}
	coordinator := &fakeCoordinator{cancelErr: context.Canceled}
	a := NewAgent(llm.AgentConfig{Model: llm.Model{ID: "m1"}}, llm.Context{}, silentCancelStreamFn(), func(event llm.AgentEvent) error {
		*events = append(*events, event)
		return nil
	})
	a.Coordinator = coordinator
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// runLoop 顶部第一件事就是 CompactIfNeeded；ctx 已取消即触发该路径
		cancel()
	}()
	message, err := a.Run(ctx, "hello")
	if err != nil {
		t.Fatalf("compaction cancel should settle via events: %v", err)
	}
	if message == nil || message.StopReason != llm.StopReasonAborted {
		t.Fatalf("stopReason = %+v, want aborted", message)
	}
	assertAbortedEventSequence(t, *events)
}
