package tui

import (
	"bytes"
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
	var clean []byte
	for i := 0; i < len(data); {
		start := i
		if bytes.HasPrefix(data[i:], []byte("M[<")) {
			start += 1
		} else if !bytes.HasPrefix(data[i:], []byte("[<")) || (i > 0 && data[i-1] == '\x1b') {
			clean = append(clean, data[i])
			i++
			continue
		}
		end := bytes.IndexAny(data[start:], "mM")
		if end < 0 {
			return clean, data[i:]
		}
		i = start + end + 1
	}
	if len(clean) > 0 && clean[len(clean)-1] == 'M' && !sgrMouse.Match(clean) {
		return clean[:len(clean)-1], []byte{'M'}
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
		start := i
		switch {
		case strings.HasPrefix(value[i:], "^[[<"):
			start += len("^[")
		case strings.HasPrefix(value[i:], "M[<"):
			start++
		case strings.HasPrefix(value[i:], "[<"):
		default:
			clean.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexAny(value[start:], "mM")
		if end < 0 {
			if pending != nil {
				*pending = value[i:]
			}
			return clean.String(), true
		}
		handled = true
		i = start + end + 1
	}
	for _, prefix := range []string{"^[[", "M[", "["} {
		if strings.HasSuffix(clean.String(), prefix) {
			text := clean.String()
			clean.Reset()
			clean.WriteString(strings.TrimSuffix(text, prefix))
			if pending != nil {
				*pending = prefix
			}
			return clean.String(), true
		}
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
