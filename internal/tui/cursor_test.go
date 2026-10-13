package tui

import (
	"bytes"
	"testing"
)

func TestCursorWriterReplacesRendererCursor(t *testing.T) {
	var output bytes.Buffer
	writer := NewCursorWriter(&output, func() (int, int) { return 4, 7 })
	input := []byte("frame\x1b[24;H")
	n, err := writer.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("write = (%d, %v)", n, err)
	}
	if got := output.String(); got != "frame\x1b[4;7H" {
		t.Fatalf("output = %q", got)
	}
	output.Reset()
	if _, err := writer.Write([]byte("next\x1b[24;H")); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "\x1b[24;1Hnext\x1b[4;7H" {
		t.Fatalf("second output = %q", got)
	}
}

// 退出 alt screen 后停止注入 CUP，避免把主屏光标钉在 TUI 底边行
func TestCursorWriterStopsInjectingAfterAltScreenExit(t *testing.T) {
	var output bytes.Buffer
	writer := NewCursorWriter(&output, nil)
	// 运行中最后一帧：帧尾 CUP 学到 bottom=24
	writer.Write([]byte("frame\x1b[24;H"))
	output.Reset()
	// bubbletea 退出序列：?1049l 之后的写入不得再注入
	writer.Write([]byte("\x1b[?1049l"))
	if got := output.String(); got != "\x1b[?1049l" {
		t.Fatalf("alt-screen exit output = %q", got)
	}
	output.Reset()
	writer.Write([]byte("\x1b[?25h"))
	if got := output.String(); got != "\x1b[?25h" {
		t.Fatalf("post-exit output = %q, want no injected CUP", got)
	}
}
