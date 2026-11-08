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

// ToolStatus 表示一条工具调用在工具行中的执行状态
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
	Round int
	Text  string
}

// CodemodeCallStatus 是嵌套调用的显示阶段
type CodemodeCallStatus string

const (
	CodemodeQueued    CodemodeCallStatus = "queued"
	CodemodeRunning   CodemodeCallStatus = "running"
	CodemodeOK        CodemodeCallStatus = "ok"
	CodemodeError     CodemodeCallStatus = "error"
	CodemodeCancelled CodemodeCallStatus = "cancelled"
)

// CodemodeCallView 保存成功展开时的逐调用摘要
type CodemodeCallView struct {
	ID         string
	Name       string
	Args       string
	Status     CodemodeCallStatus
	DurationMS *int64 // 未启动为 nil，0 有效
	Error      string
}

// CodemodeToolCountView 保存同名工具的阶段计数
type CodemodeToolCountView struct {
	Name       string
	Success    int
	Failure    int
	Unfinished int
}

// CodemodeSummaryView 保存预先投影的分组统计
type CodemodeSummaryView struct {
	Complete bool // 是否收到最终完整快照
	Groups   []CodemodeToolCountView
}

// CodemodeOutputView 保存正文之外的最终元数据
type CodemodeOutputView struct {
	DurationMS     *int64 // 最终耗时，nil 表示未知
	LineCount      int    // 预算裁剪前的输出行数
	FullOutputPath string
}

// CodemodeView 只保留父工具行需要的显示数据
type CodemodeView struct {
	Calls   []CodemodeCallView
	Summary CodemodeSummaryView
	Output  CodemodeOutputView
	Error   string // 父脚本完整错误
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
	// ArgsComplete 表示模型已写完参数，行是否仍在进行由它判断
	ArgsComplete bool
	StartedAt    time.Time
	Codemode     *CodemodeView // 结构化 codemode 显示数据，无详情时为 nil
}

// ToolGroupView 将连续 thinking 和工具调用作为一个可折叠工作块
type ToolGroupView struct {
	ID       string
	Calls    []ToolCallView
	Thinking []ThinkingBlockView
}

// TurnDividerView 标记一个 turn 的最终工作耗时与结束时刻
type TurnDividerView struct {
	Elapsed    time.Duration
	FinishedAt time.Time
}

// Message 是显示投影，ID 在流式更新中保持稳定
type Message struct {
	Revision       uint64
	ID, Role, Text string
	Aborted        bool
	ToolGroup      *ToolGroupView
	Divider        *TurnDividerView
	Skill          *SkillInvocationView
	Images         []ImageView
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
	SteeringInputs   []ComposerPayload
	SteeringRevision uint64
	FollowUp         []string
	FollowUpInputs   []ComposerPayload
	FollowUpRevision uint64
	Subagents        SubagentSnapshot
	InputHistory     *InputHistoryState
}

type queuedKind uint8

const (
	queuedSteering queuedKind = iota
	queuedFollowUp
)

type compactionQueuedMessage struct {
	message ComposerPayload
	kind    queuedKind
}

// ClipboardData 是系统剪贴板的一次读取结果
type ClipboardData struct {
	Text   string
	Images []ImagePart
}

// Request 表示用户发起的一次操作，Kind 为 run/model/thinking/resume/tree/compact/rename/new/reload/clear/delete
type Request struct {
	Kind        RequestKind
	Value       string
	AfterID     string
	ID          uint64
	Payload     *ComposerPayload
	RestoreMode RestoreKind
}

// Handlers 由运行适配器提供，命令在 tea.Cmd 中执行，取消只发信号而不阻塞 Update
type Handlers struct {
	Request func(Request) tea.Cmd
	// Status 查询模型、用量与会话文件，不启动模型 operation
	Status                  func() tea.Cmd
	Cancel                  func()
	EnqueueSteeringMessage  func(ComposerPayload) error
	RestoreSteeringMessages func() []ComposerPayload
	EnqueueFollowUpMessage  func(ComposerPayload) error
	// RestoreFollowUpMessages 原子取出后台尚未消费的 follow-up
	RestoreFollowUpMessages func() []ComposerPayload
	ClearFollowUp           func() []ComposerPayload
	ReadClipboard           func() (string, error)
	ReadClipboardData       func() (ClipboardData, error)
	WriteClipboard          func(string) error
	SubagentRequest         func(SubagentRequest) tea.Cmd
	SubagentSnapshot        func() tea.Cmd
	// ShellList 查询后台 shell,供 /ps 使用
	ShellList func() tea.Cmd
	// ShellStopAll 终止全部运行中的后台 shell,供 /stop 使用
	ShellStopAll func() tea.Cmd
	// ShellKill 终止单个后台 shell,供 /ps 弹窗内按 x 使用
	ShellKill func(id string) tea.Cmd
	History   HistoryHandlers
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
type SteeringStateMsg struct {
	Messages []string
	Inputs   []ComposerPayload
	Revision uint64
}
type QueueStateMsg struct {
	Steering         []string
	SteeringInputs   []ComposerPayload
	SteeringRevision uint64
	FollowUp         []string
	FollowUpInputs   []ComposerPayload
	FollowUpRevision uint64
	Subagents        SubagentSnapshot
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
	EditorMessage   *ComposerPayload
	RestoreEditor   bool
	ResetSessionUI  bool
	ResetTranscript bool
	Quit            bool
	InputHistory    *InputHistoryState
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
	// lines 是 view 的行数，渲染时算一次，避免每帧重新切分
	lines int
	// hits 是这条消息内部的可点击区域，行号相对消息块起点
	hits []toolHit
}

// Model 独占全部显示状态
type Model struct {
	messageIndex map[string]int
	operationID  uint64
	state        Snapshot
	handlers     Handlers
	// panel 承载子任务视图与选择器状态
	panel          subagentPanel
	spinnerRunning bool
	width, height  int
	editor         textarea.Model
	viewport       viewport.Model
	spinner        spinner.Model
	// composer 承载输入框的补全、历史、草稿与粘贴注册表
	composer                 composer
	busy, closing            bool
	initialPrompt            string
	operationIsRun           bool
	steeringRestored         bool
	canceling                bool
	compacting               bool
	compactionQueuedMessages []compactionQueuedMessage
	// backgroundRun 标记当前忙碌态来自通知驱动的后台 run,不是用户操作
	backgroundRun bool
	status        string
	lastCtrlC     time.Time
	flushPending  bool
	cache         map[string]cachedMessage
	markdown      markdownRenderer
	clock         func() time.Time
	// sel 承载鼠标选择与复制提示状态
	sel selection
	// transcript 承载渲染缓存、展开状态与点击命中区间
	transcript transcriptView
	// queues 承载 steering / follow-up 队列镜像
	queues          queueMirror
	statusNoticeSeq uint64
	// shellPanelSeq 是 /stop 受理文案与命令回显的独立序号,避免与 /status 互相覆盖
	shellPanelSeq uint64
	// shellDialog 非空时输入坞被 /ps 弹窗接管,transcript 不动
	shellDialog  *shellDialog
	cursorRow    atomic.Int64
	cursorColumn atomic.Int64
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
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)), clock: time.Now}
	m.composer.images = make(map[int]ImagePart)
	m.transcript.cache = make(map[string]cachedMessage)
	m.transcript.expandedGroups = make(map[string]bool)
	m.transcript.expandedCalls = make(map[string]bool)
	m.transcript.expandedThoughts = make(map[string]bool)
	m.applySnapshot(state)
	return m
}

func (m *Model) Init() tea.Cmd {
	if m.initialPrompt == "" {
		return textarea.Blink
	}
	prompt := m.initialPrompt
	m.initialPrompt = ""
	message := m.recordSubmission(prompt, ComposerPayload{Text: prompt})
	return tea.Batch(textarea.Blink, m.dispatchMessage(RequestRun, prompt, &message))
}

// startSpinner 只在没有活动 tick 链时启动旋转器
func (m *Model) startSpinner() tea.Cmd {
	if m.spinnerRunning {
		return nil
	}
	m.spinnerRunning = true
	return m.spinner.Tick
}

// Update 处理事件，不读取或修改后台运行器的状态
// Update 处理事件。panic 时先转储堆栈再原样抛出,
// 让 Bubble Tea 按原逻辑终止,同时留下可定位的证据
func (m *Model) Update(msg tea.Msg) (_ tea.Model, _ tea.Cmd) {
	defer func() {
		if recovered := recover(); recovered != nil {
			dumpPanic("update", recovered)
			panic(recovered)
		}
	}()
	return m.update(msg)
}

// handleOperationDone 收尾一次操作：复位忙碌态、应用快照与尾部消息、回填编辑器
func (m *Model) handleOperationDone(msg OperationDoneMsg) (tea.Model, tea.Cmd) {
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
			preserveFollowUp := m.queues.followUpRestored
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
	m.installInputHistory(msg.InputHistory)
	m.steeringRestored = false
	m.queues.followUpRestored = false
	if msg.ResetTranscript {
		m.viewport.GotoBottom()
		m.cache = make(map[string]cachedMessage)
		m.transcript.messageHits = nil
	}
	if msg.Tail != nil {
		if m.panel.viewingTaskID != "" {
			m.replaceMainTail(*msg.Tail)
		} else {
			m.replaceTail(*msg.Tail)
		}
	}
	for _, change := range msg.Changes {
		if m.panel.viewingTaskID != "" {
			m.upsertMainMessage(change)
		} else {
			m.upsertMessage(change)
		}
	}
	if msg.RestoreEditor && m.panel.viewingTaskID == "" {
		m.invalidateHistoryInput()
		m.currentHistory().navigation.lastText = nil
		m.resetPastes()
		editorValue := msg.EditorValue
		if msg.EditorMessage != nil {
			editorValue = m.editorTextForPayload(*msg.EditorMessage)
		}
		m.editor.SetValue(editorValue)
		m.editor.CursorEnd()
		m.composer.completion, m.composer.completionHidden = 0, true
	}
	if msg.Err != nil {
		m.notice("Error: " + msg.Err.Error())
	} else if msg.Notice != "" {
		m.notice(msg.Notice)
	}
	m.layout()
	m.refreshTranscript()
	return m, m.startSubagentRefresh()
}

// handleQueueState 应用队列状态快照，撤回过的队列优先于后台推送
func (m *Model) handleQueueState(msg QueueStateMsg) (tea.Model, tea.Cmd) {
	if !m.steeringRestored && msg.SteeringRevision >= m.queues.steeringRevision {
		m.queues.steeringRevision = msg.SteeringRevision
		m.setSteeringState(msg.Steering, msg.SteeringInputs)
	}
	if !m.queues.followUpRestored && msg.FollowUpRevision >= m.queues.followUpRevision {
		m.queues.followUpRevision = msg.FollowUpRevision
		m.setFollowUpState(msg.FollowUp, msg.FollowUpInputs)
	}
	m.layout()
	return m, nil
}

// handleSteeringState 应用只含 steering 的状态推送，旧版本一律丢弃
func (m *Model) handleSteeringState(msg SteeringStateMsg) (tea.Model, tea.Cmd) {
	if msg.Revision == 0 && m.queues.steeringRevision > 0 {
		return m, nil
	}
	if msg.Revision < m.queues.steeringRevision {
		return m, nil
	}
	m.steeringRestored = false
	m.queues.steeringRevision = msg.Revision
	m.setSteeringState(msg.Messages, msg.Inputs)
	m.layout()
	return m, nil
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case HistoryLookupMsg:
		return m, m.handleHistoryLookup(msg)
	case ScopePreparedMsg:
		return m, m.handleScopePrepared(msg)
	case HistoryRecordMsg:
		if msg.Err != nil {
			m.notice("Input history: " + msg.Err.Error())
			if m.handlers.History.Acknowledge != nil {
				m.handlers.History.Acknowledge(msg.Origin)
			}
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.layout()
		m.refreshTranscript()
	case tea.KeyMsg:
		// 裸 Escape 是子任务视图的退出键,不能被鼠标报告过滤器挂起
		if msg.Type == tea.KeyRunes && !msg.Paste && string(msg.Runes) == "\x1b" {
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		}
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
		cmd := m.handleMouse(msg)
		if m.hasSelection() {
			m.invalidateHistoryInput()
		}
		return m, cmd
	case MessageMsg:
		if m.panel.viewingTaskID != "" {
			// 主 Agent 的流式消息属于 main transcript;child 视图只缓存
			m.upsertMainMessage(Message(msg))
			return m, nil
		}
		m.upsertMessage(Message(msg))
		if !m.flushPending {
			m.flushPending = true
			return m, tea.Tick(time.Second/60, func(time.Time) tea.Msg { return flushMsg{} })
		}
	case MessageCommittedMsg:
		if m.panel.viewingTaskID != "" {
			m.replaceMainMessage(msg.PreviousID, msg.Message)
			return m, nil
		}
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
	case ShellsMsg:
		m.updateShellDialog(msg)
		return m, nil
	case ShellKilledMsg:
		if msg.Err != nil {
			m.notice("Failed to stop background shell: " + msg.Err.Error())
			return m, nil
		}
		// 终止成功后立刻重查,弹窗内的状态不等下一轮刷新
		if m.handlers.ShellList != nil {
			return m, m.handlers.ShellList()
		}
		return m, nil
	case ShellsStoppedMsg:
		// 受理文案顶格呈现,与 /ps 面板的标题对齐,不加圆点
		m.shellPanelSeq++
		m.upsertMessage(Message{
			ID:   fmt.Sprintf("shell-stop/%d", m.shellPanelSeq),
			Role: "summary",
			Text: shellStopLine(msg.Stopped, msg.Err),
		})
		m.refreshTranscript()
		// 立刻拉一次快照,提示行的计数不等下一轮轮询
		if m.handlers.SubagentSnapshot != nil {
			return m, m.handlers.SubagentSnapshot()
		}
		return m, nil
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
	case QueueStateMsg:
		return m.handleQueueState(msg)
	case SteeringStateMsg:
		return m.handleSteeringState(msg)
	case BackgroundRunMsg:
		// 通知驱动的 run 由后端自行发起,TUI 没有对应的 Request,
		// 只借用忙碌态让状态栏与输入框边框跟随
		if msg.Running {
			m.busy, m.status = true, "Working"
			m.backgroundRun = true
			m.layout()
			return m, m.startSpinner()
		}
		if m.backgroundRun {
			m.busy, m.status = false, ""
			m.backgroundRun = false
			m.layout()
			m.refreshTranscript()
		}
		return m, nil
	case OperationDoneMsg:
		return m.handleOperationDone(msg)
	case SubagentSnapshotMsg:
		if len(msg.RestoreMessages) > 0 {
			m.restoreSubagentMessages(msg.RestoreMessages)
		}
		// 控制请求可能已经改变任务状态后才返回错误(例如停止一个已完成任务)。
		// 有 revision 的错误响应仍然带着最新快照,不能让旧的 running 行永久滞留。
		snapshotApplied := msg.Snapshot.Revision >= m.state.Subagents.Revision && (msg.Err == nil || msg.Snapshot.Revision > 0)
		if snapshotApplied {
			m.applySubagentSnapshot(msg.Snapshot)
			if msg.InputHistory != nil {
				if msg.Snapshot.ViewingTaskID != m.panel.viewingTaskID {
					if msg.Snapshot.ViewingTaskID == "" {
						m.exitSubagentView()
					} else {
						m.enterSubagentView(msg.Snapshot.ViewingTaskID)
					}
				}
				m.installInputHistory(msg.InputHistory)
			}
		}
		if msg.Err != nil {
			if msg.RestoreMessage != nil && m.panel.viewingTaskID != "" {
				m.restoreEditorPayload(msg.RestoreMessage, "")
			}
			m.notice("Subagent view unavailable: " + msg.Err.Error())
			m.layout()
			if snapshotApplied {
				return m, m.startSubagentRefresh()
			}
		} else if snapshotApplied {
			return m, m.startSubagentRefresh()
		}
	case subagentRefreshMsg:
		m.panel.refreshRunning = false
		// 弹窗只依赖 ShellList,不要求存在子任务快照通道
		if m.shellDialog != nil && m.handlers.ShellList != nil {
			return m, tea.Batch(m.handlers.ShellList(), subagentRefreshTick())
		}
		if m.handlers.SubagentSnapshot == nil || !m.needsSubagentRefresh() {
			return m, nil
		}
		// 唯一所有者:只有本 tick 链续接自己,pushed snapshot 不得再开第二条链
		m.panel.refreshRunning = true
		cmds := []tea.Cmd{m.startSpinner(), subagentRefreshTick()}
		if m.handlers.SubagentSnapshot != nil {
			cmds = append(cmds, m.handlers.SubagentSnapshot())
		}
		// 弹窗打开期间同一 tick 顺带重查 shell,运行时长与输出跟着走
		if m.shellDialog != nil && m.handlers.ShellList != nil {
			cmds = append(cmds, m.handlers.ShellList())
		}
		return m, tea.Batch(cmds...)
	case spinner.TickMsg:
		if m.busy || hasRunningSubagent(m.state.Subagents.Tasks) {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			if m.busy {
				m.refreshTranscript()
			}
			return m, cmd
		}
		m.spinnerRunning = false
	case clipboardReadMsg:
		if msg.err != nil {
			m.notice("Clipboard paste failed: " + msg.err.Error())
		} else {
			m.invalidateHistoryInput()
			m.composer.histories.lookup.editorRevision++
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
		m.sel.noticeSeq++
		sequence := m.sel.noticeSeq
		m.sel.notice = true
		return m, tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return clearCopyNoticeMsg(sequence) })
	case clearCopyNoticeMsg:
		if uint64(msg) == m.sel.noticeSeq {
			m.sel.notice = false
		}
	default:
		if m.composer.selector != nil {
			before := m.composer.selector.input.Value()
			var cmd tea.Cmd
			m.composer.selector.input, cmd = m.composer.selector.input.Update(msg)
			if before != m.composer.selector.input.Value() {
				m.composer.selector.filter()
				m.layout()
			}
			return m, cmd
		}
		before := m.editor.Value()
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		if before != m.editor.Value() {
			m.composer.completion, m.composer.completionHidden = 0, false
			m.layout()
		}
		return m, cmd
	}
	return m, nil
}

func (m *Model) applySnapshot(s Snapshot) {
	viewingTaskID := m.panel.viewingTaskID
	m.applyMetadata(s)
	m.steeringRestored = false
	// 子任务视图里 transcript 属于 child;主 transcript 先缓存起来,
	// 退出视图时再恢复
	m.panel.mainMessages = cloneMessages(s.Messages)
	if viewingTaskID != "" {
		if task, ok := findSubagentTask(s.Subagents.Tasks, viewingTaskID); ok {
			m.state.Messages = cloneMessages(task.Messages)
			m.rebuildMessageIndex()
			return
		}
		m.exitSubagentView()
	}
	m.messageIndex = make(map[string]int, len(s.Messages))
	for i, message := range s.Messages {
		m.messageIndex[message.ID] = i
	}
	m.state.Messages = append([]Message(nil), s.Messages...)
	for i := range m.state.Messages {
		m.state.Messages[i].ToolGroup = m.state.Messages[i].ToolGroup.Clone()
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

func (m *Model) dispatch(kind RequestKind, value string) tea.Cmd {
	return m.dispatchMessage(kind, value, nil)
}

func (m *Model) dispatchMessage(kind RequestKind, value string, message *ComposerPayload) tea.Cmd {
	if m.busy {
		if kind == RequestTree {
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
	// run 期间子任务可能被拉起,刷新链必须同时启动,
	// 否则任务行要等 operation 结束才出现,看起来像被忙碌态吞掉
	return tea.Batch(m.spinner.Tick, m.handlers.Request(Request{ID: m.operationID, AfterID: m.lastMessageID(), Kind: kind, Value: value, Payload: message}), m.startSubagentRefresh())
}

func (m *Model) dispatchSteeringMessage(value string, message *ComposerPayload) tea.Cmd {
	if !m.busy {
		return nil
	}
	if message != nil && m.handlers.EnqueueSteeringMessage != nil {
		if err := m.handlers.EnqueueSteeringMessage(*message); err != nil {
			m.restoreEditorPayload(message, value)
			m.composer.completion, m.composer.completionHidden = 0, true
			m.notice("Error: " + err.Error())
			m.layout()
			return nil
		}
		m.steeringRestored = false
		m.queues.steeringRevision++
		m.appendSteeringState(value, message, false)
		if !m.canceling && !m.compacting {
			m.status = "Working"
		}
		m.layout()
		return nil
	}
	return nil
}

func (m *Model) dispatchFollowUpMessage(value string, message *ComposerPayload) tea.Cmd {
	if !m.busy {
		return nil
	}
	if message != nil && m.handlers.EnqueueFollowUpMessage != nil {
		if err := m.handlers.EnqueueFollowUpMessage(*message); err != nil {
			m.restoreEditorPayload(message, value)
			m.composer.completion, m.composer.completionHidden = 0, true
			m.notice("Error: " + err.Error())
			m.layout()
			return nil
		}
		m.queues.followUpRestored = false
		m.queues.followUpRevision++
		m.appendFollowUpState(value, message, false)
		if !m.canceling && !m.compacting {
			m.status = "Working"
		}
		m.layout()
		return nil
	}
	return nil
}

func textPayload(value string) ComposerPayload {
	return ComposerPayload{Text: value}
}

func (m *Model) enqueueCompactionMessage(item compactionQueuedMessage) error {
	message := item.message
	if item.kind == queuedSteering {
		if m.handlers.EnqueueSteeringMessage == nil {
			return fmt.Errorf("steering queue is unavailable")
		}
		return m.handlers.EnqueueSteeringMessage(message)
	}
	if m.handlers.EnqueueFollowUpMessage == nil {
		return fmt.Errorf("follow-up queue is unavailable")
	}
	return m.handlers.EnqueueFollowUpMessage(message)
}

func payloadText(payload ComposerPayload) string {
	return payload.Text
}

func (m *Model) takeCompactionMessages(kind queuedKind) []ComposerPayload {
	var selected []ComposerPayload
	remaining := make([]compactionQueuedMessage, 0, len(m.compactionQueuedMessages))
	for _, item := range m.compactionQueuedMessages {
		if item.kind == kind {
			selected = append(selected, clonePayload(item.message))
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
		value := m.editorTextForPayload(item.message)
		message := item.message
		if item.kind == queuedSteering {
			m.steeringRestored = false
			m.queues.steeringRevision++
			m.appendSteeringState(value, &message, false)
		} else {
			m.queues.followUpRestored = false
			m.queues.followUpRevision++
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
	m.invalidateHistoryInput()
	m.currentHistory().navigation.lastText = nil
	var currentMessage *ComposerPayload
	if message, ok := m.buildPayload(); ok {
		copy := clonePayload(message)
		currentMessage = &copy
	}
	// 先清空 paste/image 表，再生成 marker，保证回填后的图片仍能提交
	m.resetPastes()
	parts := make([]string, 0, len(queued)+1)
	for _, item := range queued {
		parts = append(parts, m.editorTextForPayload(item.message))
	}
	if currentMessage != nil {
		current = m.editorTextForPayload(*currentMessage)
	}
	if strings.TrimSpace(current) != "" {
		parts = append(parts, current)
	}
	m.compactionQueuedMessages = nil
	m.editor.SetValue(strings.Join(parts, "\n\n"))
	m.editor.CursorEnd()
	m.composer.completion, m.composer.completionHidden = 0, true
	m.layout()
}

func (m *Model) appendSteeringState(value string, message *ComposerPayload, deduplicate bool) {
	if message == nil {
		text := textPayload(value)
		message = &text
	}
	if len(m.state.SteeringInputs) != len(m.state.Steering) {
		m.state.SteeringInputs = make([]ComposerPayload, len(m.state.Steering))
		for i, existing := range m.state.Steering {
			m.state.SteeringInputs[i] = textPayload(existing)
		}
	}
	if deduplicate && len(m.state.Steering) > 0 {
		last := len(m.state.Steering) - 1
		if m.state.Steering[last] == value && reflect.DeepEqual(m.state.SteeringInputs[last], *message) {
			return
		}
	}
	m.state.Steering = append(m.state.Steering, value)
	m.state.SteeringInputs = append(m.state.SteeringInputs, clonePayload(*message))
	m.state.SteeringRevision = m.queues.steeringRevision
}

func (m *Model) setSteeringState(messages []string, inputs []ComposerPayload) {
	m.state.Steering = append([]string(nil), messages...)
	if len(inputs) == len(messages) {
		m.state.SteeringInputs = clonePayloads(inputs)
		m.state.SteeringRevision = m.queues.steeringRevision
		return
	}
	m.state.SteeringInputs = make([]ComposerPayload, len(messages))
	for i, message := range messages {
		m.state.SteeringInputs[i] = textPayload(message)
	}
	m.state.SteeringRevision = m.queues.steeringRevision
}

func (m *Model) appendFollowUpState(value string, message *ComposerPayload, deduplicate bool) {
	if message == nil {
		text := textPayload(value)
		message = &text
	}
	if len(m.state.FollowUpInputs) != len(m.state.FollowUp) {
		m.state.FollowUpInputs = make([]ComposerPayload, len(m.state.FollowUp))
		for i, existing := range m.state.FollowUp {
			m.state.FollowUpInputs[i] = textPayload(existing)
		}
	}
	if deduplicate && len(m.state.FollowUp) > 0 {
		last := len(m.state.FollowUp) - 1
		if m.state.FollowUp[last] == value && reflect.DeepEqual(m.state.FollowUpInputs[last], *message) {
			return
		}
	}
	m.state.FollowUp = append(m.state.FollowUp, value)
	m.state.FollowUpInputs = append(m.state.FollowUpInputs, clonePayload(*message))
	m.state.FollowUpRevision = m.queues.followUpRevision
}

func (m *Model) setFollowUpState(messages []string, inputs []ComposerPayload) {
	m.state.FollowUp = append([]string(nil), messages...)
	if len(inputs) == len(messages) {
		m.state.FollowUpInputs = clonePayloads(inputs)
		m.state.FollowUpRevision = m.queues.followUpRevision
		return
	}
	m.state.FollowUpInputs = make([]ComposerPayload, len(messages))
	for i, message := range messages {
		m.state.FollowUpInputs[i] = textPayload(message)
	}
	m.state.FollowUpRevision = m.queues.followUpRevision
}

// restoreQueuedSteering 把 steering 按顺序放回编辑器，并原子清空后台队列
func (m *Model) restoreQueuedSteering() int {
	if m.handlers.RestoreSteeringMessages != nil || len(m.state.SteeringInputs) > 0 {
		var queued []ComposerPayload
		backendQueue := m.handlers.RestoreSteeringMessages != nil
		if m.handlers.RestoreSteeringMessages != nil {
			queued = m.handlers.RestoreSteeringMessages()
		} else {
			queued = clonePayloads(m.state.SteeringInputs)
		}
		if len(queued) == 0 && !backendQueue {
			if len(m.state.SteeringInputs) == len(m.state.Steering) && len(m.state.SteeringInputs) > 0 {
				queued = clonePayloads(m.state.SteeringInputs)
			} else if len(m.state.Steering) > 0 {
				queued = make([]ComposerPayload, len(m.state.Steering))
				for i, value := range m.state.Steering {
					queued[i] = textPayload(value)
				}
			}
		}
		if len(queued) == 0 {
			m.layout()
			return 0
		}
		m.state.Steering = nil
		m.state.SteeringInputs = nil
		m.queues.steeringRevision++
		m.state.SteeringRevision = m.queues.steeringRevision
		m.steeringRestored = true
		current := m.editor.Value()
		var currentMessage *ComposerPayload
		if message, ok := m.buildPayload(); ok {
			copy := clonePayload(message)
			currentMessage = &copy
		}
		m.resetPastes()
		var parts []string
		for _, message := range queued {
			parts = append(parts, m.editorTextForPayload(message))
		}
		queuedText := strings.Join(parts, "\n\n")
		if currentMessage != nil {
			current = m.editorTextForPayload(*currentMessage)
		}
		if strings.TrimSpace(current) != "" {
			queuedText += "\n\n" + current
		}
		m.editor.SetValue(queuedText)
		m.editor.CursorEnd()
		m.composer.completion, m.composer.completionHidden = 0, true
		m.layout()
		return len(queued)
	}
	queued := append([]string(nil), m.state.Steering...)
	m.state.Steering = nil
	if len(queued) == 0 {
		m.layout()
		return 0
	}
	m.queues.steeringRevision++
	m.state.SteeringRevision = m.queues.steeringRevision
	m.steeringRestored = true
	queuedText := strings.Join(queued, "\n\n")
	current := m.editor.Value()
	if strings.TrimSpace(current) != "" {
		queuedText += "\n\n" + current
	}
	m.editor.SetValue(queuedText)
	m.editor.CursorEnd()
	m.composer.completion, m.composer.completionHidden = 0, true
	m.resetPastes()
	m.layout()
	return len(queued)
}

func (m *Model) takeFollowUpForRestore() []ComposerPayload {
	var queued []ComposerPayload
	if m.handlers.RestoreFollowUpMessages != nil {
		queued = m.handlers.RestoreFollowUpMessages()
	} else if len(m.state.FollowUpInputs) == len(m.state.FollowUp) && len(m.state.FollowUpInputs) > 0 {
		queued = clonePayloads(m.state.FollowUpInputs)
	} else {
		queued = make([]ComposerPayload, len(m.state.FollowUp))
		for i, value := range m.state.FollowUp {
			queued[i] = textPayload(value)
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
	m.queues.followUpRevision++
	m.state.FollowUpRevision = m.queues.followUpRevision
	m.queues.followUpRestored = true
	current := m.editor.Value()
	var currentMessage *ComposerPayload
	if message, ok := m.buildPayload(); ok {
		copy := clonePayload(message)
		currentMessage = &copy
	}
	// 先清空 paste/image 表，再生成 marker，保证回填后的图片仍能提交
	m.resetPastes()
	parts := make([]string, 0, len(queued)+1)
	for _, message := range queued {
		parts = append(parts, m.editorTextForPayload(message))
	}
	if currentMessage != nil {
		current = m.editorTextForPayload(*currentMessage)
	}
	if strings.TrimSpace(current) != "" {
		parts = append(parts, current)
	}
	m.editor.SetValue(strings.Join(parts, "\n\n"))
	m.editor.CursorEnd()
	m.composer.completion, m.composer.completionHidden = 0, true
	m.layout()
	return len(queued)
}

func (m *Model) restoreQueuedMessages() int {
	var steering []ComposerPayload
	if m.handlers.RestoreSteeringMessages != nil {
		steering = m.handlers.RestoreSteeringMessages()
	} else if len(m.state.SteeringInputs) == len(m.state.Steering) {
		steering = clonePayloads(m.state.SteeringInputs)
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
	m.queues.steeringRevision++
	m.queues.followUpRevision++
	m.state.SteeringRevision = m.queues.steeringRevision
	m.state.FollowUpRevision = m.queues.followUpRevision
	m.steeringRestored = true
	m.queues.followUpRestored = true
	current := m.editor.Value()
	var currentMessage *ComposerPayload
	if message, ok := m.buildPayload(); ok {
		copy := clonePayload(message)
		currentMessage = &copy
	}
	// 先清空 paste/image 表，再生成 marker，保证回填后的图片仍能提交
	m.resetPastes()
	parts := make([]string, 0, len(steering)+len(followUp)+1)
	for _, message := range steering {
		parts = append(parts, m.editorTextForPayload(message))
	}
	if len(steering) > 0 && len(followUp) > 0 {
		parts = append(parts, "")
	}
	for _, message := range followUp {
		parts = append(parts, m.editorTextForPayload(message))
	}
	if currentMessage != nil {
		current = m.editorTextForPayload(*currentMessage)
	}
	if strings.TrimSpace(current) != "" {
		parts = append(parts, "", current)
	}
	m.editor.SetValue(strings.Join(parts, "\n\n"))
	m.editor.CursorEnd()
	m.composer.completion, m.composer.completionHidden = 0, true
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
	m.queues.followUpRevision++
	m.state.FollowUpRevision = m.queues.followUpRevision
	m.queues.followUpRestored = true
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
	m.composer.histories = composerHistories{}
	m.composer.completion = 0
	m.composer.completionHidden = true
	m.composer.selector = nil
	m.shellDialog = nil
	// 跨会话必须清空面板状态,否则 viewingTaskID 与焦点意图会泄漏
	m.panel = subagentPanel{}
	m.editor.Focus()
	m.transcript.showThinking, m.transcript.showAllTools, m.transcript.hideTools = false, false, false
	m.clearSelection()
	m.cache = make(map[string]cachedMessage)
	m.transcript.messageHits = nil
	m.queues.steeringRevision = 0
	m.state.Steering = nil
	m.state.SteeringInputs = nil
	m.state.SteeringRevision = 0
	m.queues.followUpRestored = false
	m.queues.followUpRevision = 0
	m.state.FollowUp = nil
	m.state.FollowUpInputs = nil
	m.state.FollowUpRevision = 0
	m.sel.noticeSeq++
	m.sel.notice = false
	m.lastCtrlC = time.Time{}
	m.viewport.GotoTop()
}

// refreshTranscript 只重渲染内容、宽度、思考与工具的展开/遮罩状态变化的消息
func (m *Model) refreshTranscript() {
	if m.width == 0 {
		return
	}
	follow := m.viewport.AtBottom()
	// 展开状态对所有消息相同，每帧只算一次，避免逐消息重复分配与排序
	expanded := m.expandSignature()
	var blocks []string
	// 逐消息渲染，并把消息内相对行号的命中区间换算成屏幕行
	var screenHits []toolHit
	blockOffset := 0
	changed := len(m.state.Messages) != m.transcript.count
	for _, message := range m.state.Messages {
		cached, ok := m.cache[message.ID]
		animate := false
		if m.busy && message.ToolGroup != nil {
			animate = groupRunning(message.ToolGroup.Calls)
		}
		if animate || !ok || cached.revision != message.Revision || cached.width != m.width || cached.showThinking != m.transcript.showThinking || cached.allTools != m.transcript.showAllTools || cached.hidden != m.transcript.hideTools || cached.expanded != expanded {
			changed = true
			m.transcript.messageHits = nil
			view := m.renderMessage(message)
			cached = cachedMessage{revision: message.Revision, width: m.width, showThinking: m.transcript.showThinking, allTools: m.transcript.showAllTools, hidden: m.transcript.hideTools, expanded: expanded, view: view, lines: strings.Count(view, "\n") + 1, hits: m.transcript.messageHits}
			m.cache[message.ID] = cached
		}
		for _, hit := range cached.hits {
			screenHits = append(screenHits, toolHit{Kind: hit.Kind, Key: hit.Key, Start: hit.Start + blockOffset, End: hit.End + blockOffset})
		}
		blocks = append(blocks, cached.view)
		// 消息之间用一个空行分隔，下一块的起点因此比本块末行多一行
		blockOffset += cached.lines + 1
	}
	m.transcript.hits = screenHits
	m.trimRenderCache()
	// 没有消息重绘且宽度不变时，拼好的内容与上一帧相同，跳过 Join 与 SetContent
	// （流式期间 spinner 每帧触发刷新，这部分是主要开销）
	if changed || m.transcript.width != m.width {
		m.transcript.width = m.width
		m.viewport.SetContent(strings.Join(blocks, "\n\n"))
	}
	m.transcript.count = len(m.state.Messages)
	if follow {
		m.viewport.GotoBottom()
	}
}

// renderCacheLimit 是渲染缓存保留的消息条数上限。
// 缓存只服务于可见窗口与邻近消息，远端条目淘汰后按需重建
const renderCacheLimit = 400

// trimRenderCache 在缓存超过上限时丢弃离当前视口最远的一半，
// 避免长会话把每一帧的渲染结果都留在内存里
func (m *Model) trimRenderCache() {
	if len(m.cache) <= renderCacheLimit {
		return
	}
	// 视口附近的条目保留，其余按消息顺序从最早开始丢弃
	keepFrom := max(len(m.state.Messages)-renderCacheLimit/2, 0)
	kept := make(map[string]cachedMessage, len(m.cache))
	for index := keepFrom; index < len(m.state.Messages); index++ {
		id := m.state.Messages[index].ID
		if cached, ok := m.cache[id]; ok {
			kept[id] = cached
		}
	}
	m.cache = kept
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
	steeringInputs := clonePayloads(m.state.SteeringInputs)
	steeringRevision := m.queues.steeringRevision
	followUp := append([]string(nil), m.state.FollowUp...)
	followUpInputs := clonePayloads(m.state.FollowUpInputs)
	followUpRevision := m.queues.followUpRevision
	m.state = s
	m.state.Messages = messages
	m.state.Subagents = cloneSubagentSnapshot(s.Subagents)
	// 整份快照替换后必须收敛焦点:列表可能被清空而 taskSelector 仍为真
	m.reconcilePanelFocus()
	m.state.Sessions = append([]SessionOption(nil), s.Sessions...)
	m.state.Tree = append([]TreeNode(nil), s.Tree...)
	m.state.Models = append([]ModelOption(nil), s.Models...)
	m.state.Skills = append([]SkillOption(nil), s.Skills...)
	if steeringRevision == 0 || s.SteeringRevision >= steeringRevision {
		m.state.Steering = append([]string(nil), s.Steering...)
		m.state.SteeringInputs = clonePayloads(s.SteeringInputs)
		m.state.SteeringRevision = s.SteeringRevision
		m.queues.steeringRevision = s.SteeringRevision
	} else {
		m.state.Steering = steering
		m.state.SteeringInputs = steeringInputs
		m.state.SteeringRevision = steeringRevision
		m.queues.steeringRevision = steeringRevision
	}
	if followUpRevision == 0 || s.FollowUpRevision >= followUpRevision {
		m.state.FollowUp = append([]string(nil), s.FollowUp...)
		m.state.FollowUpInputs = clonePayloads(s.FollowUpInputs)
		m.state.FollowUpRevision = s.FollowUpRevision
		m.queues.followUpRevision = s.FollowUpRevision
	} else {
		m.state.FollowUp = followUp
		m.state.FollowUpInputs = followUpInputs
		m.state.FollowUpRevision = followUpRevision
		m.queues.followUpRevision = followUpRevision
	}
	m.composer.completion = 0
	for i := range m.state.Models {
		m.state.Models[i].Levels = append([]string(nil), s.Models[i].Levels...)
	}
	m.installInputHistory(s.InputHistory)
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

func clonePayload(payload ComposerPayload) ComposerPayload {
	payload.Images = append([]ImagePart(nil), payload.Images...)
	return payload
}

func clonePayloads(messages []ComposerPayload) []ComposerPayload {
	if len(messages) == 0 {
		return nil
	}
	out := make([]ComposerPayload, len(messages))
	for i, message := range messages {
		out[i] = clonePayload(message)
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

// BackgroundRunMsg 由适配器在通知驱动的后台 run 开始或结束时投递
type BackgroundRunMsg struct {
	Running bool
}
