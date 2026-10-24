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
	Retain         bool
	Revision       uint64
	LastActivity   string
	Messages       []Message
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
}

type SubagentSnapshotMsg struct {
	Snapshot       SubagentSnapshot
	Err            error
	RestoreMessage *llm.UserMessage
}

type subagentRefreshMsg struct{}

// startSubagentRefresh 只在没有活动 tick 链时建立唯一刷新链
func (m *Model) startSubagentRefresh() tea.Cmd {
	if !hasRunningSubagent(m.state.Subagents.Tasks) || m.handlers.SubagentSnapshot == nil {
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
	previousTask, hadPreviousTask := findSubagentTask(m.state.Subagents.Tasks, m.viewingTaskID)
	snapshot = shallowSubagentSnapshot(snapshot)
	m.state.Subagents = snapshot
	if m.selectedTaskID != "" && !hasSubagentTask(snapshot.Tasks, m.selectedTaskID) {
		m.selectedTaskID = ""
	}
	if m.taskSelector && len(snapshot.Tasks) == 0 {
		m.taskSelector = false
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
	if previousRows != m.subagentRows() {
		m.layout()
	}
	if messagesChanged {
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
	return m.handlers.SubagentRequest(SubagentRequest{Kind: "enter", TaskID: task.ID})
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
	m.taskSelector = false
	m.state.Subagents.ViewingTaskID = id
	m.state.Messages = cloneMessages(task.Messages)
	m.editor.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color(blueColor))
	m.editor.BlurredStyle.Prompt = m.editor.FocusedStyle.Prompt
	m.editor.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(blueColor))
	m.editor.Placeholder = "Message @" + taskLabel(task) + "…"
	m.editor.Focus()
	m.viewport.GotoBottom()
	m.layout()
	m.refreshTranscript()
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
	m.markdownPreview = make(map[string]string)
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

// taskSelectionMarker 渲染选中标记:箭头列只在选中行显示 ❯,圆点列在选中时变实心。
// 被终止的任务保持空心,只用箭头表示选中。
func taskSelectionMarker(selected, killed bool) string {
	arrow := " "
	if selected {
		arrow = "❯"
	}
	dot := "○"
	if selected && !killed {
		dot = "●"
	}
	return arrow + " " + dot
}

// taskMarkerColor 被终止的任务用红色空心圆,其余按选中态取色
func taskMarkerColor(selected, killed bool) string {
	if killed {
		return redColor
	}
	return taskSelectionColor(selected)
}

// taskListIndent 让任务列表与主 transcript 文本列对齐,不贴终端左边框
const taskListIndent = " "

func (m *Model) taskRows() []string {
	if len(m.state.Subagents.Tasks) == 0 {
		return nil
	}
	mainSelected := m.viewingTaskID == "" && m.mainSelected
	mainMarker := colored(taskMarkerColor(mainSelected, false), taskSelectionMarker(mainSelected, false))
	rows := []string{taskListIndent + mainMarker + " main"}
	for _, task := range m.state.Subagents.sortedTasks() {
		selected := !m.mainSelected && task.ID == m.selectedTaskID
		if m.selectedTaskID == "" && m.viewingTaskID != "" {
			selected = task.ID == m.viewingTaskID
		}

		killed := task.Status == "killed"
		icon := colored(taskMarkerColor(selected, killed), taskSelectionMarker(selected, killed))
		label := taskLabel(task)
		description := task.Description
		if task.Status == "failed" && task.Error != "" {
			description = "Failed: " + task.Error
		}
		if killed && description == "" {
			description = "Stopped by user"
		}
		suffix := taskElapsed(task, m.clock()) + "  " + taskTokens(task)
		if task.PendingCount > 0 {
			suffix += fmt.Sprintf("  ·%d", task.PendingCount)
		}
		// 把耗时和 token 用量固定在状态区右边缘。
		// 先截断描述部分,保证后缀始终对齐。
		suffix = ansi.Truncate(suffix, m.width, "…")
		fixed := icon + " " + label
		body := taskListIndent + fixed
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

func (m *Model) subagentRows() int {
	if len(m.state.Subagents.Tasks) == 0 {
		return 0
	}
	rows := len(m.state.Subagents.Tasks) + 1
	if m.taskSelector {
		rows++
	}
	return rows
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
