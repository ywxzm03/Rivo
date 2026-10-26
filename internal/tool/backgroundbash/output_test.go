package backgroundbash

import (
	"os"
	"path/filepath"
	"testing"

	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// 输出必须被截断到上限，并把截断标记暴露给调用方
func TestLimitedBackgroundOutputTruncatesAtLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	out := &limitedBackgroundOutput{file: file, max: 8}

	// 写入未超限的数据：不应标记截断
	if n, err := out.Write([]byte("12345")); err != nil || n != 5 {
		t.Fatalf("Write(5) = %d, %v; want 5, nil", n, err)
	}
	if truncated, used := out.Stats(); truncated || used != 5 {
		t.Fatalf("Stats() = %v/%d, want false/5", truncated, used)
	}

	// 写入跨越上限的数据：只保留剩余额度，并标记截断
	if n, err := out.Write([]byte("67890")); err != nil || n != 5 {
		t.Fatalf("Write(5) = %d, %v; want the full length reported", n, err)
	}
	if truncated, used := out.Stats(); !truncated || used != 8 {
		t.Fatalf("Stats() = %v/%d, want true/8", truncated, used)
	}

	// 已达上限后的写入必须丢弃内容但仍报告成功
	if n, err := out.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("Write(after limit) = %d, %v; want 4, nil", n, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "12345678" {
		t.Fatalf("file content = %q, want the first 8 bytes", data)
	}
}

// 写入底层文件失败时必须把错误抛给调用方
func TestLimitedBackgroundOutputPropagatesWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// 关闭文件让后续写入失败
	file.Close()

	out := &limitedBackgroundOutput{file: file, max: 16}
	if _, err := out.Write([]byte("data")); err == nil {
		t.Error("Write() error = nil, want the underlying write error")
	}
}

// 未知任务没有输出文件
func TestOutputFileUnknownTask(t *testing.T) {
	store := taskpkg.NewStore()
	if got := outputFile(store, "missing"); got != "" {
		t.Errorf("outputFile(unknown) = %q, want empty", got)
	}
}

// 非 ExitError 一律返回 -1，区分"未启动"与"正常退出"
func TestExitCodeForNonExitError(t *testing.T) {
	if got := exitCode(nil); got != -1 {
		t.Errorf("exitCode(nil) = %d, want -1", got)
	}
}
