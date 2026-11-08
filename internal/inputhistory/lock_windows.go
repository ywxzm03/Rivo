//go:build windows

package inputhistory

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryFileLock(file *os.File, exclusive bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	// 覆盖 EOF 以后的范围，防止后续追加绕过已有锁
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, ^uint32(0), ^uint32(0), &overlapped)
}

func lockContended(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped)
}
