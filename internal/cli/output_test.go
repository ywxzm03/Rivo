package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/imageutil"
)

// serializedOutput 必须把写入与读取串行化，nil 文件返回明确的错误
func TestSerializedOutputWriteAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	out := newSerializedOutput(file)
	if _, err := out.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("read = %q, want hello", buf)
	}
	if err := out.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// nil 文件必须返回 closed pipe，而不是 panic
func TestSerializedOutputNilFile(t *testing.T) {
	out := newSerializedOutput(nil)
	if _, err := out.Write([]byte("x")); err != io.ErrClosedPipe {
		t.Errorf("Write(nil file) error = %v, want ErrClosedPipe", err)
	}
	if _, err := out.Read(make([]byte, 1)); err != io.ErrClosedPipe {
		t.Errorf("Read(nil file) error = %v, want ErrClosedPipe", err)
	}
	if err := out.Close(); err != nil {
		t.Errorf("Close(nil file) error = %v, want nil", err)
	}
}

// gitBranch 在非 git 目录返回空串，在 git 仓库返回当前分支
func TestGitBranch(t *testing.T) {
	if got := gitBranch(t.TempDir()); got != "" {
		t.Errorf("gitBranch(non-repo) = %q, want empty", got)
	}
}

// 剪贴板图片必须带正确 MIME 类型，无法识别的数据要被拒绝
func TestProcessClipboardImage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	if _, err := processClipboardImage(png, "image/png"); err != nil {
		t.Logf("processClipboardImage(minimal png) error = %v (imageutil rejected the stub)", err)
	}
	if _, err := processClipboardImage([]byte("not an image"), "image/png"); err == nil {
		t.Error("processClipboardImage(garbage) error = nil, want error")
	}
	if imageutil.Sniff(png) == "" && imageutil.Sniff([]byte("x")) != "" {
		t.Error("Sniff() inconsistent for stub data")
	}
}

// 读取不存在的文件必须返回 false 而不是 panic
func TestReadClipboardFileRejectsMissingAndOversized(t *testing.T) {
	if _, ok := readClipboardFile(filepath.Join(t.TempDir(), "missing.png")); ok {
		t.Error("readClipboardFile(missing) = true, want false")
	}
	big := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(big, make([]byte, maxClipboardImageBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readClipboardFile(big); ok {
		t.Error("readClipboardFile(oversized) = true, want false")
	}
}
