package tui

import (
	"io"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type terminalFile interface {
	io.ReadWriteCloser
	Fd() uintptr
}

var sgrMouse = regexp.MustCompile(`\x1b?\[<([0-9]+);[0-9]+;[0-9]+[mM]`)

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

// NewInputReader removes malformed mouse reports before Bubble Tea turns them
// into ordinary runes. Valid reports with the ESC prefix remain untouched.
func NewInputReader(source io.Reader) io.Reader {
	reader := &inputReader{source: source}
	if file, ok := source.(terminalFile); ok {
		reader.file = file
	}
	return reader
}

type inputReader struct {
	source  io.Reader
	pending []byte
	file    terminalFile
}

func (r *inputReader) Read(dst []byte) (int, error) {
	for {
		buf := make([]byte, 256)
		n, err := r.source.Read(buf)
		if n > 0 {
			data := append(r.pending, buf[:n]...)
			clean, pending := stripMalformedMouse(data)
			r.pending = pending
			if len(clean) > 0 {
				count := copy(dst, clean)
				if count < len(clean) {
					r.pending = append(clean[count:], r.pending...)
				}
				return count, nil
			}
		}
		if err != nil {
			if len(r.pending) > 0 {
				count := copy(dst, r.pending)
				r.pending = r.pending[count:]
				if count > 0 {
					return count, nil
				}
			}
			return 0, err
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
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}

func stripMalformedMouse(data []byte) ([]byte, []byte) {
	value := string(data)
	var clean []byte
	for i := 0; i < len(value); {
		if strings.HasPrefix(value[i:], "\x1b[200~") {
			end := strings.Index(value[i+len("\x1b[200~"):], "\x1b[201~")
			if end < 0 {
				return clean, []byte(value[i:])
			}
			end += i + len("\x1b[200~") + len("\x1b[201~")
			clean = append(clean, value[i:end]...)
			i = end
			continue
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
	for _, prefix := range []string{"^[", "^[[", "M[", "["} {
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
	if msg.Type != tea.KeyRunes {
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
	if msg.Type != tea.KeyRunes {
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
	return strings.Contains(value, "[<") || strings.Contains(value, "[M")
}
