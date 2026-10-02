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
func withFileMutationQueue[T any](ctx context.Context, path string, fn func() (T, error)) (T, error) {
	var zero T
	key, err := mutationQueueKey(path)
	if err != nil {
		return zero, err
	}
	current := &mutationQueue{done: make(chan struct{})}

	mutationQueuesMu.Lock()
	previous := mutationQueues[key]
	mutationQueues[key] = current
	mutationQueuesMu.Unlock()

	// canceled 标记等待前序操作期间被取消的调用，其节点不能提前从队列摘除
	var canceled bool
	defer func() {
		mutationQueuesMu.Lock()
		if mutationQueues[key] == current {
			if canceled {
				// 取消的等待者把队尾还给前序节点，后续调用仍会等到前序操作完成
				mutationQueues[key] = previous
			} else {
				delete(mutationQueues, key)
			}
		}
		mutationQueuesMu.Unlock()
		if !canceled {
			close(current.done)
		}
	}()

	if previous != nil {
		select {
		case <-previous.done:
		case <-ctx.Done():
			// 已入队的后续调用在等待本节点的 done，关闭时机推迟到前序操作完成之后
			canceled = true
			go func() {
				<-previous.done
				close(current.done)
			}()
			return zero, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
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
