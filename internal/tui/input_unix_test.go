//go:build unix

package tui

import (
	"os"
	"testing"
	"time"
)

// TestInputReaderCloseInterruptsTerminalRead 使用支持取消读取的 Unix 管道验证收尾
func TestInputReaderCloseInterruptsTerminalRead(t *testing.T) {
	source, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	defer writer.Close()
	reader := NewInputReader(source)
	started, stopped := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := reader.Read(make([]byte, 256))
		stopped <- err
	}()
	<-started
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err == nil {
			t.Fatal("closed reader returned no error")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal read survived Close")
	}
}
