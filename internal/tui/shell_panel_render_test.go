package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// shellPanelModel 构造一个带运行中 shell 的模型
func shellPanelModel(t *testing.T) *Model {
	t.Helper()
	m := New(Snapshot{Cwd: "/repo", Subagents: SubagentSnapshot{RunningShells: 1}}, Handlers{
		Request:      func(Request) tea.Cmd { return nil },
		ShellList:    func() tea.Cmd { return nil },
		ShellStopAll: func() tea.Cmd { return nil },
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

// /ps 执行后先回显命令,与 /status 一样顶格用 accent 色
func TestPsEchoesCommandLikeStatus(t *testing.T) {
	m := shellPanelModel(t)
	m.editor.SetValue("/ps")
	m.submit()
	// 查询结果到达后才有面板
	m.Update(ShellsMsg{Shells: []ShellView{{Command: "sleep 120", Status: "running"}}})

	var commandIndex, panelIndex = -1, -1
	for i, message := range m.state.Messages {
		switch message.Role {
		case "command":
			if message.Text == "/ps" {
				commandIndex = i
			}
		case "shells":
			panelIndex = i
		}
	}
	if commandIndex < 0 {
		t.Fatal("no /ps command echo appended")
	}
	if panelIndex < 0 {
		t.Fatal("no shells panel appended")
	}
	if commandIndex > panelIndex {
		t.Fatalf("command at %d, panel at %d, want the echo first", commandIndex, panelIndex)
	}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[commandIndex]))
	if rendered != "/ps" {
		t.Fatalf("command echo = %q, want %q", rendered, "/ps")
	}
}

// 面板标题顶格、无圆点,与上方的 Worked for 行左对齐
func TestShellPanelTitleIsFlushLeft(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsMsg{Shells: []ShellView{{Command: "sleep 120", Status: "running"}}})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	if message == nil {
		t.Fatal("no shells panel message")
	}
	rendered := ansi.Strip(m.renderMessage(*message))
	lines := strings.Split(rendered, "\n")
	if len(lines) == 0 {
		t.Fatal("panel rendered no lines")
	}
	if lines[0] != "Background shells" {
		t.Fatalf("title = %q, want %q flush left without a bullet", lines[0], "Background shells")
	}
}

// 命令行用紫色 accent,输出行缩进四格并用 ↳ 前缀
func TestShellPanelCommandUsesAccentColor(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsMsg{Shells: []ShellView{{
		Command: "sleep 120", Status: "running", OutputTail: []string{"line one"},
	}}})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	if message == nil {
		t.Fatal("no shells panel message")
	}
	raw := m.renderMessage(*message)
	accentANSI := colorANSI(t, accentColor)
	if !strings.Contains(raw, accentANSI) {
		t.Fatalf("panel = %q, want the command colored with accent %s", raw, accentColor)
	}
	plain := ansi.Strip(raw)
	if !strings.Contains(plain, "  • sleep 120") {
		t.Fatalf("panel = %q, want the command line with a bullet", plain)
	}
	if !strings.Contains(plain, "↳ line one") {
		t.Fatalf("panel = %q, want the output prefixed with an arrow", plain)
	}
}

// 空态文案与 codex 一致
func TestShellPanelEmptyState(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsMsg{})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	if message == nil {
		t.Fatal("no shells panel message")
	}
	plain := ansi.Strip(m.renderMessage(*message))
	if !strings.Contains(plain, "No background shells running.") {
		t.Fatalf("panel = %q, want the empty-state line", plain)
	}
}

// 超长命令截断并带 [...] 后缀
func TestShellPanelTruncatesLongCommand(t *testing.T) {
	m := shellPanelModel(t)
	long := strings.Repeat("x", 300)
	m.Update(ShellsMsg{Shells: []ShellView{{Command: long, Status: "running"}}})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	plain := ansi.Strip(m.renderMessage(*message))
	if !strings.Contains(plain, "[...]") {
		t.Fatalf("panel = %q, want a truncation marker", plain)
	}
	for line := range strings.SplitSeq(plain, "\n") {
		if len(line) > m.width {
			t.Fatalf("line %q exceeds width %d", line, m.width)
		}
	}
}

// /stop 也要回显命令
func TestStopEchoesCommand(t *testing.T) {
	m := shellPanelModel(t)
	m.editor.SetValue("/stop")
	m.submit()

	found := false
	for _, message := range m.state.Messages {
		if message.Role == "command" && message.Text == "/stop" {
			found = true
		}
	}
	if !found {
		t.Fatal("no /stop command echo appended")
	}
}

// 面板消息不参与 markdown 折叠,必须整块显示
func TestShellsRoleIsStandalone(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsMsg{Shells: []ShellView{{Command: "sleep 1", Status: "running"}}})
	if lastMessageWithRole(m, "shells") == nil {
		t.Fatal("shells panel missing after ShellsMsg")
	}
}

// colorANSI 把主题色名渲染成实际 ANSI 转义,便于断言颜色而非色值
func colorANSI(t *testing.T, color string) string {
	t.Helper()
	rendered := colored(color, "x")
	return rendered[:strings.Index(rendered, "x")]
}

// lastMessageWithRole 返回最后一条指定角色的消息
func lastMessageWithRole(m *Model, role string) *Message {
	for i := len(m.state.Messages) - 1; i >= 0; i-- {
		if m.state.Messages[i].Role == role {
			return &m.state.Messages[i]
		}
	}
	return nil
}
