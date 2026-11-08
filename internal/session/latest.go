package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LatestSessionPath 返回 cwd 对应会话目录中修改时间最新的会话文件路径；
// 无会话时返回空串，同 mtime 按路径升序取第一个，与 ListSessions 的排序一致
func LatestSessionPath(cwd string) (string, error) {
	dir, err := DefaultDir(cwd)
	if err != nil {
		return "", err
	}
	return latestSessionPathInDir(dir)
}

// latestSessionPathInDir 只读目录项元数据，不解析会话内容
func latestSessionPathInDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("session: failed to read session directory: %w", err)
	}
	var bestPath string
	var bestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if bestPath == "" || info.ModTime().After(bestTime) || (info.ModTime().Equal(bestTime) && path < bestPath) {
			bestPath, bestTime = path, info.ModTime()
		}
	}
	return bestPath, nil
}
