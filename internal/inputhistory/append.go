package inputhistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type fileOps struct {
	write    func(*os.File, []byte) (int, error)
	truncate func(*os.File, int64) error
	close    func(*os.File) error
}

func (ops *fileOps) writeFile(file *os.File, data []byte) (int, error) {
	if ops.write != nil {
		return ops.write(file, data)
	}
	return file.Write(data)
}

func (ops *fileOps) truncateFile(file *os.File, length int64) error {
	if ops.truncate != nil {
		return ops.truncate(file, length)
	}
	return file.Truncate(length)
}

func (ops *fileOps) closeFile(file *os.File) error {
	if ops.close != nil {
		return ops.close(file)
	}
	return file.Close()
}

// finishFile 保证解锁失败时仍关闭描述符，并保留所有清理错误
func (store *Store) finishFile(file *os.File, locked bool) error {
	var err error
	if locked {
		err = unlockFile(file)
	}
	return errors.Join(err, store.file.ops.closeFile(file))
}

func (store *Store) appendRecord(record Record) (err error) {
	store.file.mu.Lock()
	defer store.file.mu.Unlock()
	if store.file.poisoned != nil {
		return store.file.poisoned
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(store.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	locked := false
	defer func() { err = errors.Join(err, store.finishFile(file, locked)) }()
	if err := lockFile(context.Background(), file, true); err != nil {
		return err
	}
	locked = true
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	boundary, err := store.repairTail(file)
	if err != nil {
		return err
	}
	n, err := store.file.ops.writeFile(file, data)
	if n != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	if err == nil {
		return nil
	}
	writeErr := fmt.Errorf("append input history: %w", err)
	if n > 0 {
		if rollbackErr := store.file.ops.truncateFile(file, boundary); rollbackErr != nil {
			store.file.poisoned = fmt.Errorf("input history cannot safely continue: %w", errors.Join(
				writeErr, fmt.Errorf("rollback input history: %w", rollbackErr),
			))
			return store.file.poisoned
		}
	}
	return writeErr
}

// repairTail 只截去未提交残尾，保留完整坏行和已建立索引的前缀
func (store *Store) repairTail(file *os.File) (int64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("input history is not a regular file: %s", store.path)
	}
	boundary, err := committedBoundary(file, info.Size())
	if err != nil {
		return 0, err
	}
	if boundary != info.Size() {
		if err := store.file.ops.truncateFile(file, boundary); err != nil {
			return 0, fmt.Errorf("repair input history tail: %w", err)
		}
	}
	return boundary, nil
}

func committedBoundary(file *os.File, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	var buffer [historyReadBuffer]byte
	for end := size; end > 0; {
		start := max(int64(0), end-int64(len(buffer)))
		part := buffer[:end-start]
		if _, err := file.ReadAt(part, start); err != nil {
			return 0, err
		}
		if index := bytes.LastIndexByte(part, '\n'); index >= 0 {
			return start + int64(index) + 1, nil
		}
		end = start
	}
	return 0, nil
}
