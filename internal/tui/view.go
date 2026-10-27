package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const jumpToBottomLabel = " Jump to bottom ↓ "

// branchIcon 是 git 分支图标（Nerd Font octicon git-branch），放在分支名左侧
const branchIcon = "\uf418"

type jumpPillBounds struct {
	left, right, y int
}

// layout 先为 footer 和输入坞分配高度，再把剩余空间交给消息视口
func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	follow := m.viewport.AtBottom()
	previousWidth, previousHeight := m.editor.Width(), m.editor.Height()
	targetLine := m.editor.Line()
	lineInfo := m.editor.LineInfo()
	targetColumn := lineInfo.StartColumn + lineInfo.ColumnOffset
	m.editor.SetWidth(max(1, m.width))
	m.editor.SetCursor(targetColumn)
	rows := 0
	probe := textarea.New()
	probe.Prompt = "❯ "
	probe.SetPromptFunc(2, func(line int) string {
		if line == 0 {
			return "❯ "
		}
		return "  "
	})
	probe.ShowLineNumbers = false
	probe.SetWidth(max(1, m.width))
	for line := range strings.SplitSeq(m.editor.Value(), "\n") {
		// 使用编辑器自身的软换行规则，包含行尾光标与按单词换行占用的空间
		probe.SetValue(line)
		rows += probe.LineInfo().Height
	}
	limit := min(max(5, m.height*3/10), max(1, m.height-4))
	m.editor.SetHeight(max(1, min(rows, limit)))
	if previousWidth != m.editor.Width() || previousHeight != m.editor.Height() {
		refreshEditorViewport(&m.editor, targetLine, targetColumn)
	}
	dock := m.pendingRows() + m.editor.Height() + 2
	if m.composer.selector != nil {
		m.composer.selector.input.Width = max(1, m.width-3)
		dock = m.composer.selector.height(max(1, m.height-2))
	} else {
		// 视口与输入框上边框之间保留一行空白，避免长消息贴边
		dock += m.completionRows(len(m.completionItems())) + 1
	}
	m.viewport.Width = m.width
	// 任务面板占用底部空间,viewport 必须让出来,否则总行数溢出会挤掉输入框。
	// 高度紧张时仍保留 1 行:viewport 被压到 0 会让帧比终端少一行,
	// 补白就落在任务列表下方,看起来像是底部多了几行空行
	m.viewport.Height = max(1, m.height-1-dock-m.subagentRows())
	if follow {
		m.viewport.GotoBottom()
	}
}

func refreshEditorViewport(editor *textarea.Model, targetLine, targetColumn int) {
	// textarea 在 SetHeight/SetWidth 后会保留私有视口偏移，先回到输入起点，
	// 再走到原来的逻辑行，使光标停在用户离开的位置
	*editor, _ = editor.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	for range 100000 {
		line := editor.Line()
		if line >= targetLine {
			break
		}
		var next tea.Cmd
		*editor, next = editor.Update(tea.KeyMsg{Type: tea.KeyDown})
		_ = next
	}
	editor.SetCursor(targetColumn)
	// 空 rune 消息只触发 textarea 的 repositionView，不改变已恢复的值与光标列
	*editor, _ = editor.Update(tea.KeyMsg{Type: tea.KeyRunes})
}

// View 只拼接显示缓存，不调用后台运行器或 Markdown 解析器
// View 渲染一帧。panic 时先转储堆栈再原样抛出
func (m *Model) View() (view string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			dumpPanic("view", recovered)
			panic(recovered)
		}
	}()
	return m.view()
}

func (m *Model) view() string {
	if m.width == 0 {
		return ""
	}
	lines := m.frameLines()
	if m.hasSelection() {
		paintSelection(lines, m.sel.anchor, m.sel.focus)
	}
	if len(lines) < m.height {
		lines = append(lines, make([]string, m.height-len(lines))...)
	}
	if len(lines) > m.height {
		// 输入坞高度已预先测量，只有异常子组件才会溢出。
		// 从底部整体保留 m.height 行:输入框、footer、任务行都在底部,
		// 被裁掉的只会是 transcript 顶部,用户仍有完整输入入口
		lines = lines[len(lines)-m.height:]
	}
	m.paintJumpPill(lines)
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	m.updateCursorPosition()
	return strings.Join(lines, "\n")
}

// jumpPillBounds 返回主 transcript 离底时覆盖的 pill 区域
func (m *Model) jumpPillBounds() (jumpPillBounds, bool) {
	if m.closing || m.composer.selector != nil || m.viewport.Height <= 0 || m.viewport.AtBottom() {
		return jumpPillBounds{}, false
	}
	if rows := m.completionRows(len(m.completionItems())); rows > 0 {
		return jumpPillBounds{}, false
	}
	width := ansi.StringWidth(jumpToBottomLabel)
	if width > m.width {
		return jumpPillBounds{}, false
	}
	left := (m.width - width) / 2
	return jumpPillBounds{left: left, right: left + width, y: m.viewport.Height - 1}, true
}

func (m *Model) jumpPillVisible() bool {
	_, ok := m.jumpPillBounds()
	return ok
}

func (m *Model) jumpPillHovered() bool {
	bounds, ok := m.jumpPillBounds()
	return ok && m.sel.mouseSet && m.sel.mouse.y == bounds.y && m.sel.mouse.x >= bounds.left && m.sel.mouse.x < bounds.right
}

// paintJumpPill 在 transcript 最后一行覆盖到底提示
func (m *Model) paintJumpPill(lines []string) {
	bounds, ok := m.jumpPillBounds()
	if !ok || bounds.y < 0 || bounds.y >= len(lines) {
		return
	}
	line := lines[bounds.y]
	lineWidth := ansi.StringWidth(line)
	if lineWidth < m.width {
		line += strings.Repeat(" ", m.width-lineWidth)
	}
	background := userBg
	if m.jumpPillHovered() {
		background = userBgHover
	}
	pill := lipgloss.NewStyle().
		Background(lipgloss.Color(background)).
		Foreground(lipgloss.Color(textColor)).
		Faint(true).
		Render(jumpToBottomLabel)
	lines[bounds.y] = ansi.Cut(line, 0, bounds.left) + pill + ansi.Cut(line, bounds.right, m.width)
}

// jumpToBottom 恢复实时视图并清除可能残留的文本选择
func (m *Model) jumpToBottom() {
	m.viewport.GotoBottom()
	m.clearSelection()
}

func (m *Model) frameLines() []string {
	var lines []string
	if m.viewport.Height > 0 {
		// viewport 内容不足时不补行,会让整帧比终端矮,
		// 补白就落到任务列表下方。这里按声明高度补齐
		body := splitFrameLines(m.viewport.View())
		for len(body) < m.viewport.Height {
			body = append(body, "")
		}
		lines = append(lines, body...)
	}
	if m.composer.selector != nil {
		lines = append(lines, splitFrameLines(m.composer.selector.view(m.width, max(1, m.height-2)))...)
	} else {
		// 子任务视图显示该任务自己的队列;主视图显示主 agent 的
		lines = append(lines, m.pendingLines()...)
		// 消息区与输入框之间留一行空白，长消息不会贴上边框。
		// 视口已被压到 0 行时省略它,否则总行数会溢出并挤掉输入框
		if m.viewport.Height > 0 {
			lines = append(lines, "")
		}
		// 候选列表排在输入框上方，向上展开，输入框位置保持不变
		if completions := m.completionView(); completions != "" {
			lines = append(lines, splitFrameLines(completions)...)
		}
		label := ""
		borderColor := inputBorder
		if m.busy {
			// 运行中的旋转器与状态文字用主题紫，与选中态一致
			label = m.spinner.View() + " " + m.status
			borderColor = accentColor
		} else if m.status != "" {
			label = m.status
		}
		// 子任务视图的输入框边框用蓝色,并在标签位置显示任务详情
		if m.panel.viewingTaskID != "" {
			label = m.subagentDetailHeader()
			borderColor = blueColor
		}
		border := rule(m.width, label, borderColor, false)
		if m.sel.notice {
			// 复制提示贴在输入框右上边框，不占用新行或覆盖运行状态
			const notice = " copied! ──"
			if m.width >= ansi.StringWidth(label)+ansi.StringWidth(notice)+5 {
				border = rule(m.width-ansi.StringWidth(notice), label, borderColor, false) + colored(mutedColor, notice)
			}
		}
		lines = append(lines, border)
		lines = append(lines, splitFrameLines(m.editor.View())...)
		// 上下两条边框同色，运行中一起变紫
		lines = append(lines, rule(m.width, "", borderColor, false))
	}
	lines = append(lines, splitFrameLines(m.footer())...)
	lines = append(lines, m.taskRows()...)
	return lines
}

// pendingRows 返回队列区域占用的行数
// pendingRows 与 pendingLines 共用同一套布局计算,避免两处公式漂移
// 导致多出或缺少一行
func (m *Model) pendingRows() int {
	if m.composer.selector != nil {
		return 0
	}
	return len(m.pendingLines())
}

// pendingLines 显示两类待消费消息和各自的回填提示
func (m *Model) pendingLines() []string {
	steering, followUp := m.pendingQueueTexts()
	if len(steering) == 0 && len(followUp) == 0 {
		return nil
	}
	lines := []string{""}
	for _, message := range steering {
		if index := strings.IndexByte(message, '\n'); index >= 0 {
			message = message[:index]
		}
		lines = append(lines, ansi.Truncate(colored(dimColor, "Steering: "+message), m.width, "…"))
	}
	for _, message := range followUp {
		if index := strings.IndexByte(message, '\n'); index >= 0 {
			message = message[:index]
		}
		lines = append(lines, ansi.Truncate(colored(dimColor, "Follow-up: "+message), m.width, "…"))
	}
	if len(steering) > 0 {
		lines = append(lines, ansi.Truncate(colored(dimColor, "↳ "+steeringDequeueKeyLabel()+" to edit steering"), m.width, "…"))
	}
	if len(followUp) > 0 {
		lines = append(lines, ansi.Truncate(colored(dimColor, "↳ "+followUpDequeueKeyLabel()+" to edit follow-up"), m.width, "…"))
	}
	// 队列区可用的行数:除输入坞、候选列表与任务列表外都归队列
	available := max(0, m.height-1-(m.editor.Height()+2)-m.completionRows(len(m.completionItems()))-m.subagentRows())
	// 优先保留消息与提示行,空行只在有富余时出现。
	// 高度不足时整体不显示,避免只剩一个孤立空行
	body := lines[1:]
	if available <= 0 || len(body) == 0 {
		return nil
	}
	if available >= len(body)+1 {
		return append([]string{""}, body...)
	}
	if available == 1 {
		return body[:1]
	}
	return append([]string{""}, body[:available-1]...)
}

// 队列撤回用横向键,避开与任务列表的 Up/Down 选择冲突
func steeringDequeueKeyLabel() string { return "shift+left" }

func followUpDequeueKeyLabel() string { return "shift+right" }

func splitFrameLines(view string) []string {
	view = strings.TrimSuffix(view, "\n")
	if view == "" {
		return nil
	}
	return strings.Split(view, "\n")
}

func rule(width int, label, color string, centered bool) string {
	if label == "" || width < 5 {
		return colored(color, strings.Repeat("─", width))
	}
	label = ansi.Truncate(label, max(1, width-4), "")
	remaining := max(0, width-ansi.StringWidth(label)-2)
	left := min(2, remaining)
	if centered {
		left = remaining / 2
	}
	return colored(color, strings.Repeat("─", left)+" "+label+" "+strings.Repeat("─", remaining-left))
}

func formatElapsed(d time.Duration) string {
	total := max(1, int(d.Round(time.Second).Seconds()))
	hours, total := total/3600, total%3600
	minutes, seconds := total/60, total%60
	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
	if minutes > 0 {
		if seconds > 0 {
			return fmt.Sprintf("%dm %ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", seconds)
}

// footer 输出单行状态栏：左侧工作目录与分支，右侧模型与思考等级，两端对齐
func (m *Model) footer() string {
	cwd := shortenToolPath(m.state.Cwd)
	if m.state.Branch != "" {
		// 分支前用 git 图标，与目录之间用小点分隔
		cwd += " • " + branchIcon + " " + m.state.Branch
	}
	left := colored(dimColor, ansi.Truncate(cwd, m.width, "…"))
	model := m.currentModel()
	right := model.ID
	if len(model.Levels) > 1 {
		level := m.state.Thinking
		if level == "off" {
			level = "effort off"
		}
		right += " • " + level
	}
	available := m.width - ansi.StringWidth(ansi.Strip(left))
	right = colored(dimColor, ansi.Truncate(right, max(0, available), "…"))
	gap := strings.Repeat(" ", max(0, m.width-ansi.StringWidth(ansi.Strip(left))-ansi.StringWidth(ansi.Strip(right))))
	return left + gap + right
}
