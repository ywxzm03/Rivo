// tui/preview 是独立视觉验收入口：使用固定数据，不装配真实 Agent、不写会话
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/tui"
)

const exampleThinking = "The user just said hello. I will answer briefly and wait for their next request."
const exampleAnswer = "Hi! I'm ready to help with your code — reading files, editing code, running commands, and debugging. What would you like to do?"
const exampleMarkdown = "## A small Go example\n\n中文、emoji 👋 和 **Markdown** 使用同一套宽度规则。\n\n```go\nfunc greet(name string) string {\n    return \"Hello, \" + name\n}\n```\n\n- Inline `code` and **bold text**\n- [Rivo](https://github.com/ywxzm03/Rivo)\n\n> A quoted paragraph with a long line that wraps inside the transcript.\n\n| State | Result |\n| --- | --- |\n| streaming | visible |\n| completed | cached |"

// exampleToolGroup 覆盖 claude 行式的几类结果：只读分组、命令输出、write 预览、edit diff
func exampleToolGroup() *tui.ToolGroupView {
	patch := strings.Join([]string{
		"--- demo/greet.ts",
		"+++ demo/greet.ts",
		"@@ -1,3 +1,3 @@",
		" export function greet(name: string): string {",
		"-  return `Hello, ${name}!`;",
		"+  return `Welcome, ${name}!`;",
		" }",
	}, "\n")
	return &tui.ToolGroupView{
		ID: "preview-tools", Sealed: true,
		Calls: []tui.ToolCallView{
			{ID: "grep", Name: "grep", Round: 1, ArgsComplete: true, Arguments: `{"pattern":"greet|language","path":"demo"}`, Result: "demo/greet.ts:1: export function greet(name: string): string {", Status: tui.ToolSuccess, Elapsed: 33 * time.Millisecond},
			{ID: "read", Name: "read", Round: 1, ArgsComplete: true, Arguments: `{"path":"demo/greet.ts"}`, Result: "export function greet(name: string): string {\n  return `Hello, ${name}!`;\n}", Status: tui.ToolSuccess, Elapsed: 10 * time.Millisecond},
			{ID: "read2", Name: "read", Round: 1, ArgsComplete: true, Arguments: `{"path":"demo/config.json"}`, Result: "{\n  \"lang\": \"zh\"\n}", Status: tui.ToolSuccess, Elapsed: 8 * time.Millisecond},
			{ID: "bash", Name: "bash", Round: 2, ArgsComplete: true, Arguments: `{"command":"node --test demo/greet.test.mjs"}`, Result: "✔ greeting helper exports a typed function (0.527416ms)\nℹ tests 1\nℹ suites 0\nℹ pass 1", Status: tui.ToolSuccess, Elapsed: 145 * time.Millisecond},
			{ID: "write", Name: "write", Round: 3, ArgsComplete: true, Arguments: `{"path":"demo/notes.md","content":"# Greeting\n\nUse a friendly welcome."}`, Result: "Successfully wrote to demo/notes.md", Status: tui.ToolSuccess, Elapsed: 2 * time.Millisecond},
			{ID: "edit", Name: "edit", Round: 4, ArgsComplete: true, Arguments: `{"path":"demo/greet.ts"}`, Result: "Successfully replaced 1 block(s) in demo/greet.ts.", Details: `{"Patch":` + strconv.Quote(patch) + `}`, Status: tui.ToolSuccess, Elapsed: 3 * time.Millisecond},
		},
		Thinking: []tui.ThinkingBlockView{
			{Index: 0, Round: 1, Text: "The user said hello. First look around the demo directory.", Tokens: 120},
			{Index: 0, Round: 2, Text: "Now run the test to confirm the helper still works.", Tokens: 90},
			{Index: 0, Round: 3, Text: "Write the notes file, then update the greeting.", Tokens: 50},
		},
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
		switch request.Kind {
		case "run":
			return p.run(ctx, request.Value)
		case "model":
			p.state.ModelKey = request.Value
			for _, model := range p.state.Models {
				if model.Key() != request.Value {
					continue
				}
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
		case "thinking":
			p.state.Thinking = request.Value
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
		return tui.OperationDoneMsg{Snapshot: &p.state}
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
		message.Divider = &tui.TurnDividerView{Elapsed: time.Since(start), FinishedAt: time.Now()}
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

// exampleDiffPatch 是一段带长行与空内容删除行的统一 patch，用来验收 diff 版式
const exampleDiffPatch = `@@ -360,9 +360,9 @@
 // Insert 插入键，已存在时返回 false
 func (t *Tree[K]) Insert(key K) bool {
-
-var path []*node[K]
+var parent *node[K]
 cur := t.root
 for cur != nil {
-
-path = append(path, cur)
+parent = cur
 switch {`

func exampleDiffGroup() *tui.ToolGroupView {
	patch, _ := json.Marshal(map[string]any{"Patch": exampleDiffPatch})
	return &tui.ToolGroupView{
		ID: "preview-diff", Sealed: true,
		Calls: []tui.ToolCallView{{
			ID: "edit", Name: "edit", Round: 1, ArgsComplete: true,
			Arguments: `{"path":"~/resources/handoff/rivo-handoff-2026-09-23.md"}`,
			Result:    "Successfully replaced 1 block(s) in rbtree.go.",
			Details:   string(patch),
			Status:    tui.ToolSuccess, Elapsed: 3 * time.Millisecond,
		}},
	}
}

func main() {
	scene := flag.String("scene", "initial", "initial, conversation, markdown, working, completion, model, thinking, resume, diff")
	flag.Parse()
	valid := map[string]bool{"initial": true, "conversation": true, "markdown": true, "working": true, "completion": true, "model": true, "thinking": true, "resume": true, "diff": true}
	if !valid[*scene] {
		fmt.Fprintln(os.Stderr, "unknown scene:", *scene)
		os.Exit(2)
	}
	driver := &preview{state: initialState(), scene: *scene}
	if *scene == "diff" {
		driver.state.Messages = []tui.Message{
			{ID: "ask", Role: "user", Text: "把这次改动展示一下"},
			{ID: "answer", Role: "assistant", ToolGroup: exampleDiffGroup()},
		}
	}
	if *scene == "conversation" || *scene == "model" || *scene == "markdown" {
		answer := exampleAnswer
		if *scene == "markdown" {
			answer = exampleMarkdown
		}
		assistant := tui.Message{ID: "answer", Role: "assistant", Thinking: exampleThinking, Text: answer, Divider: &tui.TurnDividerView{Elapsed: 12 * time.Second, FinishedAt: time.Now()}}
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
