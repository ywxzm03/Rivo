package tui

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// SubagentTaskView 是单个 root-store 任务的不可变展示投影。
// TUI 永远拿不到 runtime 句柄或可变任务状态。
type SubagentTaskView struct {
	ID             string
	Type           string
	Status         string
	AgentType      string
	Description    string
	Prompt         string
	OutputFile     string
	WorktreePath   string
	Result         string
	Error          string
	StartTime      time.Time
	EndTime        *time.Time
	LifecycleToken uint64
	TotalTokens    int
	ToolUses       int
	PendingCount   int
	// PendingSteering / PendingFollowUp 是该任务两条队列的文本,供子任务视图渲染
	PendingSteering []string
	PendingFollowUp []string
	Retain          bool
	Revision        uint64
	LastActivity    string
	Messages        []Message
}

type SubagentSnapshot struct {
	Tasks         []SubagentTaskView
	ViewingTaskID string
	Revision      uint64
}

type SubagentRequest struct {
	Kind    string
	TaskID  string
	Message *llm.UserMessage
	// FollowUp 用于 dequeue:true 撤回 follow-up 队列,false 撤回 steering 队列
	FollowUp bool
}

type SubagentSnapshotMsg struct {
	Snapshot SubagentSnapshot
	Err      error
	// RestoreMessage 是请求失败时要还给编辑器的输入
	RestoreMessage *llm.UserMessage
	// RestoreMessages 是从子任务队列撤回、要按顺序放回编辑器的输入
	RestoreMessages []llm.UserMessage
}

type subagentRefreshMsg struct{}

// needsSubagentRefresh 判断是否需要继续轮询子任务:
// 有任务留在面板上、正在查看某个任务,或主 agent 正在 run。
// 终态任务也要继续轮询:驱逐由 store 侧定时器完成,不轮询就永远停在
// 最后一张快照上,任务行不会消失
func (m *Model) needsSubagentRefresh() bool {
	if len(m.state.Subagents.Tasks) > 0 || m.viewingTaskID != "" {
		return true
	}
	return m.busy && m.operationIsRun
}

// startSubagentRefresh 只在没有活动 tick 链时建立唯一刷新链
func (m *Model) startSubagentRefresh() tea.Cmd {
	// 正在查看的任务即使已终态也要续链:首次进入时 transcript 还没加载完
	if m.handlers.SubagentSnapshot == nil || !m.needsSubagentRefresh() {
		m.subagentRefreshRunning = false
		return nil
	}
	if m.subagentRefreshRunning {
		return nil
	}
	m.subagentRefreshRunning = true
	return tea.Batch(m.startSpinner(), subagentRefreshTick())
}

func subagentRefreshTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return subagentRefreshMsg{} })
}

func cloneSubagentSnapshot(snapshot SubagentSnapshot) SubagentSnapshot {
	copy := snapshot
	copy.Tasks = make([]SubagentTaskView, len(snapshot.Tasks))
	for i, task := range snapshot.Tasks {
		copy.Tasks[i] = task
		copy.Tasks[i].Messages = append([]Message(nil), task.Messages...)
		for j := range copy.Tasks[i].Messages {
			copy.Tasks[i].Messages[j].ToolGroup = cloneToolGroup(copy.Tasks[i].Messages[j].ToolGroup)
		}
	}
	return copy
}

func shallowSubagentSnapshot(snapshot SubagentSnapshot) SubagentSnapshot {
	copy := snapshot
	copy.Tasks = append([]SubagentTaskView(nil), snapshot.Tasks...)
	return copy
}

// sortedTasks 按创建顺序返回任务,状态变化不重排。
// 终止任务必须留在原位,否则用户按 x 后行会跳到列表末尾。
func (s SubagentSnapshot) sortedTasks() []SubagentTaskView {
	tasks := append([]SubagentTaskView(nil), s.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		left, right := tasks[i], tasks[j]
		if !left.StartTime.Equal(right.StartTime) {
			return left.StartTime.Before(right.StartTime)
		}
		return left.ID < right.ID
	})
	return tasks
}

func (m *Model) applySubagentSnapshot(snapshot SubagentSnapshot) {
	previousRows := m.subagentRows()
	previousPending := m.pendingRows()
	previousWaiting := m.backgroundWaitingLabel()
	previousTask, hadPreviousTask := findSubagentTask(m.state.Subagents.Tasks, m.viewingTaskID)
	snapshot = shallowSubagentSnapshot(snapshot)
	m.state.Subagents = snapshot
	waitingChanged := previousWaiting != m.backgroundWaitingLabel()
	if m.selectedTaskID != "" && !hasSubagentTask(snapshot.Tasks, m.selectedTaskID) {
		// 选中的任务被驱逐或清空后光标回到 main 行,
		// 否则列表会停在"全都没有选中"的状态
		m.selectedTaskID = ""
		m.mainSelected = true
	}
	if m.taskSelector && len(snapshot.Tasks) == 0 {
		// 列表消失时必须把焦点交回输入框,否则编辑器保持失焦、无法输入
		m.setTaskFocus(false)
	}
	messagesChanged := false
	if m.viewingTaskID != "" {
		if task, ok := findSubagentTask(snapshot.Tasks, m.viewingTaskID); ok {
			messagesChanged = !hadPreviousTask || previousTask.Revision != task.Revision
			if hadPreviousTask && previousTask.Revision == 0 && task.Revision == 0 {
				messagesChanged = childMessagesChanged(m.state.Messages, task.Messages)
			}
			if messagesChanged {
				for _, message := range task.Messages {
					delete(m.cache, message.ID)
				}
				m.state.Messages = task.Messages
				m.rebuildMessageIndexPreserveCache()
			}
		} else {
			m.exitSubagentView()
			return
		}
	}
	// 队列区与等待 footer 的行数变化同样需要重算布局
	if previousRows != m.subagentRows() || previousPending != m.pendingRows() {
		m.layout()
	}
	if waitingChanged {
		m.cache = make(map[string]cachedMessage)
	}
	if messagesChanged || waitingChanged {
		m.refreshTranscript()
	}
}

func childMessagesChanged(previous, current []Message) bool {
	if len(previous) != len(current) {
		return true
	}
	for i := range current {
		if !reflect.DeepEqual(previous[i], current[i]) {
			return true
		}
	}
	return false
}

func hasSubagentTask(tasks []SubagentTaskView, id string) bool {
	_, ok := findSubagentTask(tasks, id)
	return ok
}

func findSubagentTask(tasks []SubagentTaskView, id string) (SubagentTaskView, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return SubagentTaskView{}, false
}

// setTaskFocus 把焦点在输入框与任务列表之间切换。
// 从 child view 进入列表时保留当前查看任务作为方向键锚点
func (m *Model) setTaskFocus(focused bool) {
	if m.taskSelector == focused {
		if focused && !m.mainSelected && m.selectedTaskID == "" {
			m.mainSelected = true
			m.layout()
		}
		return
	}
	m.taskSelector = focused
	if focused {
		m.editor.Blur()
		if m.viewingTaskID != "" && hasSubagentTask(m.state.Subagents.Tasks, m.selectedTaskID) {
			m.mainSelected = false
		} else {
			m.clearSelection()
			m.mainSelected = true
			m.selectedTaskID = ""
		}
	} else {
		if m.viewingTaskID != "" && hasSubagentTask(m.state.Subagents.Tasks, m.viewingTaskID) {
			m.mainSelected = false
			m.selectedTaskID = m.viewingTaskID
		} else {
			m.selectedTaskID = ""
			m.mainSelected = true
		}
		m.editor.Focus()
	}
	m.layout()
}

func (m *Model) selectedSubagent() (SubagentTaskView, bool) {
	if m.mainSelected || m.selectedTaskID == "" {
		return SubagentTaskView{}, false
	}
	return findSubagentTask(m.state.Subagents.Tasks, m.selectedTaskID)
}

func (m *Model) selectSubagent(direction int) bool {
	tasks := m.state.Subagents.sortedTasks()
	if len(tasks) == 0 || direction == 0 {
		return false
	}
	position := -1
	if m.mainSelected {
		position = 0
	} else if m.selectedTaskID != "" {
		for i, task := range tasks {
			if task.ID == m.selectedTaskID {
				position = i + 1
				break
			}
		}
	}
	if position < 0 {
		if direction > 0 {
			position = 0
		} else {
			position = len(tasks) + 1
		}
	}
	position = (position + direction + len(tasks) + 1) % (len(tasks) + 1)
	m.mainSelected = position == 0
	if m.mainSelected {
		m.selectedTaskID = ""
	} else {
		m.selectedTaskID = tasks[position-1].ID
	}
	m.layout()
	return true
}

func (m *Model) enterSelectedSubagent() tea.Cmd {
	if m.mainSelected || m.selectedTaskID == "" {
		if m.viewingTaskID != "" {
			return m.exitSubagentViewCmd()
		}
		m.mainSelected = true
		m.taskSelector = false
		m.layout()
		return nil
	}
	task, ok := m.selectedSubagent()
	if !ok {
		return nil
	}
	m.enterSubagentView(task.ID)
	if m.handlers.SubagentRequest == nil {
		return nil
	}
	// enter 请求返回的快照已带该任务的 transcript;
	// 同时续上刷新链,让后续增量也能进来
	return tea.Batch(m.handlers.SubagentRequest(SubagentRequest{Kind: "enter", TaskID: task.ID}), m.startSubagentRefresh())
}

func (m *Model) enterSubagentView(id string) {
	task, ok := findSubagentTask(m.state.Subagents.Tasks, id)
	if !ok {
		return
	}
	if m.viewingTaskID == "" {
		m.mainMessages = cloneMessages(m.state.Messages)
	}
	m.viewingTaskID = id
	m.selectedTaskID = id
	m.mainSelected = false
	// 进入 child view 后先保持输入框焦点;第一次 Up/Down 再进入任务列表
	m.taskSelector = false
	m.state.Subagents.ViewingTaskID = id
	m.state.Messages = cloneMessages(task.Messages)
	// 刚进入时手上只有列表投影,transcript 还没加载。清掉 revision 基准,
	// 保证下一份快照一定被接受,而不是被"没有变化"判定丢掉
	m.state.Subagents.Revision = 0
	for i := range m.state.Subagents.Tasks {
		if m.state.Subagents.Tasks[i].ID == id {
			m.state.Subagents.Tasks[i].Revision = 0
			m.state.Subagents.Tasks[i].Messages = nil
			break
		}
	}
	m.editor.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color(blueColor))
	m.editor.BlurredStyle.Prompt = m.editor.FocusedStyle.Prompt
	m.editor.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(blueColor))
	m.editor.Placeholder = "Message @" + taskLabel(task) + "…"
	m.editor.Focus()
	m.viewport.GotoBottom()
	m.layout()
	m.refreshTranscript()
}

// exitSubagentViewCmd 离开子任务视图,并通知后端停止跟踪该任务
func (m *Model) exitSubagentViewCmd() tea.Cmd {
	id := m.viewingTaskID
	m.exitSubagentView()
	if m.handlers.SubagentRequest == nil || id == "" {
		return nil
	}
	return m.handlers.SubagentRequest(SubagentRequest{Kind: "exit", TaskID: id})
}

func (m *Model) stopViewedSubagent() tea.Cmd {
	if m.handlers.SubagentRequest == nil || m.viewingTaskID == "" {
		return nil
	}
	task, ok := findSubagentTask(m.state.Subagents.Tasks, m.viewingTaskID)
	if !ok || task.Status != "running" {
		return nil
	}
	return m.handlers.SubagentRequest(SubagentRequest{Kind: "stop", TaskID: task.ID})
}
func (m *Model) exitSubagentView() {
	if m.viewingTaskID == "" {
		return
	}
	m.viewingTaskID = ""
	m.selectedTaskID = ""
	m.mainSelected = true
	m.taskSelector = false
	m.state.Subagents.ViewingTaskID = ""
	m.state.Messages = cloneMessages(m.mainMessages)
	m.mainMessages = nil
	m.rebuildMessageIndex()
	m.editor.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color(inputBorder))
	m.editor.BlurredStyle.Prompt = m.editor.FocusedStyle.Prompt
	m.editor.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(textColor))
	m.editor.Placeholder = ""
	m.editor.Focus()
	m.layout()
	m.refreshTranscript()
}

func (m *Model) rebuildMessageIndex() {
	m.messageIndex = make(map[string]int, len(m.state.Messages))
	for i, message := range m.state.Messages {
		m.messageIndex[message.ID] = i
	}
	m.cache = make(map[string]cachedMessage)
}

func (m *Model) rebuildMessageIndexPreserveCache() {
	m.messageIndex = make(map[string]int, len(m.state.Messages))
	for i, message := range m.state.Messages {
		m.messageIndex[message.ID] = i
	}
}

func taskStatusIcon(status string, runningFrame string) string {
	switch status {
	case "running":
		if runningFrame == "" {
			return "●"
		}
		return runningFrame
	case "completed":
		return "✓"
	case "failed":
		return "!"
	case "killed":
		return "■"
	default:
		return "•"
	}
}

func taskSelectionColor(selected bool) string {
	if selected {
		return textColor
	}
	return dimColor
}

// taskCursor 渲染行首光标:选中行显示 ❯,其余留空。
// 光标只表示这一行被选中,与运行状态无关
func taskCursor(selected bool) string {
	if selected {
		return "❯"
	}
	return " "
}

// taskStateDot 渲染状态圆点:实心表示当前作用域。
// 列表未聚焦时实心表示正在查看的 transcript;列表聚焦时实心跟随选中行
func taskStateDot(filled, killed bool) string {
	if killed {
		if filled {
			return "●"
		}
		return "◯"
	}
	if filled {
		return "⏺"
	}
	return "◯"
}

// taskDotColor 被终止用红色,实心用正文色,其余暗色
func taskDotColor(filled, killed bool) string {
	if killed {
		return redColor
	}
	if filled {
		return textColor
	}
	return dimColor
}

// taskListIndent 让任务列表与主 transcript 文本列对齐,不贴终端左边框
const taskListIndent = " "

func (m *Model) taskRows() []string {
	if len(m.state.Subagents.Tasks) == 0 {
		return nil
	}
	// 实心圆表示当前查看的作用域,❯ 表示选中行,两者独立
	focused := m.taskSelector
	// ❯ 只看选中行,与当前查看哪个 transcript 无关
	mainViewed := m.viewingTaskID == ""
	mainSelected := focused && m.mainSelected
	// 未聚焦时实心标记正在查看的作用域;聚焦时实心跟随选中行
	mainFilled := mainViewed
	if focused {
		mainFilled = mainSelected
	}
	mainDot := colored(taskDotColor(mainFilled, false), taskStateDot(mainFilled, false))
	var rows []string
	rows = append(rows, taskListIndent+taskCursor(mainSelected)+" "+mainDot+" main")
	// 超出上限时保留最近的若干任务,避免把输入坞挤出屏幕
	budget := m.taskListLimit() - len(rows)
	sorted := m.state.Subagents.sortedTasks()
	if budget < len(sorted) {
		sorted = sorted[len(sorted)-max(0, budget):]
	}
	for _, task := range sorted {
		selected := focused && !m.mainSelected && task.ID == m.selectedTaskID
		viewed := task.ID == m.viewingTaskID
		killed := task.Status == "killed"
		filled := viewed
		if focused {
			filled = selected
		}
		dot := colored(taskDotColor(filled, killed), taskStateDot(filled, killed))
		label := taskLabel(task)
		description := task.Description
		if task.Status == "failed" && task.Error != "" {
			description = "Failed: " + task.Error
		}
		// 被终止与正常结束在行内必须可区分:描述可能很长,
		// 状态词放在后缀里更可靠
		if killed {
			description = "Stopped: " + description
			if description == "Stopped: " {
				description = "Stopped by user"
			}
		}
		suffix := taskElapsed(task, m.clock()) + "  " + taskTokens(task)
		if task.PendingCount > 0 {
			suffix += fmt.Sprintf("  ·%d", task.PendingCount)
		}
		// 把耗时和 token 用量固定在状态区右边缘。
		// 先截断描述部分,保证后缀始终对齐。
		suffix = ansi.Truncate(suffix, m.width, "…")
		body := taskListIndent + taskCursor(selected) + " " + dot + " " + label
		if description != "" {
			body += "  " + description
		}
		body = ansi.Truncate(body, max(0, m.width-ansi.StringWidth(suffix)-2), "…")
		gap := max(1, m.width-ansi.StringWidth(body)-ansi.StringWidth(suffix))
		rows = append(rows, ansi.Truncate(body+strings.Repeat(" ", gap)+suffix, m.width, "…"))
	}
	return rows
}

func taskLabel(task SubagentTaskView) string {
	label := task.AgentType
	if label == "" {
		label = task.Type
	}
	if label == "" {
		label = "agent"
	}
	return label
}

func taskElapsed(task SubagentTaskView, now time.Time) string {
	end := now
	if task.EndTime != nil {
		end = *task.EndTime
	}
	if task.StartTime.IsZero() {
		return ""
	}
	return formatElapsed(end.Sub(task.StartTime))
}

func taskTokens(task SubagentTaskView) string {
	if task.TotalTokens <= 0 {
		return "?"
	}
	if task.TotalTokens >= 1000 {
		return fmt.Sprintf("%.1fk", float64(task.TotalTokens)/1000)
	}
	return fmt.Sprintf("%d", task.TotalTokens)
}

// subagentRows 返回任务列表实际占用的行数。
// 任务多到会挤掉输入坞时按上限截断,只显示最近的一部分
func (m *Model) subagentRows() int {
	if len(m.state.Subagents.Tasks) == 0 {
		return 0
	}
	// 任务列表只包含 main 行和每个任务行
	rows := len(m.state.Subagents.Tasks) + 1
	return min(rows, m.taskListLimit())
}

// taskListLimit 是任务列表允许占用的最大行数:
// 至少给 transcript、输入框与 footer 留出空间
func (m *Model) taskListLimit() int {
	// 输入坞(上边框、编辑行、下边框)、footer 与一行 transcript
	const reserved = 4
	return max(1, m.height-reserved)
}

func (m *Model) subagentDetailHeader() string {
	task, ok := findSubagentTask(m.state.Subagents.Tasks, m.viewingTaskID)
	if !ok {
		return ""
	}
	name := taskLabel(task)
	status := strings.Title(task.Status)
	if status == "" {
		status = "Working"
	}
	line := name + " › " + task.Description + "  " + taskStatusIcon(task.Status, m.spinner.View()) + " " + status
	if task.LastActivity != "" {
		line += "  " + task.LastActivity
	}
	return ansi.Truncate(line, m.width, "…")
}

// dequeueSubagentMessages 撤回当前子任务视图的队列消息并放回编辑器
func (m *Model) dequeueSubagentMessages(followUp bool) tea.Cmd {
	if m.handlers.SubagentRequest == nil || m.viewingTaskID == "" {
		return nil
	}
	return m.handlers.SubagentRequest(SubagentRequest{
		Kind: "dequeue", TaskID: m.viewingTaskID, FollowUp: followUp,
	})
}

// restoreSubagentMessages 把撤回的子任务队列按顺序追加到编辑器
func (m *Model) restoreSubagentMessages(messages []llm.UserMessage) {
	if len(messages) == 0 {
		m.status = "No queued messages to restore"
		m.layout()
		return
	}
	var parts []string
	for _, message := range messages {
		if text := strings.TrimSpace(m.editorTextForMessage(message)); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		m.status = "No queued messages to restore"
		m.layout()
		return
	}
	current := m.editor.Value()
	if strings.TrimSpace(current) != "" {
		parts = append(parts, current)
	}
	m.editor.SetValue(strings.Join(parts, "\n\n"))
	m.editor.CursorEnd()
	m.completion, m.completionHidden = 0, true
	m.resetPastes()
	m.layout()
}

// pendingQueueTexts 返回当前视图要显示的队列文本:
// 子任务视图读该任务的两条队列,主视图读主 agent 的
func (m *Model) pendingQueueTexts() (steering, followUp []string) {
	if m.viewingTaskID == "" {
		return m.state.Steering, m.state.FollowUp
	}
	task, ok := findSubagentTask(m.state.Subagents.Tasks, m.viewingTaskID)
	if !ok {
		return nil, nil
	}
	return task.PendingSteering, task.PendingFollowUp
}
