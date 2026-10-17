// Package tui 实现 Pi 风格的全屏交互界面；核心运行通过回调与不可变消息接入
package tui

import (
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// ModelOption 是可供选择的模型及核心已解析的思考等级，不含凭据
type ModelOption struct {
	ID, Provider, Name string
	ContextWindow      int
	Levels             []string
	SupportsImages     bool
}

func (m ModelOption) Key() string { return m.Provider + "/" + m.ID }

type SessionOption struct {
	Path, Summary, Modified string
}

// TreeNode 是 TUI 的 user-only 树投影；连接线由运行适配器预先计算。
type TreeNode struct {
	ID, Text, Prefix string
	ParentID         string
	Depth            int
	Current          bool
}

// SkillOption 是命令补全使用的技能元数据，不向 UI 暴露文件读取职责
type SkillOption struct {
	Name, Description string
}

// SkillInvocationView 保存已提交正文的显示投影，不重新读取磁盘
type SkillInvocationView struct {
	Name, Location, Content, Arguments string
}

// ToolStatus 表示工具调用在 compact transcript 中的状态
type ToolStatus string

const (
	ToolPending     ToolStatus = "pending"
	ToolSuccess     ToolStatus = "success"
	ToolError       ToolStatus = "error"
	ToolInterrupted ToolStatus = "interrupted"
)

// ThinkingBlockView 保存一个按 content index 分隔的 reasoning block
type ThinkingBlockView struct {
	Index       int
	Text        string
	Active      bool
	Tokens      int
	TokensExact bool
}

// ToolCallView 保存完整工具状态；compact/full 只改变展示方式
type ToolCallView struct {
	ID, Name  string
	Arguments string
	Result    string
	Status    ToolStatus
	Elapsed   time.Duration
	StartedAt time.Time
	EndedAt   time.Time
}

// ToolGroupView 将连续 thinking 和工具调用作为一个可折叠工作块
type ToolGroupView struct {
	ID       string
	Calls    []ToolCallView
	Thinking []ThinkingBlockView
	Active   bool
	Sealed   bool
}

// TurnDividerView 标记一个 turn 的最终工作耗时
type TurnDividerView struct {
	Elapsed time.Duration
}

// Message 是显示投影；ID 在流式更新中保持稳定，压缩提示不冒充用户消息
type Message struct {
	Revision                 uint64
	ID, Role, Text, Thinking string
	Elapsed                  time.Duration
	Aborted                  bool
	ToolGroup                *ToolGroupView
	Divider                  *TurnDividerView
	Skill                    *SkillInvocationView
	Images                   []ImageView
}

// ImageView 是消息投影中的图片元数据，不包含图片正文
type ImageView struct {
	MIMEType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
}

// Snapshot 由运行侧在安全边界提供；UI 不回读活动中的 Agent/SessionManager
type Snapshot struct {
	Cwd, Branch, ModelKey, DefaultModelKey, Thinking string
	SessionName                                      string
	// CurrentModel 保留运行中的模型元数据，供选择器定位当前项
	CurrentModel     ModelOption
	Models           []ModelOption
	Sessions         []SessionOption
	Tree             []TreeNode
	Messages         []Message
	Skills           []SkillOption
	Steering         []string
	SteeringInputs   []llm.UserMessage
	SteeringRevision uint64
}

// ClipboardData 是系统剪贴板的一次读取结果
type ClipboardData struct {
	Text   string
	Images []llm.ImageContent
}

// Request 表示用户发起的一次操作；Kind 为 run/model/thinking/resume/tree/compact/rename/new/reload/clear/delete
type Request struct {
	Kind, Value string
	AfterID     string
	ID          uint64
	Message     *llm.UserMessage
	RestoreMode filehistory.RestoreMode
}

// Handlers 由运行适配器提供，命令在 tea.Cmd 中执行，取消只发信号而不阻塞 Update
type Handlers struct {
	Request func(Request) tea.Cmd
	Cancel  func()
	// EnqueueSteering 同步接受一条 steering，避免撤回与异步入队竞态
	EnqueueSteering        func(string) error
	EnqueueSteeringMessage func(llm.UserMessage) error
	// RestoreSteering 原子取出后台尚未消费的 steering，供撤回到编辑器
	RestoreSteering         func() []string
	RestoreSteeringMessages func() []llm.UserMessage
	ReadClipboard           func() (string, error)
	ReadClipboardData       func() (ClipboardData, error)
	WriteClipboard          func(string) error
}

// MessageMsg 更新具有同一 ID 的消息；OperationDoneMsg 才结束整个用户操作
type MessageMsg Message
type MessageCommittedMsg struct {
	PreviousID string
	Message    Message
}

type StatusMsg string
type SteeringResultMsg struct {
	ID       uint64
	Value    string
	Message  *llm.UserMessage
	Revision uint64
	Err      error
}
type SteeringStateMsg struct {
	Messages []string
	Inputs   []llm.UserMessage
	Revision uint64
}
type MessageTail struct {
	AfterID  string
	Messages []Message
}

type OperationDoneMsg struct {
	Tail            *MessageTail
	ID              uint64
	RetainMessages  bool
	Changes         []Message
	Snapshot        *Snapshot
	Err             error
	Notice          string
	EditorValue     string
	EditorMessage   *llm.UserMessage
	RestoreEditor   bool
	ResetSessionUI  bool
	ResetTranscript bool
	Quit            bool
}
type flushMsg struct{}

type clipboardReadMsg struct {
	data ClipboardData
	err  error
}

type clipboardWriteMsg struct{ err error }

type clearCopyNoticeMsg uint64

type screenPoint struct{ x, y int }

type cachedMessage struct {
	revision uint64
	width    int
	hidden   bool
	allTools bool
	view     string
}

// Model 独占全部显示状态，生命周期与消息定稿分离
type Model struct {
	messageIndex     map[string]int
	operationID      uint64
	state            Snapshot
	handlers         Handlers
	width, height    int
	editor           textarea.Model
	viewport         viewport.Model
	spinner          spinner.Model
	selector         *selector
	completion       int
	completionHidden bool
	history          []string
	historyMessages  []llm.UserMessage
	historyPos       int
	draft            string
	draftMessage     *llm.UserMessage
	busy, closing    bool
	steeringRestored bool
	canceling        bool
	compacting       bool
	status           string
	hideThinking     bool
	showAllTools     bool
	lastCtrlC        time.Time
	flushPending     bool
	cache            map[string]cachedMessage
	steeringRevision uint64
	markdownPreview  map[string]string
	markdown         markdownRenderer
	clock            func() time.Time
	selectionAnchor  screenPoint
	selectionFocus   screenPoint
	selecting        bool
	lastClick        screenPoint
	lastClickAt      time.Time
	mousePosition    screenPoint
	mousePositionSet bool
	mouseFragment    string
	copyNoticeSeq    uint64
	copyNotice       bool
	pastes           map[int]string
	pasteCounter     int
	images           map[int]llm.ImageContent
	imageCounter     int
	cursorRow        atomic.Int64
	cursorColumn     atomic.Int64
}

// New 创建界面；预览与正式运行共享同一套组件和消息处理
func New(state Snapshot, handlers Handlers) *Model {
	ed := textarea.New()
	ed.Prompt, ed.Placeholder = "❯ ", ""
	ed.SetPromptFunc(2, func(line int) string {
		if line == 0 {
			return "❯ "
		}
		return "  "
	})
	ed.ShowLineNumbers = false
	ed.EndOfBufferCharacter = ' '
	ed.CharLimit, ed.MaxWidth = 0, 0
	ed.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "shift+enter"))
	ed.FocusedStyle = textarea.Style{Prompt: lipgloss.NewStyle().Foreground(lipgloss.Color(inputBorder)), Text: lipgloss.NewStyle().Foreground(lipgloss.Color(textColor))}
	ed.BlurredStyle = ed.FocusedStyle
	ed.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(textColor))
	ed.Focus()
	m := &Model{handlers: handlers, editor: ed, viewport: viewport.New(0, 0),
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)), cache: make(map[string]cachedMessage), markdownPreview: make(map[string]string), images: make(map[int]llm.ImageContent), clock: time.Now}
	m.applySnapshot(state)
	return m
}

func (m *Model) Init() tea.Cmd { return textarea.Blink }

// Update 处理事件，不读取或修改后台运行器的状态
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.layout()
		m.refreshTranscript()
	case tea.KeyMsg:
		if filtered, handled := m.filterMouseKey(msg); handled {
			if filtered == "" {
				return m, nil
			}
			msg.Runes = []rune(filtered)
		}
		if wheel, ok := rawMouseWheel(msg); ok {
			m.scrollWheel(wheel)
			return m, nil
		}
		if isTerminalControlKey(msg) {
			return m, nil
		}
		return m, m.handleKey(msg)
	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	case MessageMsg:
		m.upsertMessage(Message(msg))
		if !m.flushPending {
			m.flushPending = true
			return m, tea.Tick(time.Second/60, func(time.Time) tea.Msg { return flushMsg{} })
		}
	case MessageCommittedMsg:
		if index, ok := m.messageIndex[msg.PreviousID]; ok {
			delete(m.messageIndex, msg.PreviousID)
			m.messageIndex[msg.Message.ID] = index
			m.state.Messages[index] = msg.Message
			delete(m.cache, msg.PreviousID)
		} else {
			m.upsertMessage(msg.Message)
		}
		if !m.flushPending {
			m.flushPending = true
			return m, tea.Tick(time.Second/60, func(time.Time) tea.Msg { return flushMsg{} })
		}
	case flushMsg:
		m.flushPending = false
		m.refreshTranscript()
	case StatusMsg:
		value := string(msg)
		m.status = value
		switch {
		case strings.HasPrefix(value, "Compacting context"):
			m.compacting = true
		}
	case SteeringResultMsg:
		if msg.ID != 0 && msg.ID != m.operationID {
			return m, nil
		}
		if msg.Err != nil {
			if msg.Message != nil && strings.TrimSpace(m.editor.Value()) == "" {
				m.restoreEditorMessage(msg.Message, msg.Value)
			} else if strings.TrimSpace(m.editor.Value()) == "" {
				m.editor.SetValue(msg.Value)
				m.editor.CursorEnd()
			}
			m.completion, m.completionHidden = 0, true
			m.notice("Error: " + msg.Err.Error())
		} else if m.busy {
			m.steeringRestored = false
			if msg.Revision == 0 || msg.Revision > m.steeringRevision {
				if msg.Revision > m.steeringRevision {
					m.steeringRevision = msg.Revision
				}
				message := msg.Message
				if message == nil {
					value := textUserMessage(msg.Value)
					message = &value
				}
				m.appendSteeringState(msg.Value, message, true)
			}
			if !m.canceling && !m.compacting {
				m.status = "Working"
			}
		}
		m.layout()
	case SteeringStateMsg:
		if msg.Revision == 0 && m.steeringRevision > 0 {
			return m, nil
		}
		if msg.Revision < m.steeringRevision {
			return m, nil
		}
		m.steeringRestored = false
		m.steeringRevision = msg.Revision
		m.setSteeringState(msg.Messages, msg.Inputs)
		m.layout()
	case OperationDoneMsg:
		if msg.ID != 0 && msg.ID != m.operationID {
			return m, nil
		}
		m.busy, m.flushPending, m.status = false, false, ""
		m.canceling, m.compacting = false, false
		if msg.Quit && msg.Err == nil {
			m.closing = true
			return m, tea.Quit
		}
		if msg.ResetSessionUI {
			m.resetSessionUI()
		}
		if msg.Snapshot != nil {
			if msg.RetainMessages {
				preserveSteering := m.steeringRestored
				m.applyMetadata(*msg.Snapshot)
				if preserveSteering {
					m.state.Steering = nil
					m.state.SteeringInputs = nil
				}
			} else {
				m.applySnapshot(*msg.Snapshot)
			}
		}
		m.steeringRestored = false
		if msg.ResetTranscript {
			m.viewport.GotoBottom()
			m.cache = make(map[string]cachedMessage)
			m.markdownPreview = make(map[string]string)
		}
		if msg.Tail != nil {
			m.replaceTail(*msg.Tail)
		}
		for _, change := range msg.Changes {
			m.upsertMessage(change)
		}
		if msg.RestoreEditor {
			m.resetPastes()
			editorValue := msg.EditorValue
			if msg.EditorMessage != nil {
				editorValue = m.editorTextForMessage(*msg.EditorMessage)
			}
			m.editor.SetValue(editorValue)
			m.editor.CursorEnd()
			m.completion, m.completionHidden = 0, true
		}
		if msg.Err != nil {
			m.notice("Error: " + msg.Err.Error())
		} else if msg.Notice != "" {
			m.notice(msg.Notice)
		}
		m.layout()
		m.refreshTranscript()
	case spinner.TickMsg:
		if m.busy {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			m.refreshTranscript()
			return m, cmd
		}
	case clipboardReadMsg:
		if msg.err != nil {
			m.notice("Clipboard paste failed: " + msg.err.Error())
		} else {
			for _, image := range msg.data.Images {
				m.handleImage(image)
			}
			text := msg.data.Text
			if len(msg.data.Images) == 0 && text != "" {
				m.handlePaste(text)
			}
		}
	case clipboardWriteMsg:
		if msg.err != nil {
			m.notice("Clipboard copy failed: " + msg.err.Error())
			break
		}
		m.copyNoticeSeq++
		sequence := m.copyNoticeSeq
		m.copyNotice = true
		return m, tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return clearCopyNoticeMsg(sequence) })
	case clearCopyNoticeMsg:
		if uint64(msg) == m.copyNoticeSeq {
			m.copyNotice = false
		}
	default:
		if m.selector != nil {
			before := m.selector.input.Value()
			var cmd tea.Cmd
			m.selector.input, cmd = m.selector.input.Update(msg)
			if before != m.selector.input.Value() {
				m.selector.filter()
				m.layout()
			}
			return m, cmd
		}
		before := m.editor.Value()
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		if before != m.editor.Value() {
			m.completion, m.completionHidden = 0, false
			m.layout()
		}
		return m, cmd
	}
	return m, nil
}

// applySnapshot 复制切片边界，预览/运行侧之后的赋值不会改动 UI
func (m *Model) applySnapshot(s Snapshot) {
	m.applyMetadata(s)
	m.steeringRestored = false
	m.messageIndex = make(map[string]int, len(s.Messages))
	for i, message := range s.Messages {
		m.messageIndex[message.ID] = i
	}
	m.state.Messages = append([]Message(nil), s.Messages...)
	for i := range m.state.Messages {
		m.state.Messages[i].ToolGroup = cloneToolGroup(m.state.Messages[i].ToolGroup)
		if skill := m.state.Messages[i].Skill; skill != nil {
			copy := *skill
			m.state.Messages[i].Skill = &copy
		}
		if divider := m.state.Messages[i].Divider; divider != nil {
			copy := *divider
			m.state.Messages[i].Divider = &copy
		}
	}
	m.state.Sessions = append([]SessionOption(nil), s.Sessions...)
	m.state.Tree = append([]TreeNode(nil), s.Tree...)
	m.state.Models = append([]ModelOption(nil), s.Models...)
	for i := range m.state.Models {
		m.state.Models[i].Levels = append([]string(nil), s.Models[i].Levels...)
	}
	// 快照可能切换了会话，丢弃已不在显示投影中的渲染缓存
	keep := make(map[string]cachedMessage, len(s.Messages))
	for _, message := range s.Messages {
		if cached, ok := m.cache[message.ID]; ok {
			keep[message.ID] = cached
		}
	}
	m.cache = keep
}

func (m *Model) currentModel() ModelOption {
	for _, option := range m.state.Models {
		if option.Key() == m.state.ModelKey {
			return option
		}
	}
	if m.state.CurrentModel.ID != "" {
		return m.state.CurrentModel
	}
	if provider, id, ok := strings.Cut(m.state.ModelKey, "/"); ok {
		return ModelOption{Provider: provider, ID: id, Name: id}
	}
	return ModelOption{ID: "no-model"}
}

// dispatch 先锁定 UI 操作状态，再让适配器异步执行
func (m *Model) dispatch(kind, value string) tea.Cmd {
	return m.dispatchMessage(kind, value, nil)
}

func (m *Model) dispatchMessage(kind, value string, message *llm.UserMessage) tea.Cmd {
	if m.busy {
		if kind == "tree" {
			m.notice("Wait for the current operation to finish before changing the tree.")
		} else {
			m.notice("An operation is running. Press Escape to interrupt.")
		}
		return nil
	}
	if m.handlers.Request == nil {
		return nil
	}
	m.busy, m.status = true, "Working"
	m.canceling, m.compacting = false, false
	if kind == "compact" {
		m.status = "Compacting context…"
		m.compacting = true
	}
	m.operationID++
	return tea.Batch(m.spinner.Tick, m.handlers.Request(Request{ID: m.operationID, AfterID: m.lastMessageID(), Kind: kind, Value: value, Message: message}))
}

// dispatchSteering 把运行中的普通输入交给当前操作，不改变操作 ID 或 busy 状态
func (m *Model) dispatchSteering(value string) tea.Cmd {
	message := textUserMessage(value)
	return m.dispatchSteeringMessage(value, &message)
}

func (m *Model) dispatchSteeringMessage(value string, message *llm.UserMessage) tea.Cmd {
	if !m.busy {
		return nil
	}
	if message != nil && m.handlers.EnqueueSteeringMessage != nil {
		if err := m.handlers.EnqueueSteeringMessage(*message); err != nil {
			m.restoreEditorMessage(message, value)
			m.completion, m.completionHidden = 0, true
			m.notice("Error: " + err.Error())
			m.layout()
			return nil
		}
		m.steeringRestored = false
		m.steeringRevision++
		m.appendSteeringState(value, message, false)
		if !m.canceling && !m.compacting {
			m.status = "Working"
		}
		m.layout()
		return nil
	}
	if m.handlers.EnqueueSteering != nil {
		if message != nil && messageHasImages(*message) {
			m.restoreEditorMessage(message, value)
			m.notice("Structured image steering is unavailable.")
			m.layout()
			return nil
		}
		if err := m.handlers.EnqueueSteering(value); err != nil {
			m.restoreEditorMessage(message, value)
			m.completion, m.completionHidden = 0, true
			m.notice("Error: " + err.Error())
			m.layout()
			return nil
		}
		m.steeringRestored = false
		m.steeringRevision++
		m.appendSteeringState(value, message, false)
		if !m.canceling && !m.compacting {
			m.status = "Working"
		}
		m.layout()
		return nil
	}
	if m.handlers.Request == nil {
		return nil
	}
	return m.handlers.Request(Request{ID: m.operationID, AfterID: m.lastMessageID(), Kind: "steer", Value: value, Message: message})
}

func textUserMessage(value string) llm.UserMessage {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: value}}}
}

func (m *Model) appendSteeringState(value string, message *llm.UserMessage, deduplicate bool) {
	if message == nil {
		text := textUserMessage(value)
		message = &text
	}
	if len(m.state.SteeringInputs) != len(m.state.Steering) {
		m.state.SteeringInputs = make([]llm.UserMessage, len(m.state.Steering))
		for i, existing := range m.state.Steering {
			m.state.SteeringInputs[i] = textUserMessage(existing)
		}
	}
	if deduplicate && len(m.state.Steering) > 0 {
		last := len(m.state.Steering) - 1
		if m.state.Steering[last] == value && reflect.DeepEqual(m.state.SteeringInputs[last], *message) {
			return
		}
	}
	m.state.Steering = append(m.state.Steering, value)
	m.state.SteeringInputs = append(m.state.SteeringInputs, cloneUserMessage(*message))
	m.state.SteeringRevision = m.steeringRevision
}

func (m *Model) setSteeringState(messages []string, inputs []llm.UserMessage) {
	m.state.Steering = append([]string(nil), messages...)
	if len(inputs) == len(messages) {
		m.state.SteeringInputs = cloneUserMessages(inputs)
		m.state.SteeringRevision = m.steeringRevision
		return
	}
	m.state.SteeringInputs = make([]llm.UserMessage, len(messages))
	for i, message := range messages {
		m.state.SteeringInputs[i] = textUserMessage(message)
	}
	m.state.SteeringRevision = m.steeringRevision
}

// restoreQueuedSteering 按 PI 的顺序把 steering 放回编辑器，并原子清空后台队列
func (m *Model) restoreQueuedSteering() int {
	if m.handlers.RestoreSteeringMessages != nil || len(m.state.SteeringInputs) > 0 {
		var queued []llm.UserMessage
		backendQueue := m.handlers.RestoreSteeringMessages != nil || m.handlers.RestoreSteering != nil
		if m.handlers.RestoreSteeringMessages != nil {
			queued = m.handlers.RestoreSteeringMessages()
		} else if m.handlers.RestoreSteering != nil {
			for _, value := range m.handlers.RestoreSteering() {
				queued = append(queued, textUserMessage(value))
			}
		} else {
			queued = cloneUserMessages(m.state.SteeringInputs)
		}
		if len(queued) == 0 && !backendQueue {
			if len(m.state.SteeringInputs) == len(m.state.Steering) && len(m.state.SteeringInputs) > 0 {
				queued = cloneUserMessages(m.state.SteeringInputs)
			} else if len(m.state.Steering) > 0 {
				queued = make([]llm.UserMessage, len(m.state.Steering))
				for i, value := range m.state.Steering {
					queued[i] = textUserMessage(value)
				}
			}
		}
		if len(queued) == 0 {
			m.layout()
			return 0
		}
		m.state.Steering = nil
		m.state.SteeringInputs = nil
		m.steeringRevision++
		m.state.SteeringRevision = m.steeringRevision
		m.steeringRestored = true
		current := m.editor.Value()
		var currentMessage *llm.UserMessage
		if message, ok := m.buildUserMessage(); ok {
			copy := cloneUserMessage(message)
			currentMessage = &copy
		}
		m.resetPastes()
		var parts []string
		for _, message := range queued {
			parts = append(parts, m.editorTextForMessage(message))
		}
		queuedText := strings.Join(parts, "\n\n")
		if currentMessage != nil {
			current = m.editorTextForMessage(*currentMessage)
		}
		if strings.TrimSpace(current) != "" {
			queuedText += "\n\n" + current
		}
		m.editor.SetValue(queuedText)
		m.editor.CursorEnd()
		m.completion, m.completionHidden = 0, true
		m.layout()
		return len(queued)
	}
	var queued []string
	if m.handlers.RestoreSteering != nil {
		queued = m.handlers.RestoreSteering()
	} else {
		queued = append([]string(nil), m.state.Steering...)
	}
	m.state.Steering = nil
	if len(queued) == 0 {
		m.layout()
		return 0
	}
	m.steeringRevision++
	m.state.SteeringRevision = m.steeringRevision
	m.steeringRestored = true
	queuedText := strings.Join(queued, "\n\n")
	current := m.editor.Value()
	if strings.TrimSpace(current) != "" {
		queuedText += "\n\n" + current
	}
	m.editor.SetValue(queuedText)
	m.editor.CursorEnd()
	m.completion, m.completionHidden = 0, true
	m.resetPastes()
	m.layout()
	return len(queued)
}

func (m *Model) notice(value string) {
	m.upsertMessage(Message{ID: fmt.Sprintf("notice-%d", len(m.state.Messages)), Role: "notice", Text: value})
	m.refreshTranscript()
}

// resetSessionUI 仅在新会话切换成功后恢复编辑器与显示偏好的初始状态
func (m *Model) resetSessionUI() {
	m.editor.Reset()
	m.resetPastes()
	m.history, m.draft, m.draftMessage = nil, "", nil
	m.historyMessages = nil
	m.historyPos, m.completion = 0, 0
	m.completionHidden = true
	m.selector = nil
	m.editor.Focus()
	m.hideThinking, m.showAllTools = false, false
	m.clearSelection()
	m.cache = make(map[string]cachedMessage)
	m.markdownPreview = make(map[string]string)
	m.steeringRevision = 0
	m.state.Steering = nil
	m.state.SteeringInputs = nil
	m.state.SteeringRevision = 0
	m.copyNoticeSeq++
	m.copyNotice = false
	m.lastCtrlC = time.Time{}
	m.viewport.GotoTop()
}

// refreshTranscript 仅重渲染内容/宽度/思考可见性发生变化的消息
func (m *Model) refreshTranscript() {
	if m.width == 0 {
		return
	}
	follow := m.viewport.AtBottom()
	var blocks []string
	for _, message := range m.state.Messages {
		cached := m.cache[message.ID]
		animate := false
		if m.busy && message.ToolGroup != nil {
			_, _, animate = groupState(message.ToolGroup)
		}
		if animate || cached.revision != message.Revision || cached.width != m.width || cached.hidden != m.hideThinking || cached.allTools != m.showAllTools {
			cached = cachedMessage{revision: message.Revision, width: m.width, hidden: m.hideThinking, allTools: m.showAllTools, view: m.renderMessage(message)}
			m.cache[message.ID] = cached
		}
		blocks = append(blocks, cached.view)
	}
	m.viewport.SetContent(strings.Join(blocks, "\n\n"))
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *Model) upsertMessage(message Message) {
	if index, ok := m.messageIndex[message.ID]; ok {
		m.state.Messages[index] = message
		return
	}
	m.messageIndex[message.ID] = len(m.state.Messages)
	m.state.Messages = append(m.state.Messages, message)
}

// applyMetadata 更新操作结果的设置与目录，不复制未变化的 transcript
func (m *Model) applyMetadata(s Snapshot) {
	messages := m.state.Messages
	steering := append([]string(nil), m.state.Steering...)
	steeringInputs := cloneUserMessages(m.state.SteeringInputs)
	steeringRevision := m.steeringRevision
	m.state = s
	m.state.Messages = messages
	m.state.Sessions = append([]SessionOption(nil), s.Sessions...)
	m.state.Tree = append([]TreeNode(nil), s.Tree...)
	m.state.Models = append([]ModelOption(nil), s.Models...)
	m.state.Skills = append([]SkillOption(nil), s.Skills...)
	if steeringRevision == 0 || s.SteeringRevision >= steeringRevision {
		m.state.Steering = append([]string(nil), s.Steering...)
		m.state.SteeringInputs = cloneUserMessages(s.SteeringInputs)
		m.state.SteeringRevision = s.SteeringRevision
		m.steeringRevision = s.SteeringRevision
	} else {
		m.state.Steering = steering
		m.state.SteeringInputs = steeringInputs
		m.state.SteeringRevision = steeringRevision
		m.steeringRevision = steeringRevision
	}
	m.completion = 0
	for i := range m.state.Models {
		m.state.Models[i].Levels = append([]string(nil), s.Models[i].Levels...)
	}
}

func (m *Model) lastMessageID() string {
	if len(m.state.Messages) > 0 {
		return m.state.Messages[len(m.state.Messages)-1].ID
	}
	return ""
}

func cloneUserMessage(message llm.UserMessage) llm.UserMessage {
	message.Content = append([]llm.Content(nil), message.Content...)
	return message
}

func cloneUserMessages(messages []llm.UserMessage) []llm.UserMessage {
	if len(messages) == 0 {
		return nil
	}
	out := make([]llm.UserMessage, len(messages))
	for i, message := range messages {
		out[i] = cloneUserMessage(message)
	}
	return out
}

// replaceTail 只替换当前操作产生的尾部，旧历史的身份与缓存保持不变
func (m *Model) replaceTail(tail MessageTail) {
	start := 0
	if index, ok := m.messageIndex[tail.AfterID]; ok {
		start = index + 1
	} else if tail.AfterID != "" {
		return
	}
	for _, message := range m.state.Messages[start:] {
		delete(m.messageIndex, message.ID)
	}
	m.state.Messages = m.state.Messages[:start]
	for _, message := range tail.Messages {
		m.upsertMessage(message)
	}
}
