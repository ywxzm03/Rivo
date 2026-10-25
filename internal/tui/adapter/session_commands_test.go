package adapter

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestSessionCommandsPublishNameAndReplaceRuntime(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := session.DefaultDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	sm := session.New(cwd, dir)
	if err := sm.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "seed"}}}); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(sm.Path(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "")
	r.Attach(a)
	done := r.request(tui.Request{Kind: "rename", Value: "Named Thread"})().(tui.OperationDoneMsg)
	if done.Err != nil || done.Notice != "Thread name changed to Named Thread" || done.Snapshot.SessionName != "Named Thread" {
		t.Fatalf("rename completion = %+v", done)
	}
	if len(done.Snapshot.Sessions) != 1 || done.Snapshot.Sessions[0].Summary != "Named Thread" {
		t.Fatal("session selector did not use the persisted name")
	}
	old := r.namespace
	done = r.request(tui.Request{Kind: "new"})().(tui.OperationDoneMsg)
	if done.Err != nil || !done.ResetSessionUI || done.RetainMessages || r.app == a || r.namespace == old || done.Snapshot.SessionName != "" || len(done.Snapshot.Messages) != 0 {
		t.Fatalf("new completion = %+v", done)
	}
}

func TestModelAndThinkingCommandsReportNotices(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "")
	r.Attach(a)
	m := tui.New(r.Snapshot(), tui.Handlers{Request: r.request})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// 1.命令走真实提交路径，完成消息带回提示
	for _, input := range []string{"/model deepseek/deepseek-v4-pro", "/effort low"} {
		done := submitAndSettle(t, m, input)
		if done.Err != nil {
			t.Fatalf("%s: %v", input, done.Err)
		}
		m.Update(done)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Model changed to deepseek-v4-pro") || !strings.Contains(view, "Thinking changed to low") {
		t.Fatalf("frame = %q, want both change notices", view)
	}
	// 2.值未变化时静默：重复选中当前模型/等级/名称都不产生提示
	for _, input := range []string{"/model deepseek/deepseek-v4-pro", "/effort low"} {
		done := submitAndSettle(t, m, input)
		if done.Err != nil || done.Notice != "" {
			t.Fatalf("%s repeated: %+v, want silence", input, done)
		}
		m.Update(done)
	}
	if renamed := submitAndSettle(t, m, "/rename Same Name"); renamed.Err != nil || renamed.Notice != "Thread name changed to Same Name" {
		t.Fatalf("rename = %+v", renamed)
	} else {
		m.Update(renamed)
	}
	if repeated := submitAndSettle(t, m, "/rename Same Name"); repeated.Err != nil || repeated.Notice != "" {
		t.Fatalf("rename repeated = %+v, want silence", repeated)
	}
	// 3.失败只显示错误，不输出切换提示
	if failed := r.request(tui.Request{Kind: "model", Value: "deepseek/missing"})().(tui.OperationDoneMsg); failed.Err == nil || failed.Notice != "" {
		t.Fatalf("failed model completion = %+v", failed)
	}
}

// submitAndSettle 提交输入并执行返回的命令，返回操作完成消息
func submitAndSettle(t *testing.T, m *tui.Model, input string) tui.OperationDoneMsg {
	t.Helper()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(input)})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	var done tui.OperationDoneMsg
	found := false
	var drain func(tea.Cmd)
	drain = func(cmd tea.Cmd) {
		if cmd == nil || found {
			return
		}
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, next := range msg {
				drain(next)
			}
		case tui.OperationDoneMsg:
			done, found = msg, true
		}
	}
	drain(cmd)
	if !found {
		t.Fatalf("%q produced no operation completion", input)
	}
	return done
}

func TestClearAndDeleteSessionCommands(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "")
	r.Attach(a)
	clear := r.request(tui.Request{ID: 1, Kind: "clear"})().(tui.OperationDoneMsg)
	if clear.Err != nil || clear.Snapshot == nil || len(clear.Snapshot.Messages) != 0 {
		t.Fatalf("clear completion = %+v", clear)
	}
	delete := r.request(tui.Request{ID: 2, Kind: "delete"})().(tui.OperationDoneMsg)
	if delete.Err != nil || !delete.Quit {
		t.Fatalf("delete completion = %+v", delete)
	}
}
