package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

// dumpPanic 把 panic 现场写到临时目录,供事后定位。
// TUI 崩溃会清屏,stderr 上的堆栈通常看不到
func dumpPanic(where string, recovered any) {
	path := filepath.Join(os.TempDir(), "rivo-panic.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "=== %s panic at %s in %s ===\n%v\n%s\n",
		where, time.Now().Format(time.RFC3339), filepath.Base(os.Args[0]), recovered, debug.Stack())
}
