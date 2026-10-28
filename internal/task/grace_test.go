package task

import (
	"testing"
	"time"
)

// 宽限期必须可注入：测试用毫秒级宽限期，不必真等 30 秒
func TestStoreGracePeriodIsInjectable(t *testing.T) {
	store := NewStore()
	store.GracePeriod = 10 * time.Millisecond

	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(id, token, nil) {
		t.Fatal("task did not transition to completed")
	}
	task, ok := store.Task(id)
	if !ok {
		t.Fatal("task missing after completion")
	}
	base := task.Base()
	if base.EvictAfter == nil {
		t.Fatal("EvictAfter = nil, want a deadline")
	}
	// 截止时间必须按注入的宽限期计算，而不是硬编码的 30 秒
	if got := base.EvictAfter.Sub(*base.EndTime); got != 10*time.Millisecond {
		t.Fatalf("grace period = %v, want 10ms", got)
	}
}

// 未设置宽限期的 store 必须使用默认值，保持生产行为不变
func TestStoreUsesDefaultGracePeriod(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(id, token, nil) {
		t.Fatal("task did not transition to completed")
	}
	task, _ := store.Task(id)
	base := task.Base()
	if base.EvictAfter == nil {
		t.Fatal("EvictAfter = nil, want a deadline")
	}
	if got := base.EvictAfter.Sub(*base.EndTime); got != defaultGracePeriod {
		t.Fatalf("grace period = %v, want %v", got, defaultGracePeriod)
	}
}
