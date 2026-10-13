// Package tui 实现 Pi 风格的全屏交互界面；核心运行通过回调与不可变消息接入
package tui

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ModelOption 是可供选择的模型及核心已解析的思考等级，不含凭据
type ModelOption struct {
	ID, Provider, Name string
	ContextWindow      int
	Levels             []string
}

func (m ModelOption) Key() string { return m.Provider + "/" + m.ID }

type SessionOption struct {
	Path, Summary, Modified string
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
	ID, Role, Text, Thinking string
	Elapsed                  time.Duration
	ToolGroup                *ToolGroupView
	Divider                  *TurnDividerView
}

// Snapshot 由运行侧在安全边界提供；UI 不回读活动中的 Agent/SessionManager
type Snapshot struct {
	Cwd, Branch, ModelKey, DefaultModelKey, Thinking string
	// CurrentModel 保留运行中的模型，即使选择器暂时只展示有限目录
	CurrentModel ModelOption
	Models       []ModelOption
	Sessions     []SessionOption
	Messages     []Message
}

// Request 表示用户发起的一次操作；Kind 为 run/model/thinking/resume/compact
type Request struct{ Kind, Value string }

// Handlers 由运行适配器提供，命令在 tea.Cmd 中执行，取消只发信号而不阻塞 Update
type Handlers struct {
	Request        func(Request) tea.Cmd
	Cancel         func()
	ReadClipboard  func() (string, error)
	WriteClipboard func(string) error
}

// MessageMsg 更新具有同一 ID 的消息；OperationDoneMsg 才结束整个用户操作
type MessageMsg Message
type StatusMsg string
type OperationDoneMsg struct {
	Snapshot *Snapshot
	Err      error
}
type flushMsg struct{}

type clipboardReadMsg struct {
	text string
	err  error
}

type clipboardWriteMsg struct{ err error }

type clearCopyNoticeMsg uint64

type screenPoint struct{ x, y int }

type cachedMessage struct {
	message  Message
	width    int
	hidden   bool
	allTools bool
	view     string
}

// Model 独占全部显示状态，生命周期与消息定稿分离
type Model struct {
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
	historyPos       int
	draft            string
	busy, closing    bool
	status           string
	hideThinking     bool
	showAllTools     bool
	lastCtrlC        time.Time
	flushPending     bool
	cache            map[string]cachedMessage
	markdown         markdownRenderer
	clock            func() time.Time
	selectionAnchor  screenPoint
	selectionFocus   screenPoint
	selecting        bool
	lastClick        screenPoint
	lastClickAt      time.Time
	mouseFragment    string
	copyNoticeSeq    uint64
	copyNotice       bool
	pastes           map[int]string
	pasteCounter     int
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
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)), cache: make(map[string]cachedMessage), clock: time.Now}
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
		// 1.同一消息原地更新，避免 MessageEnd 与操作结束各追加一次
		found := false
		for i := range m.state.Messages {
			if m.state.Messages[i].ID == msg.ID {
				m.state.Messages[i], found = Message(msg), true
				break
			}
		}
		if !found {
			m.state.Messages = append(m.state.Messages, Message(msg))
		}
		if !m.flushPending {
			m.flushPending = true
			return m, tea.Tick(time.Second/60, func(time.Time) tea.Msg { return flushMsg{} })
		}
	case flushMsg:
		m.flushPending = false
		m.refreshTranscript()
	case StatusMsg:
		m.status = string(msg)
	case OperationDoneMsg:
		m.busy, m.flushPending, m.status = false, false, ""
		if msg.Snapshot != nil {
			m.applySnapshot(*msg.Snapshot)
		}
		if msg.Err != nil {
			m.notice("Error: " + msg.Err.Error())
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
		if msg.err == nil && msg.text != "" {
			m.handlePaste(msg.text)
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
	m.state = s
	m.state.Messages = append([]Message(nil), s.Messages...)
	for i := range m.state.Messages {
		m.state.Messages[i].ToolGroup = cloneToolGroup(m.state.Messages[i].ToolGroup)
		if divider := m.state.Messages[i].Divider; divider != nil {
			copy := *divider
			m.state.Messages[i].Divider = &copy
		}
	}
	m.state.Sessions = append([]SessionOption(nil), s.Sessions...)
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
	if m.busy {
		m.notice("An operation is running. Press Escape to interrupt.")
		return nil
	}
	if m.handlers.Request == nil {
		return nil
	}
	m.busy, m.status = true, "Working"
	if kind == "compact" {
		m.status = "Compacting context…"
	}
	return tea.Batch(m.spinner.Tick, m.handlers.Request(Request{Kind: kind, Value: value}))
}

func (m *Model) notice(value string) {
	m.state.Messages = append(m.state.Messages, Message{ID: fmt.Sprintf("notice-%d", len(m.state.Messages)), Role: "notice", Text: value})
	m.refreshTranscript()
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
		if animate || cached.message != message || cached.width != m.width || cached.hidden != m.hideThinking || cached.allTools != m.showAllTools {
			cached = cachedMessage{message: message, width: m.width, hidden: m.hideThinking, allTools: m.showAllTools, view: m.renderMessage(message)}
			m.cache[message.ID] = cached
		}
		blocks = append(blocks, cached.view)
	}
	m.viewport.SetContent(strings.Join(blocks, "\n\n"))
	if follow {
		m.viewport.GotoBottom()
	}
}
