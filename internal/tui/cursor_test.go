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
