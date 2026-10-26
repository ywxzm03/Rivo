package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// Runtime 把 app 的同步事件投影成 UI 消息，不把 Agent 或 SessionManager 暴露给 TUI
type Runtime struct {
	mu sync.Mutex

	app    *app.App
	cfg    provider.Config
	send   func(tea.Msg)
	cwd    string
	branch string

	lastAssistantIndex     int
	hasToolGroup           bool
	namespace              string
	messageIndex           map[string]int
	resetNeeded            bool
	operationLastAssistant int
	operationHadToolGroup  bool
	operationStart         int
	operationEntries       []ctxview.ContextEntry
	messages               []tui.Message
	sequence               int
	userID                 string
	userMessages           int
	assistantTurn          int
	currentAssistantID     string
	activeToolAnchorID     string
	toolStarted            map[string]time.Time
	turnStarted            time.Time
	turnRetrying           bool
	warningSeq             uint64
	// operationActive 标记当前 run 由用户请求驱动,TUI 自己已置忙碌态
	operationActive bool
	// externalRunSeen 标记已经为后台 run 投递过开始消息,避免重复
	externalRunSeen bool
	// compactionNotice 只供手动压缩完成时投影到 TUI，不进入会话上下文
	compactionNotice    *tui.Message
	compactionNoticeSeq uint64
	pendingSkillInput   *skillInput
	// subagentTranscripts 缓存每个被查看任务已投影的 transcript 前缀,
	// 完整 transcript 始终常驻磁盘。它由独立锁保护,磁盘读不阻塞事件投影
	subagentMu          sync.Mutex
	subagentRevision    uint64
	subagentTranscripts map[string]*transcriptCache
}

// subagentSnapshotLocked 投影子任务面板。app 指针在主锁下取,
// transcript 投影在独立的 subagentMu 下进行
func (r *Runtime) subagentSnapshotLocked() tui.SubagentSnapshot {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return tui.SubagentSnapshot{}
	}
	store := activeApp.SubagentStore()
	if store == nil {
		return tui.SubagentSnapshot{}
	}
	r.subagentMu.Lock()
	defer r.subagentMu.Unlock()
	viewingTaskID := store.ViewingTaskID()
	tasks := r.projectSubagentTasks(store, store.Snapshot(), viewingTaskID)
	r.subagentRevision++
	return tui.SubagentSnapshot{Tasks: tasks, ViewingTaskID: viewingTaskID, Revision: r.subagentRevision}
}

func (r *Runtime) SetProgram(program *tea.Program) {
	r.mu.Lock()
	r.send = program.Send
	r.mu.Unlock()
}

// enqueueRequest 处理只入队的请求：steering 与 follow-up 共用同一条路径，
// 只有队列种类、错误值与结果消息类型不同
func (r *Runtime) enqueueRequest(req tui.Request, activeApp *app.App, message *llm.UserMessage, editorMessage *tui.ComposerPayload) tea.Cmd {
	steering := req.Kind == tui.RequestSteer
	kind, unavailable := agent.QueueFollowUp, app.ErrFollowUpUnavailable
	if steering {
		kind, unavailable = agent.QueueSteering, app.ErrSteeringUnavailable
	}
	var err error
	var revision uint64
	if activeApp == nil {
		err = unavailable
	} else {
		if message != nil {
			err = activeApp.Enqueue(kind, agent.PayloadFromMessage(*message))
		} else if steering {
			err = activeApp.EnqueueSteering(req.Value)
		} else {
			err = activeApp.EnqueueFollowUp(req.Value)
		}
		if err == nil {
			revision = activeApp.QueueRevision(kind)
		}
	}
	return func() tea.Msg {
		if steering {
			return tui.SteeringResultMsg{ID: req.ID, Value: req.Value, Message: editorMessage, Revision: revision, Err: err}
		}
		return tui.FollowUpResultMsg{ID: req.ID, Value: req.Value, Message: editorMessage, Revision: revision, Err: err}
	}
}

// runOperation 启动 operation 并把结果投影成 TUI 消息
func (r *Runtime) runOperation(req tui.Request, activeApp *app.App, value string, message *llm.UserMessage, editorMessage *tui.ComposerPayload, previous operationSnapshot) tea.Cmd {
	op, err := activeApp.Start(context.Background(), app.OperationRequest{Kind: operationKind(req.Kind), Value: value, Message: message, RestoreMode: restoreMode(req.RestoreMode)})
	if err != nil {
		r.mu.Lock()
		r.operationActive = false
		r.mu.Unlock()
		return func() tea.Msg {
			return tui.OperationDoneMsg{ID: req.ID, Err: err, EditorValue: userMessageEditorTextOrEmpty(message), EditorMessage: editorMessage, RestoreEditor: message != nil}
		}
	}
	return func() tea.Msg {
		result := op.Wait()
		r.mu.Lock()
		r.operationActive = false
		var commitErr *session.CommitError
		if errors.As(result.Err, &commitErr) {
			if req.Kind == tui.RequestRun && strings.HasPrefix(req.Value, "/skill:") {
				r.pendingSkillInput = &skillInput{raw: req.Value, expanded: value}
			}
		} else {
			r.pendingSkillInput = nil
		}
		if result.Exit && result.Err == nil {
			r.mu.Unlock()
			return tui.OperationDoneMsg{ID: req.ID, Quit: true}
		}
		reset := result.Next != nil || r.resetNeeded || result.Replayed || (req.Kind == tui.RequestTree && result.ContextRewound)
		if req.Kind == tui.RequestClear && result.Err == nil {
			reset = true
		}
		if result.Next != nil {
			r.app = result.Next
			r.namespace = result.Next.SessionID()
			// 新会话是另一个 App,后台 run 的结束兜底必须重新挂上
			r.attachBackgroundHookLocked(result.Next)
			r.messages = nil
			r.pendingSkillInput = nil
			if req.Kind == tui.RequestNew {
				r.operationEntries = nil
				r.sequence, r.userMessages, r.assistantTurn = 0, 0, 0
				r.userID = ""
				r.toolStarted = nil
				r.turnStarted, r.turnRetrying = time.Time{}, false
			}
		}
		if reset {
			r.replaceMessagesLocked(r.app.ContextView())
			r.resetNeeded = false
			if req.Kind == tui.RequestClear {
				r.resetProjectionLocked()
			}
		}
		var tail *tui.MessageTail
		if req.Kind == tui.RequestRun && !reset {
			// 完成时只重放本次操作，保留历史重放的 thinking/工具分组显示规则
			old := r.messages[r.operationStart:]
			projected := r.projectContext(ctxview.ContextView{Entries: r.operationEntries})
			projected = preserveDisplayMetadata(projected, old)
			if partial := r.partialAssistantLocked(); partial != nil {
				found := false
				for _, message := range projected {
					if message.ID == partial.ID {
						found = true
						break
					}
				}
				if !found {
					projected = append(projected, *partial)
				}
			}
			for _, message := range old {
				delete(r.messageIndex, message.ID)
			}
			r.messages = r.messages[:r.operationStart]
			r.lastAssistantIndex, r.hasToolGroup = r.operationLastAssistant, r.operationHadToolGroup
			for _, message := range projected {
				r.upsertLocked(message)
			}
			r.activeToolAnchorID, r.currentAssistantID = "", ""
			tail = &tui.MessageTail{AfterID: req.AfterID, Messages: projected}
		}
		var changes []tui.Message
		if index := r.messageIndexLocked(r.activeToolAnchorID); index >= 0 {
			r.sealToolGroupLocked()
			changes = append(changes, r.messages[index])
		}
		// 手动压缩不产生普通消息，完成后追加可见提示
		if req.Kind == tui.RequestCompact && result.Err == nil && r.compactionNotice != nil {
			changes = append(changes, *r.compactionNotice)
		}
		r.compactionNotice = nil
		snapshot := r.snapshotLocked(reset)

		r.mu.Unlock()
		snapshot.Sessions = listSessionOptions(snapshot.Cwd)
		snapshot.Subagents = r.subagentSnapshotLocked()
		if errors.Is(result.Err, context.Canceled) {
			result.Err = nil
		}
		var notice string
		switch req.Kind {
		case tui.RequestRename:
			if result.Err == nil && snapshot.SessionName != previous.Name {
				notice = "Thread name changed to " + snapshot.SessionName
			}
		case tui.RequestModel:
			if result.Err == nil && snapshot.CurrentModel.Key() != previous.Model {
				notice = "Model changed to " + snapshot.CurrentModel.ID
			}
		case tui.RequestThinking:
			if result.Err == nil && snapshot.Thinking != previous.Thinking {
				notice = "Thinking changed to " + snapshot.Thinking
			}
		case tui.RequestNew:
			if result.Err == nil {
				notice = "New session started"
			}
		case tui.RequestReload:
			if result.Err == nil {
				notice = "Reloaded context files and skills"
			}
		case tui.RequestTree:
			if result.FileRestore != nil {
				summary := fileRestoreSummary(result.FileRestore)
				if result.Err != nil {
					result.Err = fmt.Errorf("%w (%s)", result.Err, summary)
				} else {
					notice = "Restored code: " + strings.TrimPrefix(summary, "code restore: ")
				}
			}
		}
		restoreEditor := result.RestoreEditor
		editorValue := result.EditorText
		editorMessage := payloadFromMessage(result.EditorMessage)
		if result.RestoreEditor && result.EditorMessage != nil {
			editorValue = userMessageEditorText(*result.EditorMessage)
		}
		if result.Err != nil && message != nil {
			restoreEditor = true
			editorValue = userMessageEditorText(*message)
			editorMessage = payloadFromMessage(message)
		}
		return tui.OperationDoneMsg{ID: req.ID, Snapshot: &snapshot, RetainMessages: !reset, Tail: tail, Changes: changes, Err: result.Err, Notice: notice, EditorValue: editorValue, EditorMessage: editorMessage, RestoreEditor: restoreEditor, ResetSessionUI: req.Kind == tui.RequestNew && result.Next != nil, ResetTranscript: req.Kind == tui.RequestClear && result.Err == nil}
	}
}

func (r *Runtime) request(req tui.Request) tea.Cmd {
	r.mu.Lock()
	activeApp := r.app
	r.compactionNotice = nil
	r.mu.Unlock()
	// 编辑器产出在这里统一转成领域消息，TUI 侧不再持有 llm 类型
	message := payloadMessage(req.Payload)
	editorMessage := payloadFromMessage(message)

	if req.Kind == tui.RequestSteer || req.Kind == tui.RequestFollowUp {
		return r.enqueueRequest(req, activeApp, message, editorMessage)
	}
	value := req.Value
	if req.Kind == tui.RequestRun {
		var err error
		value, err = r.expandSkillInput(activeApp, value)
		if err != nil {
			return func() tea.Msg {
				return tui.OperationDoneMsg{ID: req.ID, Err: err, EditorValue: userMessageEditorTextOrEmpty(message), EditorMessage: editorMessage, RestoreEditor: message != nil}
			}
		}
	}
	// 记录操作前设置，完成后只在值真正变化时输出提示
	previousModel, previousThinking, previousName := "", "", ""
	if activeApp != nil {
		switch req.Kind {
		case tui.RequestModel:
			model := activeApp.CurrentModel()
			previousModel = model.Provider + "/" + model.ID
		case tui.RequestThinking:
			previousThinking = string(activeApp.CurrentThinkingLevel())
			if previousThinking == "" {
				previousThinking = string(llm.ThinkingLevelOff)
			}
		case tui.RequestRename:
			previousName = activeApp.SessionName()
		}
	}
	r.mu.Lock()
	// 用户请求驱动的 run 由 TUI 自己置忙碌态,事件侧不再补 BackgroundRunMsg
	r.operationActive = true
	if req.Kind == tui.RequestRun {
		r.messageIndexLocked("")
		r.operationLastAssistant, r.operationHadToolGroup = r.lastAssistantIndex, r.hasToolGroup
		r.operationStart = len(r.messages)
		r.operationEntries = nil
		r.sequence++
		r.userID = fmt.Sprintf("%s/live/%d/user", r.namespace, r.sequence)
		r.userMessages = 0
		r.currentAssistantID, r.activeToolAnchorID = "", ""
		r.assistantTurn = 0
		r.toolStarted = make(map[string]time.Time)
		r.turnStarted = time.Time{}
		r.turnRetrying = false
	}
	r.mu.Unlock()
	return r.runOperation(req, activeApp, value, message, editorMessage, operationSnapshot{Model: previousModel, Thinking: previousThinking, Name: previousName})
}

func fileRestoreSummary(result *filehistory.RestoreResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("code restore: %d changed, %d skipped, %d failed", len(result.FilesChanged), len(result.FilesSkipped), len(result.FilesFailed))
}

func (r *Runtime) cancelCurrent() {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	activeApp.Cancel()
}

func (r *Runtime) waitCurrent() {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	activeApp.Wait()
}

// projectMessageEnd 把一条已提交消息投影成 TUI 消息，
// 并在来源 ID 存在时重命名，返回新的消息与需要替换的旧 ID
func (r *Runtime) projectMessageEnd(event llm.MessageEndEvent) (*tui.Message, string) {
	var message *tui.Message
	var previousID string
	if event.SourceID != "" {
		r.operationEntries = append(r.operationEntries, ctxview.ContextEntry{SourceID: event.SourceID, Kind: ctxview.ContextMessage, Messages: []llm.Message{event.Message}})
	}
	if user, ok := event.Message.(llm.UserMessage); ok {
		converted, valid := r.messageFromLLMLocked(user, r.userID)
		if valid {
			message = &converted
		}
	}
	if assistant, ok := event.Message.(llm.AssistantMessage); ok && assistant.StopReason != llm.StopReasonError {
		if r.currentAssistantID == "" {
			r.assistantTurn++
			r.currentAssistantID = fmt.Sprintf("%s/live/%d/assistant/%d", r.namespace, r.sequence, r.assistantTurn)
		}
		converted, ok := r.liveAssistantLocked(assistant, r.currentAssistantID, nil)
		if !ok && assistant.StopReason == llm.StopReasonAborted {
			// abort 必须有可见反馈，空 partial 也要构造消息
			converted = tui.Message{ID: r.currentAssistantID, Role: "assistant"}
		}
		if ok || assistant.StopReason == llm.StopReasonAborted {
			// aborted 的 assistant 保留 partial 正文并带取消标记，由渲染层追加提示行
			converted.Aborted = assistant.StopReason == llm.StopReasonAborted
			message = &converted
		}
	}
	if message != nil && event.SourceID != "" && !strings.HasPrefix(message.ID, r.namespace+"/entry/") {
		previousID = message.ID
		message.ID = r.sourceID(event.SourceID, "message")
		r.renameMessageLocked(previousID, message.ID)
		if message.ToolGroup != nil {
			message.ToolGroup = message.ToolGroup.Clone()
			message.ToolGroup.ID = message.ID + "/tools"
		}
	}
	return message, previousID
}

func (r *Runtime) handleEvent(event llm.AgentEvent) error {
	r.mu.Lock()
	var notifications []tea.Msg
	var message *tui.Message
	var previousID string
	var status string
	queueChanged := false
	previousToolAnchor := r.activeToolAnchorID
	switch event := event.(type) {
	case llm.AgentStartEvent:
		if r.turnStarted.IsZero() || !r.turnRetrying {
			r.turnStarted = time.Now()
		}
		r.turnRetrying = false
		// 用户请求驱动的 run 已由 TUI 自己置忙碌态;这里只补后台 run
		if !r.externalRunSeen && !r.operationActive {
			r.externalRunSeen = true
			notifications = append(notifications, tui.BackgroundRunMsg{Running: true})
		}
	case llm.TurnStartEvent:
		if r.turnStarted.IsZero() {
			r.turnStarted = time.Now()
		}
	case llm.MessageStartEvent:
		switch current := event.Message.(type) {
		case llm.UserMessage:
			r.sealToolGroupLocked()
			r.userMessages++
			if r.userMessages > 1 {
				queueChanged = true
				r.userID = fmt.Sprintf("%s/live/%d/user/%d", r.namespace, r.sequence, r.userMessages)
			}
			converted, ok := r.messageFromLLMLocked(event.Message, r.userID)
			if ok {
				message = &converted
			}
		case llm.AssistantMessage:
			r.assistantTurn++
			r.currentAssistantID = fmt.Sprintf("%s/live/%d/assistant/%d", r.namespace, r.sequence, r.assistantTurn)
			converted, ok := r.liveAssistantLocked(current, r.currentAssistantID, nil)
			if ok {
				message = &converted
			}
		}
	case llm.MessageUpdateEvent:
		if assistant, ok := event.Message.(llm.AssistantMessage); ok {
			converted, ok := r.liveAssistantLocked(assistant, r.currentAssistantID, event.AssistantMessageEvent)
			if ok {
				message = &converted
			}
		}
	case llm.MessageEndEvent:
		if projected, renamed := r.projectMessageEnd(event); projected != nil {
			message, previousID = projected, renamed
		}
	case llm.TurnEndEvent:
		// 工具循环保持一个紧凑分组，直到出现可见文本或 agent_end
	case llm.AgentEndEvent:
		if !event.WillRetry && r.externalRunSeen {
			r.externalRunSeen = false
			notifications = append(notifications, tui.BackgroundRunMsg{Running: false})
		}
		if event.WillRetry {
			r.sealToolGroupLocked()
			r.turnRetrying = true
		} else {
			r.sealToolGroupLocked()
			if index := r.addDividerLocked(); index >= 0 {
				notifications = append(notifications, tui.MessageMsg(r.messages[index]))
			}
			r.turnStarted = time.Time{}
			r.turnRetrying = false
		}
	case llm.ToolExecutionStartEvent:
		converted := r.toolStartLocked(event)
		message = &converted
	case llm.ToolExecutionUpdateEvent:
		converted := r.toolUpdateLocked(event)
		message = &converted
	case llm.ToolExecutionEndEvent:
		converted := r.toolEndLocked(event)
		message = &converted
	case llm.WarningEvent:
		r.warningSeq++
		message = &tui.Message{
			ID:   fmt.Sprintf("%s/warning/%d", r.namespace, r.warningSeq),
			Role: "notice",
			Text: "Warning: " + event.Message,
		}
	case llm.CompactionStartEvent:
		status = "Compacting context…"
	case llm.CompactionEndEvent:
		if event.Error == "" {
			status = "Working"
			r.resetNeeded = true
			r.compactionNoticeSeq++
			notice := tui.Message{
				ID:   fmt.Sprintf("%s/compaction/%d", r.namespace, r.compactionNoticeSeq),
				Role: "summary",
				Text: fmt.Sprintf("Context compacted (%s): %d -> %d tokens", event.Reason, event.TokensBefore, event.TokensAfter),
			}
			r.compactionNotice = &notice
		} else {
			status = "Compaction failed"
			r.compactionNotice = nil
		}
	}
	if message != nil {
		r.upsertLocked(*message)
		if previousID != "" {
			notifications = append(notifications, tui.MessageCommittedMsg{PreviousID: previousID, Message: r.messages[r.messageIndexLocked(message.ID)]})
		} else {
			notifications = append(notifications, tui.MessageMsg(r.messages[r.messageIndexLocked(message.ID)]))
		}
	}
	if previousToolAnchor != "" && r.activeToolAnchorID == "" {
		if index := r.messageIndexLocked(previousToolAnchor); index >= 0 {
			notifications = append(notifications, tui.MessageMsg(r.messages[index]))
		}
	}
	if status != "" {
		notifications = append(notifications, tui.StatusMsg(status))
	}
	if queueChanged && r.app != nil {
		notifications = append(notifications, tui.QueueStateMsg{
			Steering: r.app.QueueMessages(agent.QueueSteering), SteeringInputs: payloadsFromMessages(queueUserMessages(r.app, agent.QueueSteering)), SteeringRevision: r.app.QueueRevision(agent.QueueSteering),
			FollowUp: r.app.QueueMessages(agent.QueueFollowUp), FollowUpInputs: payloadsFromMessages(queueUserMessages(r.app, agent.QueueFollowUp)), FollowUpRevision: r.app.QueueRevision(agent.QueueFollowUp),
		})
		notifications = append(notifications, tui.SteeringStateMsg{Messages: r.app.QueueMessages(agent.QueueSteering), Inputs: payloadsFromMessages(queueUserMessages(r.app, agent.QueueSteering)), Revision: r.app.QueueRevision(agent.QueueSteering)})
	}
	send := r.send
	r.mu.Unlock()
	// 通知可能阻塞，发送时不持有投影或取消所需的锁
	if send != nil {
		for _, message := range notifications {
			send(message)
		}
	}
	return nil
}

func (r *Runtime) upsertLocked(message tui.Message) {
	index := r.messageIndexLocked(message.ID)
	if message.ToolGroup != nil {
		r.hasToolGroup = true
	}
	if message.Role == "assistant" && message.Text != "" {
		if index < 0 {
			r.lastAssistantIndex = len(r.messages)
		} else {
			r.lastAssistantIndex = index
		}
	}
	if index >= 0 {
		message.Revision = r.messages[index].Revision + 1
		r.messages[index] = message
		return
	}
	if message.Revision == 0 {
		message.Revision = 1
	}
	r.messageIndex[message.ID] = len(r.messages)
	r.messages = append(r.messages, message)
}

func (r *Runtime) sourceID(source, part string) string {
	return r.namespace + "/entry/" + source + "/" + part
}

func (r *Runtime) renameMessageLocked(oldID, newID string) {
	if index := r.messageIndexLocked(oldID); index >= 0 {
		delete(r.messageIndex, oldID)
		r.messageIndex[newID] = index
		r.messages[index].ID = newID
	}
	if r.userID == oldID {
		r.userID = newID
	}
	if r.currentAssistantID == oldID {
		r.currentAssistantID = newID
	}
	if r.activeToolAnchorID == oldID {
		r.activeToolAnchorID = newID
	}
}

func (r *Runtime) replaceMessagesLocked(view ctxview.ContextView) {
	messages := preserveDisplayMetadata(r.projectContext(view), r.messages)
	r.messages = messages
	r.messageIndex = nil
	r.activeToolAnchorID, r.currentAssistantID = "", ""
}

func (r *Runtime) resetProjectionLocked() {
	r.lastAssistantIndex = 0
	r.hasToolGroup = false
	r.operationLastAssistant, r.operationHadToolGroup = 0, false
	r.operationStart = 0
	r.operationEntries = nil
	r.userID = ""
	r.assistantTurn = 0
	r.currentAssistantID, r.activeToolAnchorID = "", ""
	r.toolStarted = nil
	r.turnStarted, r.turnRetrying = time.Time{}, false
	r.compactionNotice = nil
}

func (r *Runtime) partialAssistantLocked() *tui.Message {
	for i := len(r.messages) - 1; i >= 0; i-- {
		message := r.messages[i]
		if message.Role == "assistant" && message.ID == r.currentAssistantID && (message.Text != "" || message.ToolGroup != nil || message.Aborted) {
			copy := message
			return &copy
		}
		if message.Role == "user" {
			break
		}
	}
	return nil
}

func (r *Runtime) hasMessageLocked(message tui.Message) bool {
	return r.messageIndexLocked(message.ID) >= 0
}

func (r *Runtime) messageFromLLMLocked(message llm.Message, id string) (tui.Message, bool) {
	converted := tui.Message{ID: id}
	switch message := message.(type) {
	case llm.UserMessage:
		converted.Role, converted.Text = "user", textContent(message.Content)
		converted.Images = imageViews(message.Content)
		converted.Skill = skillInvocation(converted.Text)
	default:
		return tui.Message{}, false
	}
	return converted, converted.Text != "" || len(converted.Images) > 0
}

func imageViews(contents []llm.Content) []tui.ImageView {
	var images []tui.ImageView
	for _, content := range contents {
		if image, ok := content.(llm.ImageContent); ok {
			images = append(images, tui.ImageView{MIMEType: image.MIMEType})
		}
	}
	return images
}

func textContent(contents []llm.Content) string {
	var parts []string
	for _, content := range contents {
		if text, ok := content.(llm.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func userMessageEditorText(message llm.UserMessage) string {
	var builder strings.Builder
	imageIndex := 0
	for _, content := range message.Content {
		switch content := content.(type) {
		case llm.TextContent:
			builder.WriteString(content.Text)
		case llm.ImageContent:
			imageIndex++
			fmt.Fprintf(&builder, "[Image #%d]", imageIndex)
		}
	}
	return builder.String()
}

func userMessageEditorTextOrEmpty(message *llm.UserMessage) string {
	if message == nil {
		return ""
	}
	return userMessageEditorText(*message)
}

func (r *Runtime) snapshotLocked(includeMessages bool) tui.Snapshot {
	model := r.app.CurrentModel()
	thinking := r.app.CurrentThinkingLevel()
	if thinking == "" {
		thinking = llm.ThinkingLevelOff
	}
	models := make([]tui.ModelOption, 0)
	for _, candidate := range r.app.AvailableModels() {
		models = append(models, tui.ModelOption{ID: candidate.ID, Provider: candidate.Provider, Name: candidate.Name, ContextWindow: candidate.ContextWindow, Levels: thinkingLevels(candidate), SupportsImages: candidate.SupportsImages})
	}
	current := modelOption(model)
	var skills []tui.SkillOption
	for _, skill := range r.app.Skills() {
		skills = append(skills, tui.SkillOption{Name: skill.Name, Description: skill.Description})
	}
	var messages []tui.Message
	if includeMessages {
		messages = append([]tui.Message(nil), r.messages...)
	}
	steering := r.app.QueueMessages(agent.QueueSteering)
	steeringInputs := queueUserMessages(r.app, agent.QueueSteering)
	followUp := r.app.QueueMessages(agent.QueueFollowUp)
	followUpInputs := queueUserMessages(r.app, agent.QueueFollowUp)
	return tui.Snapshot{Cwd: r.cwd, Branch: r.branch, ModelKey: model.Provider + "/" + model.ID, DefaultModelKey: r.cfg.ProviderID + "/" + r.cfg.ModelID, CurrentModel: current,
		Thinking: string(thinking), Models: models, Tree: projectTree(r.app.Tree()), Messages: messages, SessionName: r.app.SessionName(), Skills: skills,
		Steering: steering, SteeringInputs: payloadsFromMessages(steeringInputs), SteeringRevision: r.app.QueueRevision(agent.QueueSteering), FollowUp: followUp, FollowUpInputs: payloadsFromMessages(followUpInputs), FollowUpRevision: r.app.QueueRevision(agent.QueueFollowUp)}
}

func listSessionOptions(cwd string) []tui.SessionOption {
	sessions, _ := session.ListSessions(cwd)
	options := make([]tui.SessionOption, 0, len(sessions))
	for _, item := range sessions {
		label := item.Name
		if label == "" {
			label = item.Summary
		}
		options = append(options, tui.SessionOption{Path: item.Path, Summary: label, Modified: session.RelativeTime(item.ModTime)})
	}
	return options
}

func modelOption(model llm.Model) tui.ModelOption {
	return tui.ModelOption{ID: model.ID, Provider: model.Provider, Name: model.Name, ContextWindow: model.ContextWindow, Levels: thinkingLevels(model), SupportsImages: model.SupportsImages}
}

func (r *Runtime) snapshot() tui.Snapshot {
	r.mu.Lock()
	snapshot := r.snapshotLocked(true)
	r.mu.Unlock()
	snapshot.Sessions = listSessionOptions(snapshot.Cwd)
	snapshot.Subagents = r.subagentSnapshotLocked()
	return snapshot
}

func thinkingLevels(model llm.Model) []string {
	if !model.Reasoning {
		return []string{"off"}
	}
	if len(model.ThinkingLevelMap) == 0 {
		return []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	}
	levels := make([]string, 0, len(model.ThinkingLevelMap))
	for _, level := range llm.ThinkingLevels {
		if _, ok := model.ThinkingLevelMap[level]; ok {
			levels = append(levels, string(level))
		}
	}
	return levels
}

func New(cfg provider.Config, cwd, branch string) *Runtime {
	return &Runtime{cfg: cfg, cwd: cwd, branch: branch}
}

// SetProjectContext 在打开会话后更新显示用的项目上下文
func (r *Runtime) SetProjectContext(cwd, branch string) {
	r.mu.Lock()
	r.cwd, r.branch = cwd, branch
	r.mu.Unlock()
}

// attachBackgroundHookLocked 把后台 run 的结束兜底挂到给定 App。
// 事件投影可能因提交失败漏发 AgentEndEvent,只靠事件清不掉忙碌态
func (r *Runtime) attachBackgroundHookLocked(a *app.App) {
	a.SetBackgroundRunHook(func() {
		r.mu.Lock()
		// 切换会话后旧 App 的收尾不能清掉新会话的忙碌态
		if r.app != a {
			r.mu.Unlock()
			return
		}
		r.externalRunSeen = false
		send := r.send
		r.mu.Unlock()
		if send != nil {
			send(tui.BackgroundRunMsg{Running: false})
		}
	})
}

func (r *Runtime) Attach(a *app.App) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.app = a
	r.namespace = a.SessionID()
	r.attachBackgroundHookLocked(a)
	r.replaceMessagesLocked(a.ContextView())
	for _, warning := range a.Warnings() {
		r.warningSeq++
		r.upsertLocked(tui.Message{
			ID:   fmt.Sprintf("%s/warning/%d", r.namespace, r.warningSeq),
			Role: "notice",
			Text: "Warning: " + warning,
		})
	}
}

func (r *Runtime) RestoreSteering() []string {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return nil
	}
	return queueTexts(activeApp, agent.QueueSteering)
}

func (r *Runtime) RestoreSteeringMessages() []tui.ComposerPayload {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return nil
	}
	return payloadsFromMessages(queueUserMessagesFrom(activeApp, agent.QueueSteering))
}

func (r *Runtime) EnqueueSteering(value string) error {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return app.ErrSteeringUnavailable
	}
	return activeApp.EnqueueSteering(value)
}

func (r *Runtime) EnqueueSteeringMessage(value tui.ComposerPayload) error {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return app.ErrSteeringUnavailable
	}
	message := payloadMessage(&value)
	if message == nil {
		return nil
	}
	return activeApp.EnqueueSteeringMessage(*message)
}

func (r *Runtime) RestoreFollowUpMessages() []tui.ComposerPayload {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return nil
	}
	return payloadsFromMessages(queueUserMessagesFrom(activeApp, agent.QueueFollowUp))
}

func (r *Runtime) ClearFollowUp() []tui.ComposerPayload {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return nil
	}
	return payloadsFromMessages(queueUserMessagesFrom(activeApp, agent.QueueFollowUp))
}

func (r *Runtime) EnqueueFollowUp(value string) error {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return app.ErrFollowUpUnavailable
	}
	return activeApp.EnqueueFollowUp(value)
}

func (r *Runtime) EnqueueFollowUpMessage(value tui.ComposerPayload) error {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return app.ErrFollowUpUnavailable
	}
	message := payloadMessage(&value)
	if message == nil {
		return nil
	}
	return activeApp.EnqueueFollowUpMessage(*message)
}

func (r *Runtime) HandleEvent(event llm.AgentEvent) error { return r.handleEvent(event) }
func (r *Runtime) Request(request tui.Request) tea.Cmd    { return r.request(request) }

// Status 返回只读的会话状态命令，不创建 App operation
func (r *Runtime) Status() tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		activeApp := r.app
		r.mu.Unlock()
		if activeApp == nil {
			return tui.SessionStatusMsg{Err: errors.New("status is unavailable")}
		}
		model := activeApp.CurrentModel()
		usage := activeApp.ContextUsage()
		return tui.SessionStatusMsg{
			Model:         model.ID,
			Provider:      model.Provider,
			ThreadName:    activeApp.SessionName(),
			SessionID:     activeApp.SessionID(),
			Transcript:    activeApp.SessionPath(),
			Tokens:        usage.Tokens,
			ContextWindow: usage.ContextWindow,
			Available:     usage.Available,
		}
	}
}
func (r *Runtime) Cancel() { r.cancelCurrent() }
func (r *Runtime) Wait()   { r.waitCurrent() }

// ShutdownSubagents 终止所有运行中的子任务与后台命令
func (r *Runtime) ShutdownSubagents() int {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	if activeApp == nil {
		return 0
	}
	return activeApp.ShutdownSubagents()
}
func (r *Runtime) Snapshot() tui.Snapshot { return r.snapshot() }

func preserveDisplayMetadata(messages, previous []tui.Message) []tui.Message {
	old := make(map[string]tui.Message, len(previous))
	for _, message := range previous {
		old[message.ID] = message
	}
	for i := range messages {
		before := old[messages[i].ID]
		messages[i].Divider = before.Divider
		messages[i].Revision = before.Revision + 1
		if messages[i].ToolGroup != nil && before.ToolGroup != nil {
			for j := range messages[i].ToolGroup.Calls {
				call := &messages[i].ToolGroup.Calls[j]
				if index := findToolCall(before.ToolGroup, call.ID); index >= 0 {
					oldCall := before.ToolGroup.Calls[index]
					call.StartedAt = oldCall.StartedAt
				}
			}
		}
	}
	return messages
}
