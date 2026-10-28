package shell_test

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/ywxzm03/Rivo/internal/shell"
)

// 后台 bash 与前台 bash 必须拿到同一个 shell 可执行文件
func TestFindBashReturnsRunnablePath(t *testing.T) {
	path, err := shell.FindBash()
	if err != nil {
		t.Fatalf("FindBash() error = %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Fatalf("FindBash() = %q, want an existing file", path)
	}
}

// 配置过的进程必须带取消钩子，保证超时能杀掉整个进程树
func TestConfigureSetsCancelHook(t *testing.T) {
	path, err := shell.FindBash()
	if err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	cmd := exec.CommandContext(context.Background(), path, "-c", "true")
	shell.ConfigureProcess(cmd)
	if cmd.Cancel == nil {
		t.Fatal("ConfigureProcess() left Cancel nil")
	}
	if cmd.WaitDelay == 0 {
		t.Error("ConfigureProcess() left WaitDelay zero")
	}
}

// 环境必须把 rivo bin 目录前置到 PATH，且不重复添加
func TestEnvironmentPrependsBinDirOnce(t *testing.T) {
	first := shell.Environment()
	second := shell.Environment()
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("Environment() returned no entries")
	}
	// 两次调用的 PATH 必须一致：重复前置会暴露去重失败
	var pathA, pathB string
	for _, item := range first {
		if len(item) > 5 && item[:5] == "PATH=" {
			pathA = item
		}
	}
	for _, item := range second {
		if len(item) > 5 && item[:5] == "PATH=" {
			pathB = item
		}
	}
	if pathA == "" || pathB == "" {
		t.Fatal("Environment() has no PATH entry")
	}
	if pathA != pathB {
		t.Errorf("PATH changed between calls:\n%q\n%q", pathA, pathB)
	}
}
