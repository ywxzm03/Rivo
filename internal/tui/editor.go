package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const timeDoublePress = 500 * time.Millisecond

type slashCommand struct{ name, description string }

var commands = []slashCommand{
	{"tree", "Select a user message to restore conversation and/or code"},
	{"model", "<provider/model> — Select model (opens selector UI)"},
	{"effort", "<level> — Set reasoning effort"},
	{"resume", "Resume a previous session"},
	{"rename", "<new Name> — Rename the current session"},
	{"new", "Start a new session"},
	{"reload", "Reload context files and skills"},
	{"clear", "Clear the current conversation context"},
	{"delete", "Delete the current session and exit Rivo"},
	{"compact", "[instructions] — Compact the conversation"},
	{"clear-follow-up", "Clear queued follow-up messages"},
	{"status", "Show model, context usage and session info"},
	{"exit", "Exit Rivo"},
}

func (m *Model) completionItems() []slashCommand {
	value := strings.TrimLeftFunc(m.editor.Value(), unicode.IsSpace)
	if m.composer.completionHidden || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \n\t\r") {
		return nil
	}
	items := append([]slashCommand(nil), commands...)
	for _, skill := range m.state.Skills {
		items = append(items, slashCommand{name: "skill:" + skill.Name, description: skill.Description})
	}
	// 裸名匹配优先，再补充只匹配完整 skill: 名称的结果
	matched := fuzzyCommands(items, value[1:], true)
	seen := make(map[string]bool, len(matched))
	for _, item := range matched {
		seen[item.name] = true
	}
	var remaining []slashCommand
	for _, item := range items {
		if strings.HasPrefix(item.name, "skill:") && !seen[item.name] {
			remaining = append(remaining, item)
		}
	}
	return append(matched, fuzzyCommands(remaining, value[1:], false)...)
}

// completionRows 只限制显示窗口，完整候选仍可通过上下键选取
func (m *Model) completionRows(count int) int {
	room := max(0, m.height-2-2-m.editor.Height()-1)
	return min(count, min(5, room))
}

// completionIndent 是候选列表相对输入框的缩进，与斜杠命令的视觉层次对齐
const completionIndent = "  "

func (m *Model) completionView() string {
	items := m.completionItems()
	rows := m.completionRows(len(items))
	if rows == 0 {
		return ""
	}
	start := max(0, min(m.composer.completion, len(items)-1)-rows+1)
	var lines []string
	for i := start; i < min(len(items), start+rows); i++ {
		command := items[i]
		prefix, color := "  ", textColor
		if i == m.composer.completion {
			prefix, color = "❯ ", accentColor
		}
		primary := min(32, max(10, m.width/3))
		// 整块右移，候选名不贴左边界；缩进不参与列宽计算，描述列仍然对齐
		label := prefix + command.name
		line := completionIndent + colored(color, label)
		if m.width > primary+12 {
			line += strings.Repeat(" ", max(1, primary-ansi.StringWidth(label))) + colored(mutedColor, command.description)
		}
		lines = append(lines, ansi.Truncate(line, m.width, "…"))
	}
	return strings.Join(lines, "\n")
}

// handleCompletionKey 处理补全列表按键；handled=false 时继续走主按键路径
func (m *Model) handleCompletionKey(msg tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	items := m.completionItems()
	if m.completionRows(len(items)) > 0 {
		switch msg.String() {
		case "up":
			m.composer.completion = max(0, m.composer.completion-1)
			return true, nil
		case "down":
			m.composer.completion = min(len(items)-1, m.composer.completion+1)
			return true, nil
		case "esc":
			m.composer.completionHidden = true
			m.layout()
			return true, nil
		case "tab", "enter":
			leading := m.editor.Value()[:len(m.editor.Value())-len(strings.TrimLeftFunc(m.editor.Value(), unicode.IsSpace))]
			m.editor.SetValue(leading + "/" + items[min(m.composer.completion, len(items)-1)].name + " ")
			m.editor.CursorEnd()
			m.resetPastes()
			m.composer.completionHidden = true
			m.layout()
			if msg.String() == "tab" {
				return true, nil
			}
		}
	}
	return false, nil
}

// handleTaskListKey 处理任务列表聚焦时的方向键与选择；handled=false 表示继续主按键路径
func (m *Model) handleTaskListKey(msg tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	if len(m.state.Subagents.Tasks) > 0 && (m.panel.taskSelector || strings.TrimSpace(m.editor.Value()) == "") {
		switch msg.String() {
		case "up":
			wasFocused := m.panel.taskSelector
			if !wasFocused {
				m.setTaskFocus(true)
			}
			// 主视图第一次 Up 只进入列表;child view 则以当前任务为锚点上移
			if wasFocused || m.panel.viewingTaskID != "" {
				m.selectSubagent(-1)
			}
			return true, nil
		case "down":
			if !m.panel.taskSelector {
				m.setTaskFocus(true)
			}
			m.selectSubagent(1)
			return true, nil
		case "enter":
			if m.panel.viewingTaskID != "" && m.panel.selectedTaskID == m.panel.viewingTaskID {
				return true, nil
			}
			return true, m.enterSelectedSubagent()
		case "tab":
			// 列表聚焦时 Tab 仍然提交 follow-up,焦点保持不变
			if m.panel.taskSelector {
				return true, m.submitFollowUp()
			}
		default:
			// 打字即离开列表,字符继续落到输入框
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.setTaskFocus(false)
			}
		}
	}
	return false, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.closing {
		return nil
	}
	// 视图级与面板级按键先各自拦截，剩下的才进入输入框
	if handled, cmd := m.handleOverlayKey(msg); handled {
		return cmd
	}
	// 队列撤回键优先于任务选择:Option+Up/Down 不能被列表当成普通按键吃掉
	if handled, cmd := m.handleDequeueKey(msg); handled {
		return cmd
	}
	// 空输入下的 x 用于停止或驱逐选中的子任务
	if cmd, handled := m.handleTaskKillKey(msg); handled {
		return cmd
	}
	if handled, cmd := m.handleTaskListKey(msg); handled {
		return cmd
	}
	// 终端 bracketed paste 整段到达，只有粘贴走折叠路径
	if msg.Paste {
		m.handlePaste(string(msg.Runes))
		return nil
	}
	if handled, cmd := m.handleCompletionKey(msg); handled {
		return cmd
	}
	switch msg.String() {
	case "ctrl+v":
		return m.readClipboard()
	case "ctrl+c":
		if m.hasSelection() {
			return m.copySelection()
		}
		now := m.clock()
		if !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) < timeDoublePress {
			return m.quit()
		}
		m.lastCtrlC = now
		m.editor.Reset()
		m.resetPastes()
		m.composer.completionHidden = true
		m.layout()
		return nil
	case "ctrl+d":
		if m.editor.Value() == "" {
			return m.quit()
		}
	case "esc":
		if m.busy {
			m.restoreQueuedMessages()
			m.canceling = true
			m.status = "Cancelling…"
			if m.handlers.Cancel != nil {
				m.handlers.Cancel()
			}
		}
		return nil
	case "ctrl+l":
		return m.openSelector("model", "")
	case "ctrl+q":
		m.transcript.hideTools = !m.transcript.hideTools
		m.refreshTranscript()
		return nil
	case "ctrl+t":
		// 隐藏状态下忽略展开开关，恢复后回到原来的展开偏好
		if m.transcript.hideTools {
			return nil
		}
		m.transcript.showThinking = !m.transcript.showThinking
		m.refreshTranscript()
		return nil
	case "ctrl+o":
		if m.transcript.hideTools {
			return nil
		}
		m.transcript.showAllTools = !m.transcript.showAllTools
		m.refreshTranscript()
		return nil
	case "shift+tab":
		levels := m.currentModel().Levels
		if len(levels) == 0 {
			return nil
		}
		index := 0
		for i, level := range levels {
			if level == m.state.Thinking {
				index = (i + 1) % len(levels)
				break
			}
		}
		return m.dispatch(RequestThinking, levels[index])
	case "pgup", "pgdown", "ctrl+home", "ctrl+end":
		if msg.String() == "ctrl+home" {
			m.viewport.GotoTop()
			return nil
		}
		if msg.String() == "ctrl+end" {
			m.viewport.GotoBottom()
			return nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return cmd
	case "enter":
		return m.submit()
	case "tab":
		// 主 agent 运行中,或正在看一个运行中的子任务,都支持 Tab 排 follow-up
		if m.busy || m.panel.viewingTaskID != "" {
			return m.submitFollowUp()
		}
	case "backspace":
		if m.backspaceAtMarker() {
			m.layout()
			return nil
		}
	case "up":
		if m.editor.Line() == 0 && m.editor.LineInfo().RowOffset == 0 {
			// 输入框首行的 Up 把焦点上移到任务列表,不插入字符也不翻历史。
			// 队列撤回改用 Shift+Left,不再占用 Up
			if len(m.state.Subagents.Tasks) > 0 {
				m.setTaskFocus(true)
				if !m.panel.mainSelected || m.panel.selectedTaskID != "" {
					m.selectSubagent(-1)
				}
				return nil
			}
			m.browseHistory(-1)
			return nil
		}
	case "down":
		line := m.editor.LineInfo()
		if m.editor.Line() == m.editor.LineCount()-1 && line.RowOffset == line.Height-1 {
			// 输入框末行的 Down 把焦点下移到任务列表。
			// 队列撤回改用 Shift+Right,不再占用 Down
			if len(m.state.Subagents.Tasks) > 0 {
				m.setTaskFocus(true)
				m.selectSubagent(1)
				return nil
			}
			m.browseHistory(1)
			return nil
		}
	}
	before := m.editor.Value()
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	if m.editor.Value() != before {
		m.panel.taskSelector = false
		m.clearSelection()
		m.composer.completion, m.composer.completionHidden = 0, false
		m.layout()
	}
	return cmd
}

func (m *Model) quit() tea.Cmd {
	m.closing = true
	if m.handlers.Cancel != nil {
		m.handlers.Cancel()
	}
	return tea.Quit
}

func (m *Model) browseHistory(direction int) {
	if len(m.composer.history) == 0 {
		return
	}
	if m.composer.historyPos == len(m.composer.history) {
		m.composer.draft = m.editor.Value()
		if message, ok := m.buildPayload(); ok {
			copy := clonePayload(message)
			m.composer.draftMessage = &copy
		} else {
			m.composer.draftMessage = nil
		}
	}
	m.composer.historyPos = max(0, min(len(m.composer.history), m.composer.historyPos+direction))
	m.resetPastes()
	if m.composer.historyPos < len(m.composer.historyMessages) {
		m.editor.SetValue(m.editorTextForPayload(m.composer.historyMessages[m.composer.historyPos]))
	} else if m.composer.draftMessage != nil {
		m.editor.SetValue(m.editorTextForPayload(*m.composer.draftMessage))
	} else {
		m.editor.SetValue(m.composer.draft)
	}
	m.editor.CursorEnd()
	m.composer.completionHidden = true
	m.layout()
}

// recordSubmission 把一次提交记入历史并清空编辑器
func (m *Model) recordSubmission(input string, message ComposerPayload) {
	m.composer.history = append(m.composer.history, input)
	m.composer.historyMessages = append(m.composer.historyMessages, message)
	m.composer.historyPos = len(m.composer.history)
	m.composer.draft = ""
	m.composer.draftMessage = nil
	m.editor.Reset()
	m.resetPastes()
	m.composer.completionHidden = true
	m.layout()
}

func (m *Model) submit() tea.Cmd { return m.submitWithQueue(queuedSteering) }

func (m *Model) submitFollowUp() tea.Cmd { return m.submitWithQueue(queuedFollowUp) }

// submitToSubagent 把输入交给当前正在查看的子任务：
// 运行中的任务进队列，终态任务只能 continue
func (m *Model) submitToSubagent(message ComposerPayload, followUp bool) tea.Cmd {
	if m.panel.viewingTaskID == "" {
		return nil
	}
	if m.handlers.SubagentRequest == nil {
		return nil
	}
	messageCopy := clonePayload(message)
	task, ok := findSubagentTask(m.state.Subagents.Tasks, m.panel.viewingTaskID)
	if !ok {
		m.notice("Subagent task is no longer available")
		return nil
	}
	// 与主 agent 一致:Enter 进 steering,Alt+Enter 进 follow-up;
	// 终态任务只能 continue
	kind := "continue"
	if task.Status == "running" {
		kind = "queue"
		if followUp {
			kind = "followup"
		}
	}
	m.editor.Reset()
	m.resetPastes()
	m.layout()
	return m.handlers.SubagentRequest(SubagentRequest{Kind: kind, TaskID: task.ID, Message: &messageCopy})
}

func (m *Model) submitWithQueue(kind queuedKind) tea.Cmd {
	input := strings.TrimSpace(m.expandPasteMarkers(m.editor.Value()))
	if input == "" {
		return nil
	}
	message, hasMessage := m.buildPayload()
	hasImages := payloadHasImages(message)
	if !hasMessage {
		return nil
	}
	if m.panel.viewingTaskID != "" {
		return m.submitToSubagent(message, kind == queuedFollowUp)
	}
	if hasImages && !m.currentModel().SupportsImages {
		m.notice("The current model does not support image input.")
		return nil
	}
	if hasImages && strings.HasPrefix(input, "/") {
		m.notice("Commands cannot include image attachments.")
		return nil
	}
	if m.busy && input != "/exit" {
		if m.canceling {
			m.notice("Wait for the current operation to finish before sending input.")
			return nil
		}
		if m.compacting {
			if strings.HasPrefix(input, "/") && !isQueueControlCommand(input) {
				m.notice("Commands are unavailable while context compaction is running.")
				return nil
			}
			if !isQueueControlCommand(input) {
				return m.stageCompactionMessage(input, message, kind)
			}
		}
		if strings.HasPrefix(input, "/") && !isQueueControlCommand(input) {
			if input == "/tree" || strings.HasPrefix(input, "/tree ") {
				m.notice("Wait for the current operation to finish before opening the tree.")
			} else {
				m.notice("Commands are unavailable while an operation is running.")
			}
			return nil
		}
		if !isQueueControlCommand(input) {
			if !m.canQueue(kind) {
				return nil
			}
			m.recordSubmission(input, message)
			return m.dispatchQueued(kind, input, &message, hasImages)
		}
	}
	if !m.busy && m.handlers.Request == nil && m.handlers.Status == nil {
		if hasImages {
			return nil
		}
		m.composer.history = append(m.composer.history, input)
		m.composer.historyMessages = append(m.composer.historyMessages, message)
		m.composer.historyPos = len(m.composer.history)
		m.composer.draft = ""
		m.composer.draftMessage = nil
		m.editor.Reset()
		m.resetPastes()
		m.composer.completionHidden = true
		m.layout()
		return nil
	}
	m.recordSubmission(input, message)
	if !strings.HasPrefix(input, "/") {
		m.viewport.GotoBottom()
		if hasImages {
			return m.dispatchMessage(RequestRun, input, &message)
		}
		return m.dispatch(RequestRun, input)
	}
	name, arg := input, ""
	if index := strings.IndexFunc(input, unicode.IsSpace); index >= 0 {
		name, arg = input[:index], strings.TrimSpace(input[index:])
	}
	if strings.HasPrefix(name, "/skill:") {
		m.viewport.GotoBottom()
		return m.dispatch(RequestRun, input)
	}
	switch name {
	case "/exit":
		return m.quit()
	case "/model":
		if arg != "" {
			var matches []ModelOption
			for _, model := range m.state.Models {
				if strings.EqualFold(model.Key(), arg) || strings.EqualFold(model.ID, arg) {
					matches = append(matches, model)
				}
			}
			if len(matches) == 1 {
				return m.dispatch(RequestModel, matches[0].Key())
			}
		}
		return m.openSelector("model", arg)
	case "/tree":
		return m.openSelector("tree", arg)
	case "/effort":
		if arg == "" {
			return m.openSelector("thinking", "")
		}
		for _, level := range m.currentModel().Levels {
			if arg == level {
				return m.dispatch(RequestThinking, level)
			}
		}
		m.notice("Unknown effort level. Available: " + strings.Join(m.currentModel().Levels, ", "))
	case "/resume":
		return m.openSelector("resume", arg)
	case "/compact":
		return m.dispatch(RequestCompact, arg)
	case "/clear-follow-up":
		if arg != "" {
			m.notice("Usage: /clear-follow-up")
			return nil
		}
		count := m.clearQueuedFollowUp()
		m.notice(fmt.Sprintf("Cleared %d follow-up message(s)", count))
		return nil
	case "/status":
		if arg != "" {
			m.notice("Usage: /status")
			return nil
		}
		if m.handlers.Status == nil {
			m.notice("Status is unavailable")
			return nil
		}
		return m.handlers.Status()
	case "/rename":
		if arg == "" {
			m.notice("usage: /rename <new Name>")
			return nil
		}
		return m.dispatch(RequestRename, arg)
	case "/new", "/reload", "/clear", "/delete":
		if arg != "" {
			m.notice("Usage: " + name)
			return nil
		}
		return m.dispatch(RequestKind(strings.TrimPrefix(name, "/")), "")
	default:
		m.notice("Unknown command: " + name)
	}
	return nil
}

// canQueue 判断当前装配是否支持该队列种类
func (m *Model) canQueue(kind queuedKind) bool {
	if m.handlers.Request != nil {
		return true
	}
	if kind == queuedFollowUp {
		return m.handlers.EnqueueFollowUpMessage != nil
	}
	return m.handlers.EnqueueSteeringMessage != nil
}

// dispatchQueued 按种类把输入交给对应队列，带图时走结构化入口
func (m *Model) dispatchQueued(kind queuedKind, input string, message *ComposerPayload, hasImages bool) tea.Cmd {
	if kind == queuedFollowUp {
		if hasImages {
			return m.dispatchFollowUpMessage(input, message)
		}
		return m.dispatchFollowUp(input)
	}
	if hasImages {
		return m.dispatchSteeringMessage(input, message)
	}
	return m.dispatchSteering(input)
}

// handleOverlayKey 处理 Esc 关闭类按键与选择器、粘贴、补全的拦截
func (m *Model) handleOverlayKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	// child view 的 Esc 停止当前 subagent,视图保持打开;选中 main 后 Enter 才退出
	if msg.String() == "esc" && m.panel.viewingTaskID != "" {
		return true, m.stopViewedSubagent()
	}
	// 列表聚焦时 Esc 先退出列表
	if msg.String() == "esc" && m.panel.taskSelector {
		m.setTaskFocus(false)
		return true, nil
	}
	if msg.String() == "esc" && m.jumpPillVisible() && (!m.busy || len(m.state.Steering) == 0) {
		m.jumpToBottom()
		return true, nil
	}
	if m.composer.selector != nil {
		return true, m.selectorKey(msg)
	}
	return false, nil
}

// handleDequeueKey 处理撤回队列的快捷键
func (m *Model) handleDequeueKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if isSteeringDequeueKey(msg) {
		// 子任务视图撤回的是该任务自己的队列,不是主 agent 的
		if m.panel.viewingTaskID != "" {
			return true, m.dequeueSubagentMessages(false)
		}
		if m.restoreQueuedSteering() == 0 {
			m.status = "No queued messages to restore"
		}
		m.layout()
		return true, nil
	}
	if isFollowUpDequeueKey(msg) {
		if m.panel.viewingTaskID != "" {
			return true, m.dequeueSubagentMessages(true)
		}
		if m.restoreQueuedFollowUp() == 0 {
			m.status = "No queued follow-up messages to restore"
		}
		m.layout()
		return true, nil
	}
	return false, nil
}

// handleTaskKillKey 在空输入下用 x 停止或驱逐选中的子任务
func (m *Model) handleTaskKillKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if msg.String() != "x" || strings.TrimSpace(m.editor.Value()) != "" {
		return nil, false
	}
	task, ok := m.selectedSubagent()
	if !ok || m.handlers.SubagentRequest == nil {
		return nil, false
	}
	kind := "evict"
	if task.Status == "running" {
		kind = "stop"
	}
	return m.handlers.SubagentRequest(SubagentRequest{Kind: kind, TaskID: task.ID}), true
}

func isQueueControlCommand(input string) bool {
	name := input
	if index := strings.IndexFunc(input, unicode.IsSpace); index >= 0 {
		name = input[:index]
	}
	return name == "/clear-follow-up"
}

func (m *Model) stageCompactionMessage(input string, message ComposerPayload, kind queuedKind) tea.Cmd {
	m.compactionQueuedMessages = append(m.compactionQueuedMessages, compactionQueuedMessage{message: clonePayload(message), kind: kind})
	m.composer.history = append(m.composer.history, input)
	m.composer.historyMessages = append(m.composer.historyMessages, clonePayload(message))
	m.composer.historyPos = len(m.composer.history)
	m.composer.draft = ""
	m.composer.draftMessage = nil
	m.editor.Reset()
	m.resetPastes()
	m.composer.completionHidden = true
	m.status = "Compacting context…"
	m.layout()
	return nil
}
