package app

import (
	"runtime"
	"testing"
	"time"
)

// 关闭后通知循环与驱逐循环必须退出，否则每次换会话都会泄漏两条 goroutine
func TestAppCloseStopsBackgroundLoops(t *testing.T) {
	a := evictOnlyApp(t)
	before := runtime.NumGoroutine()
	a.Close()
	// 给两条循环一点时间观察到关闭信号
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() < before+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after >= before+1 {
		t.Fatalf("goroutines after Close = %d, before = %d", after, before)
	}
}

// Close 必须可重入，重复调用不能 panic
func TestAppCloseIsIdempotent(t *testing.T) {
	a := evictOnlyApp(t)
	a.Close()
	a.Close()
}
