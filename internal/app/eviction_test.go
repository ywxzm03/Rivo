package app

import (
	"testing"
	"time"

	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// 驱逐间隔与宽限期必须可注入：测试用毫秒级配置，不必真等 30 秒
func TestEvictionLoopHonorsInjectedTiming(t *testing.T) {
	a := evictOnlyApp(t)
	defer a.ShutdownSubagents()

	store := a.SubagentStore()
	store.GracePeriod = 10 * time.Millisecond
	a.SetEvictionInterval(5 * time.Millisecond)

	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "fast job"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, nil) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "fast job"}) {
		t.Fatal("notification was not enqueued")
	}

	// 毫秒级宽限期加毫秒级轮询，最多几百毫秒就该驱逐
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := store.Task(taskID); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("terminal task survived the injected grace period")
}
