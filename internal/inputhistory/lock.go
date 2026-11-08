package inputhistory

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	lockAttempts = 10
	lockInterval = 100 * time.Millisecond
)

// lockFile 最多尝试十次，等待间隔可取消，不把 UI 线程挂在系统锁调用上
func lockFile(ctx context.Context, file *os.File, exclusive bool) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := tryFileLock(file, exclusive)
		if err == nil {
			return nil
		}
		if !lockContended(err) || attempt == lockAttempts {
			return fmt.Errorf("lock input history: %w", err)
		}
		timer := time.NewTimer(lockInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
