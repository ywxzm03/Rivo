package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// dialogModel 构造一个带 /ps 弹窗所需 handler 的模型
func dialogModel(t *testing.T, kill func(string) tea.Cmd) *Model {
	t.Helper()
	m := New(Snapshot{Cwd: "/repo"}, Handlers{
		Request:      func(Request) tea.Cmd { return nil },
		ShellList:    func() tea.Cmd { return nil },
		ShellStopAll: func() tea.Cmd { return nil },
		ShellKill:    kill,
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	return m
}

// openDialog 提交 /ps 并灌入一次查询结果
func openDialog(t *testing.T, shells []ShellView) *Model {
	t.Helper()
	m := dialogModel(t, func(string) tea.Cmd { return nil })
	m.editor.SetValue("/ps")
	m.submit()
	m.Update(ShellsMsg{Shells: shells})
	return m
}

func shellSample(command, status string) ShellView {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	view := ShellView{ID: "bash-1", Command: command, Status: status, StartTime: start}
	if status != "running" {
		end := start.Add(12 * time.Second)
		view.EndTime = &end
	}
	return view
}

// /ps 打开弹窗,不再往 transcript 追加消息
func TestPsOpensDialogInsteadOfAppending(t *testing.T) {
	m := dialogModel(t, func(string) tea.Cmd { return nil })
	before := len(m.state.Messages)
	m.editor.SetValue("/ps")
	m.submit()

	if m.shellDialog == nil {
		t.Fatal("no shell dialog opened by /ps")
	}
	if len(m.state.Messages) != before {
		t.Fatalf("messages = %d, want %d: /ps must not write the transcript", len(m.state.Messages), before)
	}
}

// /ps 不再回显命令行
func TestPsDoesNotEchoCommand(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	for _, message := range m.state.Messages {
		if message.Role == "command" && message.Text == "/ps" {
			t.Fatal("/ps echoed into the transcript")
		}
	}
}

// 只有一个 shell 时直接进详情
func TestSingleShellOpensDetail(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	if m.shellDialog == nil || !m.shellDialog.detail {
		t.Fatal("a single shell did not open the detail view")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Shell details") {
		t.Fatalf("view = %q, want the detail title", view)
	}
}

// 多个 shell 先进列表
func TestMultipleShellsOpenList(t *testing.T) {
	m := openDialog(t, []ShellView{
		shellSample("sleep 120", "running"),
		shellSample("sleep 240", "running"),
	})
	if m.shellDialog == nil || m.shellDialog.detail {
		t.Fatal("two shells did not open the list view")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Background shells") || !strings.Contains(view, "2 active shells\n") {
		t.Fatalf("view = %q, want the list title with the running count", view)
	}
}

// 详情必须展示四个字段
func TestDialogDetailShowsFields(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	view := ansi.Strip(m.View())
	for _, field := range []string{"Status:", "Runtime:", "Command:", "Output:"} {
		if !strings.Contains(view, field) {
			t.Fatalf("view = %q, want field %q", view, field)
		}
	}
}

// 运行中的状态值用弹窗强调色
func TestDialogRunningStatusUsesAccent(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	if !strings.Contains(m.View(), colorANSI(t, dialogAccentColor)) {
		t.Fatal("running status is not painted with the dialog accent color")
	}
}

// 已完成的运行时长的终态值用绿色
func TestDialogCompletedStatusUsesGreen(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "completed")})
	if !strings.Contains(m.View(), colorANSI(t, greenColor)) {
		t.Fatal("completed status is not painted green")
	}
}

// 输出区是圆角框,四角齐全
func TestDialogOutputBoxIsRounded(t *testing.T) {
	shell := shellSample("sleep 120", "running")
	shell.OutputTail = []string{"tick 1", "tick 2"}
	m := openDialog(t, []ShellView{shell})
	view := ansi.Strip(m.View())
	for _, corner := range []string{"╭", "╮", "╰", "╯"} {
		if !strings.Contains(view, corner) {
			t.Fatalf("view = %q, want corner %q", view, corner)
		}
	}
}

// 输出被截断时给出总字节数
func TestDialogShowsTruncationFooter(t *testing.T) {
	shell := shellSample("sleep 120", "running")
	shell.OutputTail = []string{"tick 1", "tick 2"}
	shell.Truncated = true
	shell.OutputBytes = 4096
	m := openDialog(t, []ShellView{shell})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Showing 2 lines of 4.0 KB") {
		t.Fatalf("view = %q, want the truncation footer", view)
	}
}

// 无输出时给出固定文案
func TestDialogShowsNoOutput(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "No output available") {
		t.Fatalf("view = %q, want the empty output hint", view)
	}
}

// 零个 shell 时给出空态
func TestDialogEmptyState(t *testing.T) {
	m := openDialog(t, nil)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "No background shells running.") {
		t.Fatalf("view = %q, want the empty state", view)
	}
}

// 列表 Enter 进详情,左方向键回列表
func TestDialogEnterAndBackNavigate(t *testing.T) {
	m := openDialog(t, []ShellView{
		shellSample("sleep 120", "running"),
		shellSample("sleep 240", "running"),
	})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.shellDialog.detail {
		t.Fatal("Enter did not open the detail view")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.shellDialog.detail {
		t.Fatal("Left did not go back to the list")
	}
}

// Esc 关闭弹窗并交还焦点给输入框
func TestDialogEscapeClosesAndRestoresEditor(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.shellDialog != nil {
		t.Fatal("Esc did not close the dialog")
	}
	if !m.editor.Focused() {
		t.Fatal("editor is not focused after closing the dialog")
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "Shell details") {
		t.Fatalf("view = %q, want the dialog gone", view)
	}
}

// x 只对运行中的 shell 发起终止
func TestDialogKillSendsOnlyRunningShell(t *testing.T) {
	var killed []string
	m := dialogModel(t, func(id string) tea.Cmd {
		killed = append(killed, id)
		return nil
	})
	m.editor.SetValue("/ps")
	m.submit()
	m.Update(ShellsMsg{Shells: []ShellView{shellSample("sleep 120", "completed")}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if len(killed) != 0 {
		t.Fatalf("killed = %v, want no kill for a completed shell", killed)
	}

	m.Update(ShellsMsg{Shells: []ShellView{shellSample("sleep 120", "running")}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if len(killed) != 1 || killed[0] != "bash-1" {
		t.Fatalf("killed = %v, want the running shell", killed)
	}
}

// 弹窗打开期间每秒重新查询
func TestDialogRefreshesEverySecond(t *testing.T) {
	lists := 0
	m := New(Snapshot{Cwd: "/repo"}, Handlers{
		Request:   func(Request) tea.Cmd { return nil },
		ShellList: func() tea.Cmd { lists++; return nil },
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m.editor.SetValue("/ps")
	m.submit()
	before := lists
	m.Update(subagentRefreshMsg{})
	if lists != before+1 {
		t.Fatalf("shell list calls = %d, want %d", lists, before+1)
	}
}

// 小终端下帧高度不溢出
func TestDialogHeightFitsSmallTerminal(t *testing.T) {
	shell := shellSample("sleep 120", "running")
	shell.OutputTail = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	m := dialogModel(t, func(string) tea.Cmd { return nil })
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m.editor.SetValue("/ps")
	m.submit()
	m.Update(ShellsMsg{Shells: []ShellView{shell}})
	if lines := strings.Count(m.View(), "\n") + 1; lines > 12 {
		t.Fatalf("frame lines = %d, want <= 12", lines)
	}
}

// 关闭后 transcript 内容与打开前一致
func TestDialogLeavesTranscriptUntouched(t *testing.T) {
	m := dialogModel(t, func(string) tea.Cmd { return nil })
	m.Update(MessageMsg(Message{ID: "m1", Role: "assistant", Text: "hello"}))
	before := ansi.Strip(m.View())
	m.editor.SetValue("/ps")
	m.submit()
	m.Update(ShellsMsg{Shells: []ShellView{shellSample("sleep 120", "running")}})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	after := ansi.Strip(m.View())
	if before != after {
		t.Fatalf("view changed after opening and closing the dialog:\nbefore=%q\nafter=%q", before, after)
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

// shellPanelModel 构造一个带运行中 shell 的模型,/stop 相关用例复用
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

// 单个 shell 直入详情时没有列表可回,不提示返回键
func TestDialogSingleShellHasNoBackHint(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	view := ansi.Strip(m.View())
	if strings.Contains(view, "← to go back") {
		t.Fatalf("view = %q, want no back hint without a list", view)
	}
}

// 多个 shell 时详情才提示返回键
func TestDialogMultipleShellsHasBackHint(t *testing.T) {
	m := openDialog(t, []ShellView{
		shellSample("sleep 120", "running"),
		shellSample("sleep 240", "running"),
	})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "← to go back") {
		t.Fatalf("view = %q, want the back hint", view)
	}
}

// 已完成的 shell 不提示停止键
func TestDialogCompletedShellHasNoStopHint(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "completed")})
	if view := ansi.Strip(m.View()); strings.Contains(view, "x to stop") {
		t.Fatalf("view = %q, want no stop hint for a completed shell", view)
	}
}

// 列表标题只统计仍在运行的 shell
func TestDialogListCountsOnlyRunning(t *testing.T) {
	m := openDialog(t, []ShellView{
		shellSample("sleep 120", "running"),
		shellSample("sleep 240", "killed"),
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "1 active shell\n") {
		t.Fatalf("view = %q, want the count to exclude killed shells", view)
	}
}

// 弹窗覆盖状态栏,footer 的目录与模型名都不再出现
func TestDialogCoversFooter(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	view := ansi.Strip(m.View())
	for _, text := range []string{shortenToolPath(m.state.Cwd), m.currentModel().ID} {
		if strings.Contains(view, text) {
			t.Fatalf("view = %q, want footer %q hidden behind the dialog", view, text)
		}
	}
}

// 弹窗覆盖任务列表,任务行不再渲染在弹窗下方
func TestDialogCoversTaskRows(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	m.Update(SubagentSnapshotMsg{Snapshot: SubagentSnapshot{
		Revision: m.state.Subagents.Revision + 1,
		Tasks: []SubagentTaskView{{
			ID:          "a1",
			Status:      "running",
			AgentType:   "explore",
			Description: "task-row-marker",
			StartTime:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}},
	}})
	if m.shellDialog == nil {
		t.Fatal("the snapshot update closed the dialog")
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "task-row-marker") {
		t.Fatalf("view = %q, want task rows hidden behind the dialog", view)
	}
}

// 弹窗打开时帧高与终端等高
func TestDialogFillsScreenHeight(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	if lines := strings.Count(m.View(), "\n") + 1; lines != m.height {
		t.Fatalf("frame lines = %d, want %d", lines, m.height)
	}
}

// 弹窗贴着屏幕底部长出,最后一行是按键提示
func TestDialogBottomAnchored(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "Esc") && !strings.Contains(last, "x to stop") {
		t.Fatalf("last line = %q, want the dialog hint line", last)
	}
}

// 弹窗打开时输入框不可编辑,光标归零
func TestDialogHidesCursor(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	m.View()
	if row, column := m.CursorPosition(); row != 0 || column != 0 {
		t.Fatalf("cursor = (%d, %d), want (0, 0)", row, column)
	}
}

// 弹窗打开时滚轮事件被吞掉,transcript 不滚动
func TestDialogIgnoresMouse(t *testing.T) {
	m := openDialog(t, []ShellView{shellSample("sleep 120", "running")})
	m.viewport.SetContent(strings.Repeat("transcript line\n", 200))
	m.viewport.GotoBottom()
	before := m.viewport.YOffset
	if before == 0 {
		t.Fatal("viewport is not scrollable, the mouse test proves nothing")
	}
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.viewport.YOffset != before {
		t.Fatalf("YOffset = %d, want %d: the dialog must swallow the wheel", m.viewport.YOffset, before)
	}
}
