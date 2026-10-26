package agent

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 两条队列共用一套 API：同一个操作换种类就能作用到另一条队列
func TestQueueAPIAppliesToBothKinds(t *testing.T) {
	for _, kind := range []QueueKind{QueueSteering, QueueFollowUp} {
		t.Run(kindName(kind), func(t *testing.T) {
			a := New(Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil})
			if a.Len(kind) != 0 {
				t.Fatalf("Len() = %d, want 0 on a fresh agent", a.Len(kind))
			}
			if err := a.Enqueue(kind, PendingPayload{Text: "hello"}); err != nil {
				t.Fatalf("Enqueue() error = %v", err)
			}
			if a.Len(kind) != 1 {
				t.Fatalf("Len() = %d, want 1", a.Len(kind))
			}
			if got := a.Messages(kind); len(got) != 1 || got[0] != "hello" {
				t.Fatalf("Messages() = %v, want [hello]", got)
			}
			if a.Revision(kind) == 0 {
				t.Error("Revision() = 0, want non-zero after enqueue")
			}
			taken := a.Take(kind)
			if len(taken) != 1 || taken[0].Text != "hello" {
				t.Fatalf("Take() = %v, want the enqueued payload", taken)
			}
			if a.Len(kind) != 0 {
				t.Fatalf("Len() after Take() = %d, want 0", a.Len(kind))
			}
		})
	}
}

// 空输入必须被拒，且不能污染队列
func TestEnqueueRejectsEmptyPayload(t *testing.T) {
	a := New(Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil})
	for _, kind := range []QueueKind{QueueSteering, QueueFollowUp} {
		if err := a.Enqueue(kind, PendingPayload{}); err == nil {
			t.Errorf("Enqueue(%s, empty) error = nil, want error", kindName(kind))
		}
		if a.Len(kind) != 0 {
			t.Errorf("Len(%s) = %d after rejected enqueue, want 0", kindName(kind), a.Len(kind))
		}
	}
}

// Clear 只清目标队列，另一条队列必须原样保留
func TestClearLeavesOtherQueueUntouched(t *testing.T) {
	a := New(Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil})
	if err := a.Enqueue(QueueSteering, PendingPayload{Text: "steer"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Enqueue(QueueFollowUp, PendingPayload{Text: "follow"}); err != nil {
		t.Fatal(err)
	}
	cleared := a.Clear(QueueFollowUp)
	if len(cleared) != 1 || cleared[0].Text != "follow" {
		t.Fatalf("Clear(FollowUp) = %v, want the follow-up payload", cleared)
	}
	if a.Len(QueueFollowUp) != 0 {
		t.Errorf("Len(FollowUp) = %d, want 0", a.Len(QueueFollowUp))
	}
	if a.Len(QueueSteering) != 1 {
		t.Errorf("Len(Steering) = %d, want 1", a.Len(QueueSteering))
	}
}

// 恢复必须保持顺序
func TestRestoreKeepsOrder(t *testing.T) {
	a := New(Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil})
	payloads := []PendingPayload{{Text: "first"}, {Text: "second"}}
	a.Restore(QueueSteering, payloads)
	if got := a.Messages(QueueSteering); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("Messages() = %v, want restored order", got)
	}
}

// Acknowledge 只处理 commit 失败后标为可重试的条目，普通入队项不受影响
func TestAcknowledgeOnlyRemovesRetryableItems(t *testing.T) {
	a := New(Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil})
	a.Restore(QueueSteering, []PendingPayload{{Text: "first"}, {Text: "second"}})
	if a.Acknowledge(QueueSteering, func(p PendingPayload) bool { return p.Text == "first" }) {
		t.Error("Acknowledge(non-retryable) = true, want false")
	}
	if got := a.Messages(QueueSteering); len(got) != 2 {
		t.Fatalf("Messages() = %v, want both items untouched", got)
	}
}

// 图片消息必须原样保留，不能退化成纯文本
func TestEnqueueKeepsImageContent(t *testing.T) {
	a := New(Options{Config: llm.AgentConfig{}, Context: llm.Context{}, Stream: nil, Events: nil})
	payload := PendingPayload{Text: "look", Images: []ImagePart{{Data: "aGk=", MIMEType: "image/png"}}}
	if err := a.Enqueue(QueueSteering, payload); err != nil {
		t.Fatal(err)
	}
	taken := a.Take(QueueSteering)
	if len(taken) != 1 || len(taken[0].Images) != 1 || taken[0].Images[0].Data != "aGk=" {
		t.Fatalf("Take() = %+v, want image preserved", taken)
	}
}

func kindName(kind QueueKind) string {
	if kind == QueueFollowUp {
		return "follow-up"
	}
	return "steering"
}
