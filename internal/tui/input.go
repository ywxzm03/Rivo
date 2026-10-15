package tui

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/cancelreader"
)

type terminalFile interface {
	io.ReadWriteCloser
	Fd() uintptr
}

var sgrMouse = regexp.MustCompile(`\x1b?\[<([0-9]+);[0-9]+;[0-9]+[mM]`)

const escapeWait = 25 * time.Millisecond

// mouseReportEnd validates the numeric SGR mouse grammar and returns the byte
// after a complete report. An incomplete numeric prefix is kept for the next
// read; ordinary bracket text is reported as invalid and remains untouched.
func mouseReportEnd(value string, payload int) (end int, complete, incomplete bool) {
	start := payload
	readNumber := func(index int) (int, bool, bool) {
		begin := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == begin {
			return index, false, index == len(value)
		}
		return index, true, false
	}
	var ok bool
	if start >= len(value) {
		return len(value), false, true
	}
	if start, ok, incomplete = readNumber(start); !ok {
		return start, false, incomplete
	}
	if start >= len(value) {
		return len(value), false, true
	}
	if value[start] != ';' {
		return start, false, false
	}
	start++
	if start, ok, incomplete = readNumber(start); !ok {
		return start, false, incomplete
	}
	if start >= len(value) {
		return len(value), false, true
	}
	if value[start] != ';' {
		return start, false, false
	}
	start++
	if start, ok, incomplete = readNumber(start); !ok {
		return start, false, incomplete
	}
	if start >= len(value) {
		return len(value), false, true
	}
	if value[start] != 'm' && value[start] != 'M' {
		return start, false, false
	}
	return start + 1, true, false
}

func mouseCandidate(value string, index int) (payload int, prefix int, ok bool) {
	switch {
	case strings.HasPrefix(value[index:], "^[[<"):
		return index + 4, 4, true
	case strings.HasPrefix(value[index:], "M[<"):
		return index + 3, 3, true
	case strings.HasPrefix(value[index:], "[<") && (index == 0 || value[index-1] != '\x1b'):
		return index + 2, 2, true
	default:
		return 0, 0, false
	}
}

// NewInputReader delivers complete mouse reports to Bubble Tea and removes
// reports lacking ESC. Close releases the reader without closing the terminal.
func NewInputReader(source io.Reader) io.ReadCloser {
	cancelable, err := cancelreader.NewReader(source)
	reader := &inputReader{source: cancelable, err: err}
	if file, ok := source.(terminalFile); ok {
		reader.file = file
	}
	return reader
}

type inputReader struct {
	source  cancelreader.CancelReader
	readMu  sync.Mutex
	pending []byte
	ready   []byte
	reads   chan inputRead
	err     error
	file    terminalFile
}

type inputRead struct {
	data []byte
	err  error
}

// Read drains a complete input packet before fetching more terminal bytes
func (r *inputReader) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for {
		// 1. Keep packet leftovers separate from unfinished reports
		if len(r.ready) > 0 {
			count := copy(dst, r.ready)
			r.ready = r.ready[count:]
			return count, nil
		}
		if len(r.pending) > 0 {
			r.ready, r.pending = stripMalformedMouse(r.pending)
			if len(r.ready) > 0 {
				continue
			}
		}
		if r.err != nil {
			if string(r.pending) == "\x1b" {
				r.ready, r.pending = r.pending, nil
				continue
			}
			return 0, r.err
		}
		// 2. A lone ESC may be a key or the first byte of a mouse report
		if r.reads == nil {
			reads := make(chan inputRead, 1)
			r.reads = reads
			go func() {
				buf := make([]byte, 256)
				r.readMu.Lock()
				n, err := r.source.Read(buf)
				r.readMu.Unlock()
				reads <- inputRead{data: buf[:n], err: err}
			}()
		}
		var timeout <-chan time.Time
		var timer *time.Timer
		if string(r.pending) == "\x1b" {
			timer = time.NewTimer(escapeWait)
			timeout = timer.C
		}
		select {
		case result := <-r.reads:
			if timer != nil {
				timer.Stop()
			}
			r.reads, r.err = nil, result.err
			r.pending = append(r.pending, result.data...)
		case <-timeout:
			r.ready, r.pending = r.pending, nil
		}
	}
}

func (r *inputReader) Fd() uintptr {
	if r.file == nil {
		return 0
	}
	return r.file.Fd()
}

func (r *inputReader) Write(data []byte) (int, error) {
	if r.file == nil {
		return 0, io.ErrClosedPipe
	}
	return r.file.Write(data)
}

func (r *inputReader) Close() error {
	if r.source == nil {
		return nil
	}
	if r.source.Cancel() {
		// Let the active read consume cancellation before releasing its resources
		r.readMu.Lock()
		defer r.readMu.Unlock()
	}
	return r.source.Close()
}

// stripMalformedMouse returns one input packet and retains unfinished prefixes
func stripMalformedMouse(data []byte) ([]byte, []byte) {
	value := string(data)
	var clean []byte
	for i := 0; i < len(value); {
		// Keep the introducer together, including a split bracketed paste prefix
		if value[i:] == "\x1b" || value[i:] == "\x1b[" || strings.HasPrefix("\x1b[200~", value[i:]) {
			return clean, data[i:]
		}
		if strings.HasPrefix(value[i:], "\x1b[200~") {
			if len(clean) > 0 {
				return clean, data[i:]
			}
			end := strings.Index(value[i+len("\x1b[200~"):], "\x1b[201~")
			if end < 0 {
				return clean, []byte(value[i:])
			}
			end += i + len("\x1b[200~") + len("\x1b[201~")
			return data[i:end], data[end:]
		}
		if strings.HasPrefix(value[i:], "\x1b[<") {
			if len(clean) > 0 {
				return clean, data[i:]
			}
			end, complete, incomplete := mouseReportEnd(value, i+3)
			if incomplete {
				return clean, data[i:]
			}
			if complete {
				// One report per packet avoids splitting it at Bubble Tea's buffer edge
				return data[i:end], data[end:]
			}
		}
		payload, prefix, ok := mouseCandidate(value, i)
		if !ok {
			clean = append(clean, value[i])
			i++
			continue
		}
		end, complete, incomplete := mouseReportEnd(value, payload)
		if incomplete {
			return clean, []byte(value[i:])
		}
		if complete {
			i = end
			continue
		}
		clean = append(clean, value[i:i+prefix]...)
		i += prefix
	}
	return clean, nil
}

// filterMouseRunes removes SGR mouse reports that Bubble Tea may deliver as
// ordinary rune messages when a terminal splits the escape sequence.
func filterMouseRunes(value string, pending *string) (string, bool) {
	hadPending := pending != nil && *pending != ""
	if pending != nil && *pending != "" {
		value = *pending + value
		*pending = ""
	}
	var clean strings.Builder
	handled := hadPending
	for i := 0; i < len(value); {
		payload, prefix, ok := mouseCandidate(value, i)
		if !ok {
			clean.WriteByte(value[i])
			i++
			continue
		}
		// A real ESC-prefixed report is Bubble Tea's normal input. Leave it
		// intact; the raw reader only removes reports lacking the escape byte.
		if i > 0 && value[i-1] == '\x1b' {
			clean.WriteString(value[i : i+prefix])
			i += prefix
			continue
		}
		end, complete, incomplete := mouseReportEnd(value, payload)
		if incomplete {
			if pending != nil {
				*pending = value[i:]
			}
			return clean.String(), true
		}
		if complete {
			handled = true
			i = end
			continue
		}
		clean.WriteString(value[i : i+prefix])
		i += prefix
	}
	for _, prefix := range []string{"^[[<", "M[<", "[<"} {
		text := clean.String()
		if !strings.HasSuffix(text, prefix) {
			continue
		}
		clean.Reset()
		clean.WriteString(strings.TrimSuffix(text, prefix))
		if pending != nil {
			*pending = prefix
		}
		return clean.String(), true
	}
	return clean.String(), handled
}

func (m *Model) filterMouseKey(msg tea.KeyMsg) (string, bool) {
	if msg.Type != tea.KeyRunes || msg.Paste {
		return "", false
	}
	value := string(msg.Runes)
	if sgrMouse.FindString(value) == value {
		// Let the normal raw-wheel path consume a complete report.
		return value, false
	}
	return filterMouseRunes(value, &m.mouseFragment)
}

// rawMouseWheel recognizes mouse reports that escaped Bubble Tea's parser.
func rawMouseWheel(msg tea.KeyMsg) (int, bool) {
	if msg.Type != tea.KeyRunes || msg.Paste {
		return 0, false
	}
	value := string(msg.Runes)
	match := sgrMouse.FindStringSubmatch(value)
	if len(match) == 2 {
		button, err := strconv.Atoi(match[1])
		if err == nil {
			switch button {
			case 64:
				return -1, true
			case 65:
				return 1, true
			}
		}
	}
	return 0, false
}

// isTerminalControlKey prevents incomplete CSI/SGR reports from becoming text.
func isTerminalControlKey(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || msg.Paste {
		return false
	}
	value := string(msg.Runes)
	if strings.ContainsRune(value, '\x1b') {
		return true
	}
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\t' {
			return true
		}
	}
	// X10 鼠标报告没有 ESC 前缀时固定为 [M 加三个编码字节；只识别完整报告，
	// 避免把 [Markdown] 等普通文本误判为终端控制序列
	return strings.HasPrefix(value, "[M") && len([]rune(value)) == 5
}
