// Package tui 实现全屏交互界面，核心运行通过回调与不可变消息接入
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

// ModelOption 是可供选择的模型及已解析的思考等级
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

// TreeNode 是 TUI 的 user-only 树投影，连接线由运行适配器预先计算
type TreeNode struct {
	ID, Text, Prefix string
	ParentID         string
	Depth            int
	Current          bool
}

// SkillOption 是命令补全使用的技能元数据
type SkillOption struct {
	Name, Description string
}

// SkillInvocationView 保存已提交正文的显示投影
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
	Index int
	// Round 是该思考所属的 assistant 轮次：同一轮里的 thinking 排在它自己的调用之前
	Round       int
	Text        string
	Active      bool
	Tokens      int
	TokensExact bool
}

// ToolCallView 保存完整工具状态，compact/full 只改变展示方式
type ToolCallView struct {
	ID, Name string
	// Round 是该调用所属的 assistant 轮次，用来把 thinking 排到同一轮的调用之前
	Round     int
	Arguments string
	Result    string
	// Details 是工具返回的结构化详情（如 edit 的 diff），JSON 编码后只供展示
	Details string
	Status  ToolStatus
	// ArgsComplete 表示模型已写完参数；Pi 用它判断行是否仍处于进行中
	ArgsComplete bool
	Elapsed      time.Duration
	StartedAt    time.Time
	EndedAt      time.Time
}

// ToolGroupView 将连续 thinking 和工具调用作为一个可折叠工作块
type ToolGroupView struct {
	ID       string
	Calls    []ToolCallView
	Thinking []ThinkingBlockView
	Active   bool
	Sealed   bool
}

// TurnDividerView 标记一个 turn 的最终工作耗时与结束时刻
type TurnDividerView struct {
	Elapsed    time.Duration
	FinishedAt time.Time
}

// Message 是显示投影，ID 在流式更新中保持稳定
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

// ImageView 是消息投影中的图片元数据
type ImageView struct {
	MIMEType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
}

// Snapshot 由运行侧在安全边界提供，UI 不回读活动中的 Agent/SessionManager
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
	FollowUp         []string
	FollowUpInputs   []llm.UserMessage
	FollowUpRevision uint64
}

type queuedKind uint8

const (
	queuedSteering queuedKind = iota
	queuedFollowUp
)

type compactionQueuedMessage struct {
	message llm.UserMessage
	kind    queuedKind
}

// ClipboardData 是系统剪贴板的一次读取结果
type ClipboardData struct {
	Text   string
	Images []llm.ImageContent
}

// Request 表示用户发起的一次操作，Kind 为 run/model/thinking/resume/tree/compact/rename/new/reload/clear/delete
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
	// Status 查询模型、用量与会话文件，不启动模型 operation
	Status func() tea.Cmd
	Cancel func()
	// EnqueueSteering 同步接受一条 steering，避免撤回与异步入队竞态
	EnqueueSteering        func(string) error
	EnqueueSteeringMessage func(llm.UserMessage) error
	// RestoreSteering 原子取出后台尚未消费的 steering，供撤回到编辑器
	RestoreSteering         func() []string
	RestoreSteeringMessages func() []llm.UserMessage
	// EnqueueFollowUp 同步接受一条 follow-up
	EnqueueFollowUp        func(string) error
	EnqueueFollowUpMessage func(llm.UserMessage) error
	// RestoreFollowUpMessages 原子取出后台尚未消费的 follow-up
	RestoreFollowUpMessages func() []llm.UserMessage
	ClearFollowUp           func() []llm.UserMessage
	ReadClipboard           func() (string, error)
	ReadClipboardData       func() (ClipboardData, error)
	WriteClipboard          func(string) error
}

// MessageMsg 更新具有同一 ID 的消息，OperationDoneMsg 才结束整个用户操作
type MessageMsg Message
type MessageCommittedMsg struct {
	PreviousID string
	Message    Message
}

type StatusMsg string
type SessionStatusMsg struct {
	Model, Provider, ThreadName string
	SessionID, Transcript       string
	Tokens, ContextWindow       int
	Available                   bool
	Err                         error
}
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
type FollowUpResultMsg struct {
	ID       uint64
	Value    string
	Message  *llm.UserMessage
	Revision uint64
	Err      error
}
type QueueStateMsg struct {
	Steering         []string
	SteeringInputs   []llm.UserMessage
	SteeringRevision uint64
	FollowUp         []string
	FollowUpInputs   []llm.UserMessage
	FollowUpRevision uint64
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
	revision     uint64
	width        int
	showThinking bool
	allTools     bool
	// hidden 记录 Ctrl+Q 遮罩状态，切换时必须重画
	hidden bool
	// expanded 是单击展开状态的指纹，任何一处切换都要重画
	expanded string
	view     string
	// hits 是这条消息内部的可点击区域，行号相对消息块起点
	hits []toolHit
}

// Model 独占全部显示状态
type Model struct {
	messageIndex             map[string]int
	operationID              uint64
	state                    Snapshot
	handlers                 Handlers
	width, height            int
	editor                   textarea.Model
	viewport                 viewport.Model
	spinner                  spinner.Model
	selector                 *selector
	completion               int
	completionHidden         bool
	history                  []string
	historyMessages          []llm.UserMessage
	historyPos               int
	draft                    string
	draftMessage             *llm.UserMessage
	busy, closing            bool
	initialPrompt            string
	operationIsRun           bool
	steeringRestored         bool
	canceling                bool
	compacting               bool
	compactionQueuedMessages []compactionQueuedMessage
	status                   string
	showThinking             bool
	showAllTools             bool
	// hideTools 是 Ctrl+Q 的遮罩：整段工具组与思考块只剩一行提示
	hideTools    bool
	lastCtrlC    time.Time
	flushPending bool
	cache        map[string]cachedMessage
	// expandedGroups / expandedCalls / expandedThoughts 记录单击展开的组、调用与思考块
	expandedGroups   map[string]bool
	expandedCalls    map[string]bool
	expandedThoughts map[string]bool
	// lastToolHits 是最近一次渲染的点击命中区间，用于把单击映射回调用
	lastToolHits     []toolHit
	steeringRevision uint64
	followUpRestored bool
	followUpRevision uint64
	// lastMessageHits 由 renderMessage 写入，refreshTranscript 换算成屏幕行
	lastMessageHits  []toolHit
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
	statusNoticeSeq  uint64
	pastes           map[int]string
	pasteCounter     int
	images           map[int]llm.ImageContent
	imageCounter     int
	cursorRow        atomic.Int64
	cursorColumn     atomic.Int64
}

// New 创建界面，预览与正式运行共享同一套组件和消息处理
func New(state Snapshot, handlers Handlers) *Model {
	return newModel(state, handlers, "")
}

// NewWithInitialPrompt 创建启动后自动提交一次 prompt 的界面
func NewWithInitialPrompt(state Snapshot, handlers Handlers, prompt string) *Model {
	return newModel(state, handlers, prompt)
}

func newModel(state Snapshot, handlers Handlers, initialPrompt string) *Model {
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
	m := &Model{handlers: handlers, editor: ed, initialPrompt: initialPrompt, viewport: viewport.New(0, 0),
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)), cache: make(map[string]cachedMessage), images: make(map[int]llm.ImageContent), clock: time.Now,
		expandedGroups: make(map[string]bool), expandedCalls: make(map[string]bool), expandedThoughts: make(map[string]bool)}
	m.applySnapshot(state)
	return m
}

func (m *Model) Init() tea.Cmd {
	if m.initialPrompt == "" {
		return textarea.Blink
	}
	prompt := m.initialPrompt
	m.initialPrompt = ""
	return tea.Batch(textarea.Blink, m.dispatch("run", prompt))
}

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
		case value == "Working" && m.compacting:
			m.compacting = false
			m.flushCompactionQueuedMessages()
		case value == "Compaction failed" && m.compacting:
			m.compacting = false
			m.restoreCompactionMessages(m.compactionQueuedMessages)
		}
	case SessionStatusMsg:
		if msg.Err != nil {
			m.notice("Error: " + msg.Err.Error())
		} else {
			// 每次执行都追加一组命令回显与状态块，重复查询保留历史
			m.statusNoticeSeq++
			seq := m.statusNoticeSeq
			m.upsertMessage(Message{ID: fmt.Sprintf("status-command/%d", seq), Role: "command", Text: "/status"})
			m.upsertMessage(Message{ID: fmt.Sprintf("status/%d", seq), Role: "status", Text: formatStatus(msg)})
			m.refreshTranscript()
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
	case FollowUpResultMsg:
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
			m.followUpRestored = false
			if msg.Revision == 0 || msg.Revision > m.followUpRevision {
				if msg.Revision > m.followUpRevision {
					m.followUpRevision = msg.Revision
				}
				message := msg.Message
				if message == nil {
					value := textUserMessage(msg.Value)
					message = &value
				}
				m.appendFollowUpState(msg.Value, message, true)
			}
			if !m.canceling && !m.compacting {
				m.status = "Working"
			}
		}
		m.layout()
	case QueueStateMsg:
		if !m.steeringRestored && msg.SteeringRevision >= m.steeringRevision {
			m.steeringRevision = msg.SteeringRevision
			m.setSteeringState(msg.Steering, msg.SteeringInputs)
		}
		if !m.followUpRestored && msg.FollowUpRevision >= m.followUpRevision {
			m.followUpRevision = msg.FollowUpRevision
			m.setFollowUpState(msg.FollowUp, msg.FollowUpInputs)
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
		if len(m.compactionQueuedMessages) > 0 {
			m.restoreCompactionMessages(m.compactionQueuedMessages)
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
				preserveFollowUp := m.followUpRestored
				m.applyMetadata(*msg.Snapshot)
				if preserveSteering {
					m.state.Steering = nil
					m.state.SteeringInputs = nil
				}
				if preserveFollowUp {
					m.state.FollowUp = nil
					m.state.FollowUpInputs = nil
				}
			} else {
				m.applySnapshot(*msg.Snapshot)
			}
		}
		m.steeringRestored = false
		m.followUpRestored = false
		if msg.ResetTranscript {
			m.viewport.GotoBottom()
			m.cache = make(map[string]cachedMessage)
			m.lastMessageHits = nil
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
	m.operationIsRun = kind == "run"
	if kind == "compact" {
		m.status = "Compacting context…"
		m.compacting = true
	}
	m.operationID++
	return tea.Batch(m.spinner.Tick, m.handlers.Request(Request{ID: m.operationID, AfterID: m.lastMessageID(), Kind: kind, Value: value, Message: message}))
}

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

func (m *Model) dispatchFollowUp(value string) tea.Cmd {
	message := textUserMessage(value)
	return m.dispatchFollowUpMessage(value, &message)
}

func (m *Model) dispatchFollowUpMessage(value string, message *llm.UserMessage) tea.Cmd {
	if !m.busy {
		return nil
	}
	if message != nil && m.handlers.EnqueueFollowUpMessage != nil {
		if err := m.handlers.EnqueueFollowUpMessage(*message); err != nil {
			m.restoreEditorMessage(message, value)
			m.completion, m.completionHidden = 0, true
			m.notice("Error: " + err.Error())
			m.layout()
			return nil
		}
		m.followUpRestored = false
		m.followUpRevision++
		m.appendFollowUpState(value, message, false)
		if !m.canceling && !m.compacting {
			m.status = "Working"
		}
		m.layout()
		return nil
	}
	if m.handlers.EnqueueFollowUp != nil {
		if message != nil && messageHasImages(*message) {
			m.restoreEditorMessage(message, value)
			m.notice("Structured image follow-up is unavailable.")
			m.layout()
			return nil
		}
		if err := m.handlers.EnqueueFollowUp(value); err != nil {
			m.restoreEditorMessage(message, value)
			m.completion, m.completionHidden = 0, true
			m.notice("Error: " + err.Error())
			m.layout()
			return nil
		}
		m.followUpRestored = false
		m.followUpRevision++
		m.appendFollowUpState(value, message, false)
		if !m.canceling && !m.compacting {
			m.status = "Working"
		}
		m.layout()
		return nil
	}
	if m.handlers.Request == nil {
		return nil
	}
	return m.handlers.Request(Request{ID: m.operationID, AfterID: m.lastMessageID(), Kind: "follow-up", Value: value, Message: message})
}

func textUserMessage(value string) llm.UserMessage {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: value}}}
}

func (m *Model) enqueueCompactionMessage(item compactionQueuedMessage) error {
	message := item.message
	if item.kind == queuedSteering {
		if m.handlers.EnqueueSteeringMessage != nil {
			return m.handlers.EnqueueSteeringMessage(message)
		}
		if messageHasImages(message) {
			return fmt.Errorf("structured image steering is unavailable")
		}
		if m.handlers.EnqueueSteering != nil {
			return m.handlers.EnqueueSteering(userMessageText(message))
		}
		return fmt.Errorf("steering queue is unavailable")
	}
	if m.handlers.EnqueueFollowUpMessage != nil {
		return m.handlers.EnqueueFollowUpMessage(message)
	}
	if messageHasImages(message) {
		return fmt.Errorf("structured image follow-up is unavailable")
	}
	if m.handlers.EnqueueFollowUp != nil {
		return m.handlers.EnqueueFollowUp(userMessageText(message))
	}
	return fmt.Errorf("follow-up queue is unavailable")
}

func userMessageText(message llm.UserMessage) string {
	var parts []string
	for _, content := range message.Content {
		if text, ok := content.(llm.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}

	return strings.Join(parts, "\n")
}

func (m *Model) takeCompactionMessages(kind queuedKind) []llm.UserMessage {
	var selected []llm.UserMessage
	remaining := make([]compactionQueuedMessage, 0, len(m.compactionQueuedMessages))
	for _, item := range m.compactionQueuedMessages {
		if item.kind == kind {
			selected = append(selected, cloneUserMessage(item.message))
			continue
		}
		remaining = append(remaining, item)
	}
	m.compactionQueuedMessages = remaining
	return selected
}

func (m *Model) flushCompactionQueuedMessages() {
	// 手动压缩结束后没有可续跑的 run，暂存项留给 OperationDone 回填编辑器
	if !m.operationIsRun {
		return
	}
	queued := m.compactionQueuedMessages
	m.compactionQueuedMessages = nil
	for index, item := range queued {
		if err := m.enqueueCompactionMessage(item); err != nil {
			m.restoreCompactionMessages(queued[index:])
			m.notice("Error: " + err.Error())
			return
		}
		value := m.editorTextForMessage(item.message)
		message := item.message
		if item.kind == queuedSteering {
			m.steeringRestored = false
			m.steeringRevision++
			m.appendSteeringState(value, &message, false)
		} else {
			m.followUpRestored = false
			m.followUpRevision++
			m.appendFollowUpState(value, &message, false)
		}
	}
	m.status = "Working"
	m.layout()
}

func (m *Model) restoreCompactionMessages(queued []compactionQueuedMessage) {
	if len(queued) == 0 {
		return
	}
	current := m.editor.Value()
	var currentMessage *llm.UserMessage
	if message, ok := m.buildUserMessage(); ok {
		copy := cloneUserMessage(message)
		currentMessage = &copy
	}
	// 先清空 paste/image 表，再生成 marker，保证回填后的图片仍能提交
	m.resetPastes()
	parts := make([]string, 0, len(queued)+1)
	for _, item := range queued {
		parts = append(parts, m.editorTextForMessage(item.message))
	}
	if currentMessage != nil {
		current = m.editorTextForMessage(*currentMessage)
	}
	if strings.TrimSpace(current) != "" {
		parts = append(parts, current)
	}
	m.compactionQueuedMessages = nil
	m.editor.SetValue(strings.Join(parts, "\n\n"))
	m.editor.CursorEnd()
	m.completion, m.completionHidden = 0, true
	m.layout()
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

func (m *Model) appendFollowUpState(value string, message *llm.UserMessage, deduplicate bool) {
	if message == nil {
		text := textUserMessage(value)
		message = &text
	}
	if len(m.state.FollowUpInputs) != len(m.state.FollowUp) {
		m.state.FollowUpInputs = make([]llm.UserMessage, len(m.state.FollowUp))
		for i, existing := range m.state.FollowUp {
			m.state.FollowUpInputs[i] = textUserMessage(existing)
		}
	}
	if deduplicate && len(m.state.FollowUp) > 0 {
		last := len(m.state.FollowUp) - 1
		if m.state.FollowUp[last] == value && reflect.DeepEqual(m.state.FollowUpInputs[last], *message) {
			return
		}
	}
	m.state.FollowUp = append(m.state.FollowUp, value)
	m.state.FollowUpInputs = append(m.state.FollowUpInputs, cloneUserMessage(*message))
	m.state.FollowUpRevision = m.followUpRevision
}

func (m *Model) setFollowUpState(messages []string, inputs []llm.UserMessage) {
	m.state.FollowUp = append([]string(nil), messages...)
	if len(inputs) == len(messages) {
		m.state.FollowUpInputs = cloneUserMessages(inputs)
		m.state.FollowUpRevision = m.followUpRevision
		return
	}
	m.state.FollowUpInputs = make([]llm.UserMessage, len(messages))
	for i, message := range messages {
		m.state.FollowUpInputs[i] = textUserMessage(message)
	}
	m.state.FollowUpRevision = m.followUpRevision
}

// restoreQueuedSteering 把 steering 按顺序放回编辑器，并原子清空后台队列
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

func (m *Model) takeFollowUpForRestore() []llm.UserMessage {
	var queued []llm.UserMessage
	if m.handlers.RestoreFollowUpMessages != nil {
		queued = m.handlers.RestoreFollowUpMessages()
	} else if len(m.state.FollowUpInputs) == len(m.state.FollowUp) && len(m.state.FollowUpInputs) > 0 {
		queued = cloneUserMessages(m.state.FollowUpInputs)
	} else {
		queued = make([]llm.UserMessage, len(m.state.FollowUp))
		for i, value := range m.state.FollowUp {
			queued[i] = textUserMessage(value)
		}
	}
	return append(queued, m.takeCompactionMessages(queuedFollowUp)...)
}

func (m *Model) restoreQueuedFollowUp() int {
	queued := m.takeFollowUpForRestore()
	if len(queued) == 0 {
		m.layout()
		return 0
	}
	m.state.FollowUp = nil
	m.state.FollowUpInputs = nil
	m.followUpRevision++
	m.state.FollowUpRevision = m.followUpRevision
	m.followUpRestored = true
	current := m.editor.Value()
	var currentMessage *llm.UserMessage
	if message, ok := m.buildUserMessage(); ok {
		copy := cloneUserMessage(message)
		currentMessage = &copy
	}
	// 先清空 paste/image 表，再生成 marker，保证回填后的图片仍能提交
	m.resetPastes()
	parts := make([]string, 0, len(queued)+1)
	for _, message := range queued {
		parts = append(parts, m.editorTextForMessage(message))
	}
	if currentMessage != nil {
		current = m.editorTextForMessage(*currentMessage)
	}
	if strings.TrimSpace(current) != "" {
		parts = append(parts, current)
	}
	m.editor.SetValue(strings.Join(parts, "\n\n"))
	m.editor.CursorEnd()
	m.completion, m.completionHidden = 0, true
	m.layout()
	return len(queued)
}

func (m *Model) restoreQueuedMessages() int {
	var steering []llm.UserMessage
	if m.handlers.RestoreSteeringMessages != nil {
		steering = m.handlers.RestoreSteeringMessages()
	} else if m.handlers.RestoreSteering != nil {
		for _, value := range m.handlers.RestoreSteering() {
			steering = append(steering, textUserMessage(value))
		}
	} else if len(m.state.SteeringInputs) == len(m.state.Steering) {
		steering = cloneUserMessages(m.state.SteeringInputs)
	}
	steering = append(steering, m.takeCompactionMessages(queuedSteering)...)
	followUp := m.takeFollowUpForRestore()
	if len(steering) == 0 && len(followUp) == 0 {
		m.layout()
		return 0
	}
	m.state.Steering = nil
	m.state.SteeringInputs = nil
	m.state.FollowUp = nil
	m.state.FollowUpInputs = nil
	m.steeringRevision++
	m.followUpRevision++
	m.state.SteeringRevision = m.steeringRevision
	m.state.FollowUpRevision = m.followUpRevision
	m.steeringRestored = true
	m.followUpRestored = true
	current := m.editor.Value()
	var currentMessage *llm.UserMessage
	if message, ok := m.buildUserMessage(); ok {
		copy := cloneUserMessage(message)
		currentMessage = &copy
	}
	// 先清空 paste/image 表，再生成 marker，保证回填后的图片仍能提交
	m.resetPastes()
	parts := make([]string, 0, len(steering)+len(followUp)+1)
	for _, message := range steering {
		parts = append(parts, m.editorTextForMessage(message))
	}
	if len(steering) > 0 && len(followUp) > 0 {
		parts = append(parts, "")
	}
	for _, message := range followUp {
		parts = append(parts, m.editorTextForMessage(message))
	}
	if currentMessage != nil {
		current = m.editorTextForMessage(*currentMessage)
	}
	if strings.TrimSpace(current) != "" {
		parts = append(parts, "", current)
	}
	m.editor.SetValue(strings.Join(parts, "\n\n"))
	m.editor.CursorEnd()
	m.completion, m.completionHidden = 0, true
	m.layout()
	return len(steering) + len(followUp)
}

func (m *Model) clearQueuedFollowUp() int {
	var count int
	if m.handlers.ClearFollowUp != nil {
		count = len(m.handlers.ClearFollowUp())
	} else {
		count = len(m.state.FollowUp)
	}
	count += len(m.takeCompactionMessages(queuedFollowUp))
	m.state.FollowUp = nil
	m.state.FollowUpInputs = nil
	m.followUpRevision++
	m.state.FollowUpRevision = m.followUpRevision
	m.followUpRestored = true
	m.layout()
	return count
}

func (m *Model) notice(value string) {
	m.upsertMessage(Message{ID: fmt.Sprintf("notice-%d", len(m.state.Messages)), Role: "notice", Text: value})
	m.refreshTranscript()
}

// resetSessionUI 在新会话切换成功后恢复编辑器与显示偏好的初始状态
func (m *Model) resetSessionUI() {
	m.editor.Reset()
	m.compactionQueuedMessages = nil
	m.resetPastes()
	m.history, m.draft, m.draftMessage = nil, "", nil
	m.historyMessages = nil
	m.historyPos, m.completion = 0, 0
	m.completionHidden = true
	m.selector = nil
	m.editor.Focus()
	m.showThinking, m.showAllTools, m.hideTools = false, false, false
	m.clearSelection()
	m.cache = make(map[string]cachedMessage)
	m.lastMessageHits = nil
	m.steeringRevision = 0
	m.state.Steering = nil
	m.state.SteeringInputs = nil
	m.state.SteeringRevision = 0
	m.followUpRestored = false
	m.followUpRevision = 0
	m.state.FollowUp = nil
	m.state.FollowUpInputs = nil
	m.state.FollowUpRevision = 0
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
	// 1.逐消息渲染，并把消息内相对行号的命中区间换算成屏幕行
	var screenHits []toolHit
	blockOffset := 0
	for _, message := range m.state.Messages {
		cached := m.cache[message.ID]
		animate := false
		if m.busy && message.ToolGroup != nil {
			animate = groupRunning(message.ToolGroup.Calls)
		}
		if animate || cached.revision != message.Revision || cached.width != m.width || cached.showThinking != m.showThinking || cached.allTools != m.showAllTools || cached.hidden != m.hideTools || cached.expanded != m.expandSignature() {
			m.lastMessageHits = nil
			view := m.renderMessage(message)
			cached = cachedMessage{revision: message.Revision, width: m.width, showThinking: m.showThinking, allTools: m.showAllTools, hidden: m.hideTools, expanded: m.expandSignature(), view: view, hits: m.lastMessageHits}
			m.cache[message.ID] = cached
		}
		for _, hit := range cached.hits {
			screenHits = append(screenHits, toolHit{Kind: hit.Kind, Key: hit.Key, Start: hit.Start + blockOffset, End: hit.End + blockOffset})
		}
		blocks = append(blocks, cached.view)
		// 消息之间用一个空行分隔，下一块的起点因此比本块末行多一行
		blockOffset += len(strings.Split(cached.view, "\n")) + 1
	}
	m.lastToolHits = screenHits
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

func (m *Model) applyMetadata(s Snapshot) {
	messages := m.state.Messages
	steering := append([]string(nil), m.state.Steering...)
	steeringInputs := cloneUserMessages(m.state.SteeringInputs)
	steeringRevision := m.steeringRevision
	followUp := append([]string(nil), m.state.FollowUp...)
	followUpInputs := cloneUserMessages(m.state.FollowUpInputs)
	followUpRevision := m.followUpRevision
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
	if followUpRevision == 0 || s.FollowUpRevision >= followUpRevision {
		m.state.FollowUp = append([]string(nil), s.FollowUp...)
		m.state.FollowUpInputs = cloneUserMessages(s.FollowUpInputs)
		m.state.FollowUpRevision = s.FollowUpRevision
		m.followUpRevision = s.FollowUpRevision
	} else {
		m.state.FollowUp = followUp
		m.state.FollowUpInputs = followUpInputs
		m.state.FollowUpRevision = followUpRevision
		m.followUpRevision = followUpRevision
	}
	m.completion = 0
	for i := range m.state.Models {
		m.state.Models[i].Levels = append([]string(nil), s.Models[i].Levels...)
	}
}

func (m *Model) lastMessageID() string {
	for i := len(m.state.Messages) - 1; i >= 0; i-- {
		message := m.state.Messages[i]
		if message.Role == "notice" || message.Role == "summary" || message.Role == "status" || message.Role == "command" {
			continue
		}
		return message.ID
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

// replaceTail 只替换当前操作产生的尾部，旧历史的身份与缓存保持不变；
// 状态行不属于会话投影，原位保留在本次新增消息之前
func (m *Model) replaceTail(tail MessageTail) {
	start := 0
	if index, ok := m.messageIndex[tail.AfterID]; ok {
		start = index + 1
	} else if tail.AfterID != "" {
		return
	}
	var kept []Message
	for _, message := range m.state.Messages[start:] {
		delete(m.messageIndex, message.ID)
		if isStatusRow(message) {
			kept = append(kept, message)
		}
	}
	m.state.Messages = m.state.Messages[:start]
	for _, message := range kept {
		m.upsertMessage(message)
	}
	for _, message := range tail.Messages {
		m.upsertMessage(message)
	}
}

// isStatusRow 标记 /status 的显示层行，既不进入会话投影也不被尾部替换删除
func isStatusRow(message Message) bool {
	return message.Role == "status" || message.Role == "command"
}
