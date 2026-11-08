package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const (
	// shellDialogOutputRows 是详情输出框最多展示的内容行数
	shellDialogOutputRows = 12
	// shellDialogFixedRows 是详情弹窗不含输出内容行时的固定行数:
	// 顶线、标题、空行、三个字段、空行、Output 标题、输出框上下边框、空行、提示行
	shellDialogFixedRows = 12
	// shellDialogCompactRows 是高度不足时保留的行数:顶线、标题、三个字段、提示行
	shellDialogCompactRows = 6
	// shellDialogListRows 是列表弹窗不含候选时的固定行数
	shellDialogListRows = 6
	// shellDialogMaxItems 是列表最多展示的候选数
	shellDialogMaxItems = 10
	// shellDialogIndent 是弹窗内容的左侧缩进
	shellDialogIndent = "  "
)

// shellDialog 是 /ps 弹窗状态,覆盖输入坞与状态栏,不写进 transcript
type shellDialog struct {
	// shells 是最近一次查询结果,按启动时间升序
	shells []ShellView
	// selected 是列表视图的选中下标,越界时钳到有效范围
	selected int
	// detail 为真时展示单个 shell 的详情,为假时展示列表
	detail bool
	// err 是查询失败原因,正常时为空
	err string
}

// runningCount 统计仍在运行的 shell 数量,列表标题按它显示
func (d *shellDialog) runningCount() int {
	running := 0
	for _, shell := range d.shells {
		if shell.Status == "running" {
			running++
		}
	}
	return running
}

// current 返回当前选中项,无候选时第二返回值为假
func (d *shellDialog) current() (ShellView, bool) {
	if len(d.shells) == 0 {
		return ShellView{}, false
	}
	return d.shells[clampIndex(d.selected, len(d.shells))], true
}

// clamp 收敛选中下标与视图模式,使空列表不留在详情视图
func (d *shellDialog) clamp() {
	if len(d.shells) == 0 {
		d.selected, d.detail = 0, false
		return
	}
	d.selected = clampIndex(d.selected, len(d.shells))
}

// move 在列表中移动选中项,越界时停在首尾
func (d *shellDialog) move(delta int) {
	if len(d.shells) == 0 {
		return
	}
	d.selected = clampIndex(d.selected+delta, len(d.shells))
}

func clampIndex(index, length int) int {
	if length <= 0 {
		return 0
	}
	return min(max(0, index), length-1)
}

// height 返回弹窗占用的行数。弹窗贴屏幕底部长出,预算通常就是整屏高度,
// 返回值同时决定上方 transcript 视口的高度
func (d *shellDialog) height(budget int) int {
	budget = max(1, budget)
	if !d.detail {
		return min(budget, shellDialogListRows+min(len(d.shells), shellDialogMaxItems))
	}
	if budget < shellDialogFixedRows {
		return min(budget, shellDialogCompactRows)
	}
	rows, footer := d.detailOutputRows(budget)
	total := shellDialogFixedRows + rows
	if footer {
		total++
	}
	return min(budget, total)
}

// detailOutputRows 计算输出框内容行数与是否保留截断提示
func (d *shellDialog) detailOutputRows(budget int) (rows int, footer bool) {
	room := budget - shellDialogFixedRows
	shell, ok := d.current()
	if ok && shell.Truncated && room >= 2 {
		room, footer = room-1, true
	}
	return min(shellDialogOutputRows, max(0, room)), footer
}

// view 渲染弹窗内容
func (d *shellDialog) view(width, budget int, now time.Time) string {
	if d.detail {
		return d.detailView(width, budget, now)
	}
	return d.listView(width)
}

// listView 渲染 shell 列表:标题、运行中数量、逐行候选与提示行
func (d *shellDialog) listView(width int) string {
	limit := max(1, width)
	lines := []string{
		rule(limit, "", dialogAccentColor, false),
		shellDialogIndent + boldText(colored(dialogAccentColor, "Background shells")),
	}
	if d.err != "" {
		lines = append(lines, "", colored(redColor, ansi.Truncate(shellDialogIndent+d.err, limit, "…")), "", colored(dimColor, d.guideLine(false)))
		return strings.Join(lines, "\n")
	}
	if len(d.shells) == 0 {
		lines = append(lines,
			"",
			colored(mutedColor, shellDialogIndent+"No background shells running."),
			"",
			colored(dimColor, d.guideLine(false)),
		)
		return strings.Join(lines, "\n")
	}
	running := d.runningCount()
	lines = append(lines, shellDialogIndent+colored(mutedColor, fmt.Sprintf("%d active shell%s", running, plural(running))), "")
	for index, shell := range d.shells {
		if index >= shellDialogMaxItems {
			break
		}
		lines = append(lines, shellRow(shell, index == clampIndex(d.selected, len(d.shells)), limit))
	}
	if remaining := len(d.shells) - shellDialogMaxItems; remaining > 0 {
		lines = append(lines, colored(mutedColor, ansi.Truncate(shellDialogIndent+fmt.Sprintf("... and %d more", remaining), limit, "…")))
	}
	lines = append(lines, "", colored(dimColor, d.guideLine(false)))
	return strings.Join(lines, "\n")
}

// detailView 渲染单个 shell 的详情:三个字段、圆角输出框与提示行
func (d *shellDialog) detailView(width, budget int, now time.Time) string {
	limit := max(1, width)
	shell, ok := d.current()
	if !ok {
		return d.listView(width)
	}
	title := shellDialogIndent + boldText(colored(dialogAccentColor, "Shell details"))
	status := shellDialogIndent + boldText("Status:") + " " + statusValue(shell)
	runtime := shellDialogIndent + boldText("Runtime:") + " " + shellRuntime(shell, now)
	command := shellDialogIndent + boldText("Command:") + " " + ansi.Truncate(flattenCommand(shell.Command), max(1, limit-len(shellDialogIndent)-len("Command: ")), "…")

	if budget < shellDialogFixedRows {
		lines := []string{rule(limit, "", dialogAccentColor, false), title, status, runtime, command, colored(dimColor, d.guideLine(true))}
		return strings.Join(lines[:min(len(lines), budget)], "\n")
	}

	rows, footer := d.detailOutputRows(budget)
	lines := []string{
		rule(limit, "", dialogAccentColor, false),
		title,
		"",
		status,
		runtime,
		command,
		"",
		shellDialogIndent + boldText("Output:"),
	}
	lines = append(lines, outputBox(shell, rows, limit)...)
	if footer {
		shown := min(rows, len(shell.OutputTail))
		lines = append(lines, colored(dimColor, ansi.Truncate(shellDialogIndent+fmt.Sprintf("Showing %d lines of %s", shown, formatShellBytes(shell.OutputBytes)), limit, "…")))
	}
	lines = append(lines, "", colored(dimColor, d.guideLine(true)))
	if len(lines) > budget {
		lines = lines[:budget]
	}
	return strings.Join(lines, "\n")
}

// outputBox 渲染圆角输出框,内容行数固定为 rows
func outputBox(shell ShellView, rows, limit int) []string {
	boxWidth := max(8, limit-4)
	inner := boxWidth - 4
	if inner <= 0 {
		return nil
	}
	content := shellOutputContent(shell, rows, inner)
	border := colored(dialogAccentColor, strings.Repeat("─", boxWidth-2))
	lines := make([]string, 0, rows+2)
	lines = append(lines, shellDialogIndent+colored(dialogAccentColor, "╭")+border+colored(dialogAccentColor, "╮"))
	for _, row := range content {
		lines = append(lines, shellDialogIndent+colored(dialogAccentColor, "│ ")+padRight(row, inner)+colored(dialogAccentColor, " │"))
	}
	lines = append(lines, shellDialogIndent+colored(dialogAccentColor, "╰")+border+colored(dialogAccentColor, "╯"))
	return lines
}

// shellOutputContent 取输出尾部 rows 行,不足时补空行填满输出框
func shellOutputContent(shell ShellView, rows, inner int) []string {
	content := make([]string, 0, rows)
	if shell.OutputError != "" {
		content = append(content, ansi.Truncate(shell.OutputError, inner, "…"))
	}
	if len(content) == 0 && len(shell.OutputTail) == 0 {
		content = append(content, "No output available")
	}
	tail := shell.OutputTail
	if len(tail) > rows-len(content) {
		tail = tail[len(tail)-(rows-len(content)):]
	}
	for _, line := range tail {
		if len(content) >= rows {
			break
		}
		content = append(content, ansi.Truncate(line, inner, "…"))
	}
	for len(content) < rows {
		content = append(content, "")
	}
	return content
}

// shellRow 渲染列表中的一行:游标、命令与右对齐的状态
func shellRow(shell ShellView, selected bool, limit int) string {
	cursor := "  "
	if selected {
		cursor = "❯ "
	}
	status := statusText(shell)
	command := ansi.Truncate(flattenCommand(shell.Command), max(1, limit-6-ansi.StringWidth(status)-2), "…")
	body := cursor + command
	pad := max(1, limit-2-ansi.StringWidth(body)-ansi.StringWidth(status))
	if selected {
		body = cursor + colored(accentColor, command)
	}
	return shellDialogIndent + body + strings.Repeat(" ", pad) + colored(statusColor(shell), status)
}

// statusValue 渲染详情里的状态值,含退出码
func statusValue(shell ShellView) string {
	return colored(statusColor(shell), statusText(shell))
}

// statusText 是状态文案,终态附退出码
func statusText(shell ShellView) string {
	if shell.ExitCode == nil {
		return shell.Status
	}
	return fmt.Sprintf("%s (exit code: %d)", shell.Status, *shell.ExitCode)
}

// statusColor 按状态取色:运行中用弹窗强调色,完成用绿,失败与终止用红
func statusColor(shell ShellView) string {
	switch shell.Status {
	case "running":
		return dialogAccentColor
	case "completed":
		return greenColor
	default:
		return redColor
	}
}

// shellRuntime 渲染运行时长,未知时返回短横线
func shellRuntime(shell ShellView, now time.Time) string {
	if shell.StartTime.IsZero() {
		return "-"
	}
	end := now
	if shell.EndTime != nil {
		end = *shell.EndTime
	}
	return formatElapsed(end.Sub(shell.StartTime))
}

// flattenCommand 把多行命令压成单行,避免撑破弹窗布局
func flattenCommand(command string) string {
	return strings.NewReplacer("\r\n", "↵", "\n", "↵", "\r", "↵").Replace(command)
}

// guideLine 是弹窗底部的按键提示。
// 只有存在列表可回时才提示返回键,单个 shell 直入详情时没有列表
func (d *shellDialog) guideLine(detail bool) string {
	if detail {
		guide := "Esc/Enter to close"
		if len(d.shells) > 1 {
			guide = "← to go back · " + guide
		}
		if shell, ok := d.current(); ok && shell.Status == "running" {
			guide += " · x to stop"
		}
		return shellDialogIndent + guide
	}
	return shellDialogIndent + "↑/↓ to select · Enter to view · Esc to close · x to stop"
}

// plural 在数量大于 1 时补 s
func plural(count int) string {
	if count > 1 {
		return "s"
	}
	return ""
}

// formatShellBytes 把字节数渲染成带单位的短文案
func formatShellBytes(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// padRight 按显示宽度补空格到指定列数,超宽内容已由调用方截断
func padRight(value string, width int) string {
	if pad := width - ansi.StringWidth(value); pad > 0 {
		return value + strings.Repeat(" ", pad)
	}
	return value
}

// openShellDialog 打开 /ps 弹窗并发起首次查询
func (m *Model) openShellDialog() tea.Cmd {
	m.resumePending = false
	if m.handlers.ShellList == nil {
		m.notice("Background shells are unavailable")
		return nil
	}
	m.shellDialog = &shellDialog{}
	m.editor.Blur()
	m.layout()
	return m.handlers.ShellList()
}

// closeShellDialog 关闭弹窗并把焦点交还输入框
func (m *Model) closeShellDialog() tea.Cmd {
	m.shellDialog = nil
	m.layout()
	return m.editor.Focus()
}

// updateShellDialog 应用一次查询结果,并收敛视图模式
func (m *Model) updateShellDialog(msg ShellsMsg) {
	d := m.shellDialog
	if d == nil {
		return
	}
	d.shells, d.err = msg.Shells, ""
	if msg.Err != nil {
		d.shells, d.err = nil, msg.Err.Error()
	}
	d.clamp()
	// 只有一个 shell 时跳过列表直接进详情,与 claude code 一致
	if len(d.shells) == 1 {
		d.detail = true
	}
	m.layout()
}

// shellDialogKey 处理弹窗打开期间的按键
func (m *Model) shellDialogKey(msg tea.KeyMsg) tea.Cmd {
	d := m.shellDialog
	switch msg.String() {
	case "esc", "ctrl+c":
		return m.closeShellDialog()
	case "up", "ctrl+p":
		d.move(-1)
	case "down", "ctrl+n":
		d.move(1)
	case "pgup":
		d.move(-shellDialogMaxItems)
	case "pgdown":
		d.move(shellDialogMaxItems)
	case "enter", " ":
		// 详情里 Enter 与 Space 都是关闭,列表里 Enter 进入详情
		if d.detail {
			return m.closeShellDialog()
		}
		if len(d.shells) > 0 {
			d.detail = true
		}
	case "left":
		// 只有存在多个 shell 时才有列表可回
		if d.detail && len(d.shells) > 1 {
			d.detail = false
		}
	case "x":
		return m.killSelectedShell()
	}
	return nil
}

// killSelectedShell 终止列表中选中的运行中 shell
func (m *Model) killSelectedShell() tea.Cmd {
	shell, ok := m.shellDialog.current()
	if !ok || shell.Status != "running" || m.handlers.ShellKill == nil {
		return nil
	}
	return m.handlers.ShellKill(shell.ID)
}
