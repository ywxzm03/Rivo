package tool

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"syscall"
)

type mutationQueue struct {
	done chan struct{}
}

var (
	mutationQueuesMu sync.Mutex
	mutationQueues   = make(map[string]*mutationQueue)
)

// withFileMutationQueue 按文件路径串行执行变更操作
func withFileMutationQueue(ctx context.Context, path string, fn func() error) error {
	key, err := mutationQueueKey(path)
	if err != nil {
		return err
	}
	current := &mutationQueue{done: make(chan struct{})}

	mutationQueuesMu.Lock()
	previous := mutationQueues[key]
	mutationQueues[key] = current
	mutationQueuesMu.Unlock()

	if previous != nil {
		<-previous.done
	}

	defer func() {
		close(current.done)
		mutationQueuesMu.Lock()
		if mutationQueues[key] == current {
			delete(mutationQueues, key)
		}
		mutationQueuesMu.Unlock()
	}()

	return fn()
}

// mutationQueueKey 解析已有符号链接，目标不存在时退回绝对路径
func mutationQueueKey(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absPath = filepath.Clean(absPath)
	resolved, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return absPath, nil
	}
	return "", err
}
