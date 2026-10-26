package shell

import (
	"os"
	"path/filepath"
	"strings"
)

// Environment 返回给 shell 子进程用的环境：把 rivo bin 目录前置到 PATH，
// 让工具下载的可执行文件（如 ripgrep）对命令可见
func Environment() []string {
	env := append([]string(nil), os.Environ()...)
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return env
	}
	binDir := filepath.Join(cacheDir, "rivo", "bin")
	for i, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, "PATH") {
			env[i] = key + "=" + prependPath(binDir, value)
			return env
		}
	}
	return append(env, "PATH="+binDir)
}

// prependPath 只在目录尚未出现时前置，避免多次调用叠加重复项
func prependPath(entry, current string) string {
	for _, item := range strings.Split(current, string(os.PathListSeparator)) {
		if item == entry {
			return current
		}
	}
	if current == "" {
		return entry
	}
	return entry + string(os.PathListSeparator) + current
}
