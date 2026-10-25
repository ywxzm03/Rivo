package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestTerminalWrappersPreserveFileDescriptor(t *testing.T) {
	file, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer writer.Close()
	input := NewInputReader(file)
	defer input.Close()
	if wrapped, ok := input.(terminalFile); !ok || wrapped.Fd() != file.Fd() {
		t.Fatal("input wrapper lost terminal file methods")
	}
	if _, ok := NewCursorWriter(file, nil).(terminalFile); !ok {
		t.Fatal("output wrapper lost terminal file methods")
	}
}

func TestInputReaderDropsMalformedMouseReports(t *testing.T) {
	r := NewInputReader(strings.NewReader("helloM[<65;76;30Mworld"))
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "helloworld" {
		t.Fatalf("filtered input = %q", data)
	}
}

func TestInputReaderPreservesValidMouseReports(t *testing.T) {
	value := "hello\x1b[<65;76;30Mworld"
	r := NewInputReader(strings.NewReader(value))
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != value {
		t.Fatalf("valid report changed: %q", data)
	}
}

func TestFilterMouseRunesHandlesSplitReports(t *testing.T) {
	var pending string
	var got string
	for _, part := range []string{"[<", "64;94;23", "M", "hello"} {
		clean, handled := filterMouseRunes(part, &pending)
		if !handled && part != "hello" {
			t.Fatalf("part %q was not handled", part)
		}
		got += clean
	}
	if got != "hello" || pending != "" {
		t.Fatalf("filtered split report = %q pending=%q", got, pending)
	}
}

func TestFilterMouseRunesPreservesStandaloneBracket(t *testing.T) {
	var pending string
	clean, handled := filterMouseRunes("[", &pending)
	if handled || clean != "[" || pending != "" {
		t.Fatalf("standalone bracket = %q handled=%v pending=%q", clean, handled, pending)
	}
}

func TestFilterMouseRunesPreservesOrdinaryBracketText(t *testing.T) {
	var pending string
	clean, handled := filterMouseRunes("[abc", &pending)
	if handled || clean != "[abc" || pending != "" {
		t.Fatalf("ordinary bracket text = %q handled=%v pending=%q", clean, handled, pending)
	}
}

func TestFilterMouseRunesPreservesMalformedNumericText(t *testing.T) {
	var pending string
	value := "[<tag>more] [<64;bad;23M"
	clean, handled := filterMouseRunes(value, &pending)
	if handled || clean != value || pending != "" {
		t.Fatalf("malformed bracket text = %q handled=%v pending=%q", clean, handled, pending)
	}
}

func TestInputReaderPreservesBracketedPaste(t *testing.T) {
	value := "\x1b[200~[<64;94;23M [Markdown]\ntext 中文 👋\x1b[201~"
	var parts []io.Reader
	for i := range len(value) {
		parts = append(parts, strings.NewReader(value[i:i+1]))
	}
	for _, test := range []struct {
		name  string
		input io.Reader
	}{
		{name: "whole", input: strings.NewReader(value)},
		{name: "bytewise", input: io.MultiReader(parts...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := NewInputReader(test.input)
			defer reader.Close()
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != value {
				t.Fatalf("bracketed paste changed: %q", data)
			}
		})
	}
}

func TestInputReaderParsesSplitMouseReports(t *testing.T) {
	report := "\x1b[<64;105;13M"
	for split := 1; split < len(report); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			input := io.MultiReader(strings.NewReader("draft"+report[:split]), strings.NewReader(report[split:]+"\r"))
			model, mice := runTerminalInput(t, input)
			if model.editor.Value() != "draft" || len(mice) != 1 || mice[0].Button != tea.MouseButtonWheelUp {
				t.Fatalf("split %d: editor=%q mouse=%+v", split, model.editor.Value(), mice)
			}
		})
	}
}

func TestInputReaderParsesRapidMouseReports(t *testing.T) {
	input := "draft" + strings.Repeat("\x1b[<64;105;13M\x1b[<65;105;13M", 100) + "\r"
	model, mice := runTerminalInput(t, strings.NewReader(input))
	if model.editor.Value() != "draft" || len(mice) != 200 {
		t.Fatalf("rapid scroll: editor=%q mouse count=%d", model.editor.Value(), len(mice))
	}
	for i, mouse := range mice {
		button := tea.MouseButtonWheelUp
		if i%2 != 0 {
			button = tea.MouseButtonWheelDown
		}
		if mouse.Button != button || mouse.X != 104 || mouse.Y != 12 {
			t.Fatalf("report %d = %+v", i, mouse)
		}
	}
}

func TestInputReaderParsesBytewiseMouseAndPaste(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		mice              int
	}{
		{name: "mouse", input: "draft\x1b[<64;105;13M\x1b[<65;105;13M\r", want: "draft", mice: 2},
		// Bubble Tea's Windows fallback decodes ANSI input; UTF-8 preservation is
		// checked directly in TestInputReaderPreservesBracketedPaste
		{name: "paste", input: "\x1b[200~[<64;105;13M [Markdown] text\x1b[201~\r", want: "[<64;105;13M [Markdown] text"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var parts []io.Reader
			for i := range len(test.input) {
				parts = append(parts, strings.NewReader(test.input[i:i+1]))
			}
			model, mice := runTerminalInput(t, io.MultiReader(parts...))
			if model.editor.Value() != test.want || len(mice) != test.mice {
				t.Fatalf("editor=%q mouse count=%d", model.editor.Value(), len(mice))
			}
		})
	}
}

// Shift+Left 撤回 steering:读取器把终端序列交给 Bubble Tea 后应触发撤回
func TestInputReaderRoutesShiftLeftToDequeue(t *testing.T) {
	model := testModel(80, 24)
	model.busy = true
	restored := 0
	queued := 0
	model.handlers.RestoreSteering = func() []string {
		restored++
		return []string{"queued"}
	}
	model.handlers.EnqueueSteering = func(string) error {
		queued++
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader := NewInputReader(strings.NewReader("draft\x1b[1;2D\r"))
	defer reader.Close()
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(reader), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyEnter {
				return tea.QuitMsg{}
			}
			return msg
		}))
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatalf("restore calls = %d, want one", restored)
	}
	if queued != 0 {
		t.Fatalf("queue submissions = %d, want none", queued)
	}
	if got, want := model.editor.Value(), "queued\n\ndraft"; got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}
}

// Shift+Right 撤回 follow-up
func TestInputReaderRoutesShiftRightToFollowUp(t *testing.T) {
	model := testModel(80, 24)
	restored := 0
	model.handlers.RestoreFollowUpMessages = func() []llm.UserMessage {
		restored++
		return []llm.UserMessage{textUserMessage("queued follow-up")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader := NewInputReader(strings.NewReader("draft\x1b[1;2C\r"))
	defer reader.Close()
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(reader), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyEnter {
				return tea.QuitMsg{}
			}
			return msg
		}))
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatalf("restore calls = %d, want one", restored)
	}
	if got, want := model.editor.Value(), "queued follow-up\n\ndraft"; got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}
}

// 撤回键只认 Shift+左右,方向键本身留给任务选择
func TestDequeueKeysDoNotUseArrows(t *testing.T) {
	for _, test := range []struct {
		name          string
		key           tea.KeyMsg
		steer, follow bool
	}{
		{name: "shift-left", key: tea.KeyMsg{Type: tea.KeyShiftLeft}, steer: true},
		{name: "shift-right", key: tea.KeyMsg{Type: tea.KeyShiftRight}, follow: true},
		{name: "plain-up", key: tea.KeyMsg{Type: tea.KeyUp}},
		{name: "plain-down", key: tea.KeyMsg{Type: tea.KeyDown}},
		{name: "alt-up", key: tea.KeyMsg{Type: tea.KeyUp, Alt: true}},
		{name: "alt-down", key: tea.KeyMsg{Type: tea.KeyDown, Alt: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isSteeringDequeueKey(test.key); got != test.steer {
				t.Fatalf("isSteeringDequeueKey = %v, want %v", got, test.steer)
			}
			if got := isFollowUpDequeueKey(test.key); got != test.follow {
				t.Fatalf("isFollowUpDequeueKey = %v, want %v", got, test.follow)
			}
		})
	}
}

func TestInputReaderPreservesSmallReads(t *testing.T) {
	value := "hello\x1b[<64;105;13M\x1b[200~" + strings.Repeat("[<65;2;3M", 100) + "\x1b[201~world"
	reader := NewInputReader(strings.NewReader(value))
	defer reader.Close()
	var got strings.Builder
	buf := make([]byte, 3)
	for {
		n, err := reader.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.String() != value {
		t.Fatalf("small reads changed input: %q", got.String())
	}
}

func TestInputReaderPreservesStandaloneEscape(t *testing.T) {
	source, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	defer writer.Close()
	reader := NewInputReader(source)
	defer reader.Close()
	if _, err := writer.WriteString("\x1b"); err != nil {
		t.Fatal(err)
	}
	result := make(chan inputRead, 1)
	go func() {
		buf := make([]byte, 256)
		n, err := reader.Read(buf)
		result <- inputRead{data: buf[:n], err: err}
	}()
	select {
	case got := <-result:
		if string(got.data) != "\x1b" || got.err != nil {
			t.Fatalf("standalone escape = %q, err=%v", got.data, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("standalone Escape blocked waiting for more input")
	}
}

func TestInputReaderCloseWithPendingRead(t *testing.T) {
	for _, test := range []struct {
		name       string
		cancelable bool
	}{
		{name: "cancelable", cancelable: true},
		{name: "fallback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &pendingInputReader{
				started: make(chan struct{}), canceled: make(chan struct{}),
				release: make(chan struct{}), closed: make(chan struct{}),
				cancelable: test.cancelable,
			}
			defer func() {
				select {
				case <-source.release:
				default:
					close(source.release)
				}
			}()
			reader := &inputReader{source: source}
			readDone := make(chan error, 1)
			go func() {
				_, err := reader.Read(make([]byte, 256))
				readDone <- err
			}()
			<-source.started
			closeDone := make(chan error, 1)
			go func() { closeDone <- reader.Close() }()
			<-source.canceled
			if test.cancelable {
				select {
				case <-source.closed:
					t.Fatal("source closed before the pending read finished")
				case <-time.After(20 * time.Millisecond):
				}
			} else {
				select {
				case <-source.closed:
				case <-time.After(time.Second):
					t.Fatal("Close blocked on an uncancelable read")
				}
			}
			close(source.release)
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close did not finish after the pending read returned")
			}
			select {
			case err := <-readDone:
				if err != io.ErrClosedPipe {
					t.Fatalf("canceled read error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending read did not finish after Close")
			}
		})
	}
}

// pendingInputReader keeps a canceled read active until the test releases it
type pendingInputReader struct {
	started, canceled, release, closed chan struct{}
	cancelable                         bool
}

func (r *pendingInputReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, io.ErrClosedPipe
}

func (r *pendingInputReader) Cancel() bool {
	close(r.canceled)
	return r.cancelable
}

func (r *pendingInputReader) Close() error {
	close(r.closed)
	return nil
}

// runTerminalInput exercises Bubble Tea's byte parser before events reach the editor
func runTerminalInput(t *testing.T, input io.Reader) (*Model, []tea.MouseMsg) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := testModel(80, 24)
	var mice []tea.MouseMsg
	reader := NewInputReader(input)
	defer reader.Close()
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(reader), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if mouse, ok := msg.(tea.MouseMsg); ok {
				mice = append(mice, mouse)
			}
			if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyEnter {
				return tea.QuitMsg{}
			}
			return msg
		}))
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	return model, mice
}
