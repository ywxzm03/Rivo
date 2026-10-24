package tui

import (
	"bytes"
	"io"
	"regexp"
	"strconv"

	"github.com/charmbracelet/x/ansi"
)

var finalCursorPosition = regexp.MustCompile(`\x1b\[([0-9]+)(?:;[0-9]*)?H$`)

// resetAltScreenSaveCursorMode 退出 alt screen 的 DEC 私有模式序列
const resetAltScreenSaveCursorMode = "\x1b[?1049l"

// NewCursorWriter 在 Bubble Tea 渲染器把光标停到帧下方后恢复编辑器光标，
// 使输入法候选窗贴近输入框
func NewCursorWriter(output io.Writer, position func() (row, column int)) io.Writer {
	writer := &cursorWriter{output: output, position: position}
	if file, ok := output.(terminalFile); ok {
		writer.file = file
	}
	return writer
}

type cursorWriter struct {
	output   io.Writer
	position func() (row, column int)
	bottom   int
	file     terminalFile
}

func (w *cursorWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	// ?1049l 表示退出 alt screen，其后的写入属于主屏恢复序列，
	// 注入光标会把主屏钉在 TUI 底边行，因此清零 bottom
	if bytes.Contains(data, []byte(resetAltScreenSaveCursorMode)) {
		w.bottom = 0
	}
	if w.bottom > 0 {
		data = append([]byte(ansi.CursorPosition(1, w.bottom)), data...)
	}
	row, column := 0, 0
	if w.position != nil {
		row, column = w.position()
	}
	if row > 0 && column > 0 {
		if match := finalCursorPosition.FindStringSubmatchIndex(string(data)); match != nil {
			if value, err := strconv.Atoi(string(data[match[2]:match[3]])); err == nil {
				w.bottom = value
			}
			position := []byte(ansi.CursorPosition(column, row))
			data = append(append(append([]byte(nil), data[:match[0]]...), position...), data[match[1]:]...)
		}
	} else if match := finalCursorPosition.FindStringSubmatchIndex(string(data)); match != nil {
		if value, err := strconv.Atoi(string(data[match[2]:match[3]])); err == nil {
			w.bottom = value
		}
	}
	_, err := w.output.Write(data)
	return originalLength, err
}

func (w *cursorWriter) Fd() uintptr {
	if w.file == nil {
		return 0
	}
	return w.file.Fd()
}

func (w *cursorWriter) Read(data []byte) (int, error) {
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	return w.file.Read(data)
}

func (w *cursorWriter) Close() error {
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (m *Model) CursorPosition() (row, column int) {
	return int(m.cursorRow.Load()), int(m.cursorColumn.Load())
}

func (m *Model) updateCursorPosition() {
	if m.width <= 0 || m.height <= 0 || m.selector != nil {
		m.cursorRow.Store(0)
		m.cursorColumn.Store(0)
		return
	}
	info := m.editor.LineInfo()
	visibleRow := min(max(0, info.RowOffset), max(0, m.editor.Height()-1))
	// 视口与输入框之间多一行空白，编辑器整体下移一行
	m.cursorRow.Store(int64(m.viewport.Height + 3 + visibleRow))
	m.cursorColumn.Store(int64(3 + info.CharOffset))
}
