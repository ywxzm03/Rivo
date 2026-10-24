// tui/preview 是独立视觉验收入口：使用固定数据，不装配真实 Agent、不写会话
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/tui"
)

const exampleThinking = "The user just said hello. I will answer briefly and wait for their next request."
const exampleAnswer = "Hi! I'm ready to help with your code — reading files, editing code, running commands, and debugging. What would you like to do?"
const exampleMarkdown = "## A small Go example\n\n中文、emoji 👋 和 **Markdown** 使用同一套宽度规则。\n\n```go\nfunc greet(name string) string {\n    return \"Hello, \" + name\n}\n```\n\n- Inline `code` and **bold text**\n- [Rivo](https://github.com/ywxzm03/Rivo)\n\n> A quoted paragraph with a long line that wraps inside the transcript.\n\n| State | Result |\n| --- | --- |\n| streaming | visible |\n| completed | cached |"

func exampleToolGroup() *tui.ToolGroupView {
	return &tui.ToolGroupView{
		ID: "preview-tools", Sealed: true,
		Calls: []tui.ToolCallView{
			{ID: "read", Name: "read", Arguments: `{"path":"/Users/wm/IdeaProjects/Rivo/internal/tui/model.go"}`, Result: "package tui\n\ntype Model struct {\n\tstate Snapshot\n}", Status: tui.ToolSuccess, Elapsed: 120 * time.Millisecond},
			{ID: "grep", Name: "grep", Arguments: `{"pattern":"Ctrl+O","path":"internal/tui"}`, Result: "internal/tui/editor.go:case \"ctrl+o\":", Status: tui.ToolSuccess, Elapsed: 180 * time.Millisecond},
			{ID: "bash", Name: "bash", Arguments: `{"command":"go test ./..."}`, Result: "ok   github.com/ywxzm03/Rivo/internal/tui  0.42s", Status: tui.ToolSuccess, Elapsed: 420 * time.Millisecond},
			{ID: "write", Name: "write", Arguments: `{"path":"/tmp/preview.txt"}`, Result: "Successfully wrote to /tmp/preview.txt", Status: tui.ToolSuccess, Elapsed: 90 * time.Millisecond},
		},
		Thinking: []tui.ThinkingBlockView{{Index: 0, Text: exampleThinking, Tokens: 260, TokensExact: false}},
	}
}

type preview struct {
	state    tui.Snapshot
	send     func(tea.Msg)
	cancel   context.CancelFunc
	sequence int
	scene    string
}

func initialState() tui.Snapshot {
	cwd, _ := os.Getwd()
	branch, _ := exec.Command("git", "branch", "--show-current").Output()
	levels := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	models := []tui.ModelOption{
		{ID: "deepseek-flash", Provider: "deepseek", Name: "DeepSeek Flash", ContextWindow: 1000000, Levels: levels},
	}
	return tui.Snapshot{Cwd: cwd, Branch: strings.TrimSpace(string(branch)), Models: models, ModelKey: models[0].Key(), DefaultModelKey: models[0].Key(), Thinking: "max",
		Sessions: []tui.SessionOption{
			{Path: "preview/session-1", Summary: "Investigate a streaming response", Modified: "2m ago"},
			{Path: "preview/session-2", Summary: "设计一个中文终端界面", Modified: "1h ago"},
			{Path: "preview/session-3", Summary: "Review a Go command", Modified: "1d ago"},
		}}
}

func (p *preview) request(request tui.Request) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		var notice string
		switch request.Kind {
		case "run":
			return p.run(ctx, request.Value)
		case "model":
			previous := p.state.ModelKey
			p.state.ModelKey = request.Value
			changed := ""
			for _, model := range p.state.Models {
				if model.Key() != request.Value {
					continue
				}
				changed = model.ID
				found := false
				for _, level := range model.Levels {
					if level == p.state.Thinking {
						found = true
					}
				}
				if !found {
					p.state.Thinking = model.Levels[len(model.Levels)-1]
				}
			}
			if p.state.ModelKey != previous {
				notice = "Model changed to " + changed
			}
		case "thinking":
			previous := p.state.Thinking
			p.state.Thinking = request.Value
			if p.state.Thinking != previous {
				notice = "Thinking changed to " + request.Value
			}
		case "rename":
			previous := p.state.SessionName
			p.state.SessionName = request.Value
			if p.state.SessionName != previous {
				notice = "Thread name changed to " + p.state.SessionName
			}
		case "resume":
			for _, session := range p.state.Sessions {
				if session.Path == request.Value {
					p.state.Messages = []tui.Message{{ID: "resumed-user", Role: "user", Text: session.Summary}, {ID: "resumed-assistant", Role: "assistant", Text: exampleMarkdown}}
				}
			}
		case "compact":
			p.send(tui.StatusMsg("Compacting context…"))
			if !wait(ctx, 600*time.Millisecond) {
				return tui.OperationDoneMsg{}
			}
			p.sequence++
			p.state.Messages = append(p.state.Messages, tui.Message{ID: fmt.Sprintf("summary-%d", p.sequence), Role: "summary", Text: "Context compacted. The conversation is ready to continue."})
		}
		return tui.OperationDoneMsg{Snapshot: &p.state, Notice: notice}
	}
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *preview) run(ctx context.Context, input string) tea.Msg {
	p.sequence++
	user := tui.Message{ID: fmt.Sprintf("user-%d", p.sequence), Role: "user", Text: input}
	p.state.Messages = append(p.state.Messages, user)
	p.send(tui.MessageMsg(user))
	message := tui.Message{ID: fmt.Sprintf("assistant-%d", p.sequence), Role: "assistant", Thinking: exampleThinking}
	start := time.Now()
	p.send(tui.MessageMsg(message))
	delay := 500 * time.Millisecond
	if p.scene == "working" {
		delay = 15 * time.Second
	}
	answer := exampleAnswer
	if strings.Contains(strings.ToLower(input), "markdown") {
		answer = exampleMarkdown
	}
	runes := []rune(answer)
	aborted := !wait(ctx, delay)
	for end := 0; end < len(runes) && !aborted; {
		end = min(len(runes), end+6)
		message.Text = string(runes[:end])
		p.send(tui.MessageMsg(message))
		aborted = !wait(ctx, 40*time.Millisecond)
	}
	if !aborted {
		message.Divider = &tui.TurnDividerView{Elapsed: time.Since(start)}
	}
	if aborted {
		// 与真实行为一致：partial 正文 + 消息级取消标记
		message.Aborted = true
	}
	p.state.Messages = append(p.state.Messages, message)
	if input == "error" {
		return tui.OperationDoneMsg{Snapshot: &p.state, Err: errors.New("simulated request failure")}
	}
	return tui.OperationDoneMsg{Snapshot: &p.state}
}

// previewModel 在收到真实终端尺寸后注入场景按键
type previewModel struct {
	*tui.Model
	scene  string
	seeded bool
}

func (p *previewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.Model.Update(msg)
	if _, ok := msg.(tea.WindowSizeMsg); !ok || p.seeded {
		return p, cmd
	}
	p.seeded = true
	input := ""
	enter := true
	switch p.scene {
	case "completion":
		input = "/"
		enter = false
	case "model":
		input = "/model "
	case "thinking":
		input = "/thinking "
	case "resume":
		input = "/resume "
	case "working":
		input = "hello"
	}
	if input != "" {
		_, typed := p.Model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(input)})
		cmd = tea.Batch(cmd, typed)
		if enter {
			_, submitted := p.Model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			cmd = tea.Batch(cmd, submitted)
		}
	}
	return p, cmd
}

func main() {
	scene := flag.String("scene", "initial", "initial, conversation, markdown, working, completion, model, thinking, resume")
	flag.Parse()
	valid := map[string]bool{"initial": true, "conversation": true, "markdown": true, "working": true, "completion": true, "model": true, "thinking": true, "resume": true}
	if !valid[*scene] {
		fmt.Fprintln(os.Stderr, "unknown scene:", *scene)
		os.Exit(2)
	}
	driver := &preview{state: initialState(), scene: *scene}
	if *scene == "conversation" || *scene == "model" || *scene == "markdown" {
		answer := exampleAnswer
		if *scene == "markdown" {
			answer = exampleMarkdown
		}
		assistant := tui.Message{ID: "answer", Role: "assistant", Thinking: exampleThinking, Text: answer, Divider: &tui.TurnDividerView{Elapsed: 12 * time.Second}}
		if *scene == "conversation" || *scene == "model" {
			assistant.ToolGroup = exampleToolGroup()
		}
		driver.state.Messages = []tui.Message{{ID: "hello", Role: "user", Text: "hello"}, assistant}
	}
	model := &previewModel{Model: tui.New(driver.state, tui.Handlers{Request: driver.request, Cancel: func() {
		if driver.cancel != nil {
			driver.cancel()
		}
	}}), scene: *scene}
	input := tui.NewInputReader(os.Stdin)
	defer input.Close()
	program := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(tui.NewCursorWriter(os.Stdout, model.CursorPosition)), tea.WithAltScreen(), tea.WithMouseAllMotion())
	driver.send = program.Send
	_, err := program.Run()
	if driver.cancel != nil {
		driver.cancel()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
