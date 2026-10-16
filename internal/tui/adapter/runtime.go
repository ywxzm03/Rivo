package adapter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// Runtime 把 app 的同步事件投影成 UI 消息；它不把 Agent 或 SessionManager 暴露给 TUI
type Runtime struct {
	mu sync.Mutex

	app    *app.App
	cfg    app.Config
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
	operationEntries       []compaction.ContextEntry
	messages               []tui.Message
	sequence               int
	userID                 string
	assistantTurn          int
	currentAssistantID     string
	activeToolAnchorID     string
	toolStarted            map[string]time.Time
	turnStarted            time.Time
	turnRetrying           bool
	// compactionNotice 只供手动压缩完成时投影到 TUI，不进入会话上下文
	compactionNotice    *tui.Message
	compactionNoticeSeq uint64
	pendingSkillInput   *skillInput
}

func (r *Runtime) SetProgram(program *tea.Program) {
	r.mu.Lock()
	r.send = program.Send
	r.mu.Unlock()
}

// request 由 App 接受并立即执行工作，tea.Cmd 只等待结果并转换显示快照
func (r *Runtime) request(req tui.Request) tea.Cmd {
	r.mu.Lock()
	activeApp := r.app
	r.compactionNotice = nil
	r.mu.Unlock()
	value := req.Value
	if req.Kind == "run" {
		var err error
		value, err = r.expandSkillInput(activeApp, value)
		if err != nil {
			return func() tea.Msg { return tui.OperationDoneMsg{ID: req.ID, Err: err} }
		}
	}
	r.mu.Lock()
	if req.Kind == "run" {
		r.messageIndexLocked("")
		r.operationLastAssistant, r.operationHadToolGroup = r.lastAssistantIndex, r.hasToolGroup
		r.operationStart = len(r.messages)
		r.operationEntries = nil
		r.sequence++
		r.userID = fmt.Sprintf("%s/live/%d/user", r.namespace, r.sequence)
		r.currentAssistantID, r.activeToolAnchorID = "", ""
		r.assistantTurn = 0
		r.toolStarted = make(map[string]time.Time)
		r.turnStarted = time.Time{}
		r.turnRetrying = false
	}
	r.mu.Unlock()
	op, err := activeApp.Start(context.Background(), app.OperationRequest{Kind: app.OperationKind(req.Kind), Value: value})
	if err != nil {
		return func() tea.Msg { return tui.OperationDoneMsg{ID: req.ID, Err: err} }
	}
	return func() tea.Msg {
		result := op.Wait()
		r.mu.Lock()
		var commitErr *session.CommitError
		if errors.As(result.Err, &commitErr) {
			if req.Kind == "run" && strings.HasPrefix(req.Value, "/skill:") {
				r.pendingSkillInput = &skillInput{raw: req.Value, expanded: value}
			}
		} else {
			r.pendingSkillInput = nil
		}
		reset := result.Next != nil || r.resetNeeded || result.Replayed || req.Kind == "tree"
		if result.Next != nil {
			r.app = result.Next
			r.namespace = result.Next.SessionID()
			r.messages = nil
			r.pendingSkillInput = nil
			if req.Kind == "new" {
				r.operationEntries = nil
				r.sequence, r.assistantTurn = 0, 0
				r.userID = ""
				r.toolStarted = nil
				r.turnStarted, r.turnRetrying = time.Time{}, false
			}
		}
		if reset {
			r.replaceMessagesLocked(r.app.ContextView())
			r.resetNeeded = false
		}
		var tail *tui.MessageTail
		if req.Kind == "run" && !reset {
			// 完成时只重放本次操作，保留历史重放的 thinking/工具分组显示规则
			old := r.messages[r.operationStart:]
			projected := r.projectContext(compaction.ContextView{Entries: r.operationEntries})
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
		// 手动压缩不产生普通消息，完成后追加可见提示；内部摘要仍只留在模型上下文
		if req.Kind == "compact" && result.Err == nil && r.compactionNotice != nil {
			changes = append(changes, *r.compactionNotice)
		}
		r.compactionNotice = nil
		snapshot := r.snapshotLocked(reset)

		r.mu.Unlock()
		snapshot.Sessions = listSessionOptions(snapshot.Cwd)
		if errors.Is(result.Err, context.Canceled) {
			result.Err = nil
		}
		var notice string
		if result.Err == nil {
			switch req.Kind {
			case "rename":
				notice = "Session renamed: " + snapshot.SessionName
			case "new":
				notice = "New session started"
			case "reload":
				notice = "Reloaded context files and skills"
			}
		}
		return tui.OperationDoneMsg{ID: req.ID, Snapshot: &snapshot, RetainMessages: !reset, Tail: tail, Changes: changes, Err: result.Err, Notice: notice, EditorValue: result.EditorText, RestoreEditor: result.RestoreEditor, ResetSessionUI: req.Kind == "new" && result.Next != nil}
	}
}

func (r *Runtime) cancelCurrent() {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	activeApp.Cancel()
}

// waitCurrent 在终端恢复后等待 App 收尾，不依赖未执行的 tea.Cmd
func (r *Runtime) waitCurrent() {
	r.mu.Lock()
	activeApp := r.app
	r.mu.Unlock()
	activeApp.Wait()
}

// handleEvent 只发送消息/状态事件；OperationDoneMsg 由请求命令在 app 返回后发送
func (r *Runtime) handleEvent(event llm.AgentEvent) error {
	r.mu.Lock()
	var notifications []tea.Msg
	var message *tui.Message
	var previousID string
	var status string
	previousToolAnchor := r.activeToolAnchorID
	switch event := event.(type) {
	case llm.AgentStartEvent:
		if r.turnStarted.IsZero() || !r.turnRetrying {
			r.turnStarted = time.Now()
		}
		r.turnRetrying = false
	case llm.TurnStartEvent:
		if r.turnStarted.IsZero() {
			r.turnStarted = time.Now()
		}
	case llm.MessageStartEvent:
		switch current := event.Message.(type) {
		case llm.UserMessage:
			r.sealToolGroupLocked()
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
		if event.SourceID != "" {
			r.operationEntries = append(r.operationEntries, compaction.ContextEntry{SourceID: event.SourceID, Kind: compaction.ContextMessage, Messages: []llm.Message{event.Message}})
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
				// abort 必有可见反馈：空 partial 也要构造消息（对齐 Pi 的无条件渲染）
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
				message.ToolGroup = cloneToolGroup(message.ToolGroup)
				message.ToolGroup.ID = message.ID + "/tools"
			}
		}
	case llm.TurnEndEvent:
		// Tool loops stay in one compact group until visible text or agent_end.
	case llm.AgentEndEvent:
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
	send := r.send
	r.mu.Unlock()
	// 通知可以阻塞，发送时不持有投影或取消所需的锁
	if send != nil {
		for _, message := range notifications {
			send(message)
		}
	}
	return nil
}

// upsertLocked 以稳定 ID 定位显示块，revision 只在此块发生变化时推进
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

// replaceMessagesLocked 只在加载或来源结构变化时重放，元数据按来源身份保留
func (r *Runtime) replaceMessagesLocked(view compaction.ContextView) {
	messages := preserveDisplayMetadata(r.projectContext(view), r.messages)
	r.messages = messages
	r.messageIndex = nil
	r.activeToolAnchorID, r.currentAssistantID = "", ""
}

func (r *Runtime) partialAssistantLocked() *tui.Message {
	for i := len(r.messages) - 1; i >= 0; i-- {
		message := r.messages[i]
		if message.Role == "assistant" && message.ID == r.currentAssistantID && (message.Text != "" || message.Thinking != "" || message.ToolGroup != nil || message.Aborted) {
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
		converted.Skill = skillInvocation(converted.Text)
	case llm.AssistantMessage:
		converted.Role = "assistant"
		for _, content := range message.Content {
			switch content := content.(type) {
			case llm.TextContent:
				converted.Text += content.Text
			case llm.ThinkingContent:
				converted.Thinking += content.Thinking
			}
		}
	case llm.ToolResultMessage:
		return tui.Message{}, false
	default:
		return tui.Message{}, false
	}
	return converted, converted.Text != "" || converted.Thinking != ""
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

func (r *Runtime) snapshotLocked(includeMessages bool) tui.Snapshot {
	model := r.app.CurrentModel()
	thinking := r.app.CurrentThinkingLevel()
	if thinking == "" {
		thinking = llm.ThinkingLevelOff
	}
	models := make([]tui.ModelOption, 0)
	for _, candidate := range r.app.AvailableModels() {
		models = append(models, tui.ModelOption{ID: candidate.ID, Provider: candidate.Provider, Name: candidate.Name, ContextWindow: candidate.ContextWindow, Levels: thinkingLevels(candidate)})
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
	return tui.Snapshot{Cwd: r.cwd, Branch: r.branch, ModelKey: model.Provider + "/" + model.ID, DefaultModelKey: r.cfg.ProviderID + "/" + r.cfg.ModelID, CurrentModel: current,
		Thinking: string(thinking), Models: models, Tree: projectTree(r.app.Tree()), Messages: messages, SessionName: r.app.SessionName(), Skills: skills}
}

// listSessionOptions 在运行时锁外读取会话，慢速文件系统不阻塞取消与事件投影
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
	return tui.ModelOption{ID: model.ID, Provider: model.Provider, Name: model.Name, ContextWindow: model.ContextWindow, Levels: thinkingLevels(model)}
}

func (r *Runtime) snapshot() tui.Snapshot {
	r.mu.Lock()
	snapshot := r.snapshotLocked(true)
	r.mu.Unlock()
	snapshot.Sessions = listSessionOptions(snapshot.Cwd)
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
	for level := range model.ThinkingLevelMap {
		levels = append(levels, string(level))
	}
	sort.Strings(levels)
	return levels
}

// New 创建显示适配器，事件 sink 可在 App 装配时绑定
func New(cfg app.Config, cwd, branch string) *Runtime {
	return &Runtime{cfg: cfg, cwd: cwd, branch: branch}
}

// Attach 在加载边界绑定 App 并重放会话投影
func (r *Runtime) Attach(a *app.App) {
	r.app = a
	r.namespace = a.SessionID()
	r.replaceMessagesLocked(a.ContextView())
}

func (r *Runtime) HandleEvent(event llm.AgentEvent) error { return r.handleEvent(event) }
func (r *Runtime) Request(request tui.Request) tea.Cmd    { return r.request(request) }
func (r *Runtime) Cancel()                                { r.cancelCurrent() }
func (r *Runtime) Wait()                                  { r.waitCurrent() }
func (r *Runtime) Snapshot() tui.Snapshot                 { return r.snapshot() }

func preserveDisplayMetadata(messages, previous []tui.Message) []tui.Message {
	old := make(map[string]tui.Message, len(previous))
	for _, message := range previous {
		old[message.ID] = message
	}
	for i := range messages {
		before := old[messages[i].ID]
		messages[i].Elapsed, messages[i].Divider = before.Elapsed, before.Divider
		messages[i].Revision = before.Revision + 1
		if messages[i].ToolGroup != nil && before.ToolGroup != nil {
			for j := range messages[i].ToolGroup.Calls {
				call := &messages[i].ToolGroup.Calls[j]
				if index := findToolCall(before.ToolGroup, call.ID); index >= 0 {
					oldCall := before.ToolGroup.Calls[index]
					call.Elapsed, call.StartedAt, call.EndedAt = oldCall.Elapsed, oldCall.StartedAt, oldCall.EndedAt
				}
			}
		}
	}
	return messages
}
