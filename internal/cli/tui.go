package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// tuiRuntime 把 app 的同步事件投影成 UI 消息；它不把 Agent 或 SessionManager 暴露给 TUI
type tuiRuntime struct {
	mu sync.Mutex

	app    *app.App
	cfg    app.Config
	send   func(tea.Msg)
	cwd    string
	branch string

	messages           []tui.Message
	sequence           int
	userID             string
	assistantTurn      int
	currentAssistantID string
	activeToolAnchorID string
	toolGroupSequence  int
	toolStarted        map[string]time.Time
	turnStarted        time.Time
	turnRetrying       bool
	cancel             context.CancelFunc
	done               <-chan struct{}
}

func (r *tuiRuntime) setProgram(program *tea.Program) {
	r.mu.Lock()
	r.send = program.Send
	r.mu.Unlock()
}

// request 接受操作后立即启动后台工作，tea.Cmd 只等待完成结果
func (r *tuiRuntime) request(req tui.Request) tea.Cmd {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return func() tea.Msg { return tui.OperationDoneMsg{Err: errors.New("an operation is running")} }
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	done := make(chan struct{})
	r.done = done
	activeApp := r.app
	r.mu.Unlock()
	results := make(chan tea.Msg, 1)
	go func() {
		result := r.execute(ctx, activeApp, req)
		cancel()
		r.mu.Lock()
		r.cancel, r.done = nil, nil
		close(done)
		r.mu.Unlock()
		results <- result
	}()
	return func() tea.Msg { return <-results }
}

// execute 运行操作并生成快照；完成通知由 tea.Cmd 返回，晚于活动句柄的清理
func (r *tuiRuntime) execute(ctx context.Context, activeApp *app.App, req tui.Request) tea.Msg {
	var err error
	switch req.Kind {
	case "run":
		r.mu.Lock()
		r.sequence++
		r.userID = fmt.Sprintf("user-%d", r.sequence)
		r.currentAssistantID = ""
		r.activeToolAnchorID = ""
		r.assistantTurn, r.toolGroupSequence = 0, 0
		r.toolStarted = make(map[string]time.Time)
		r.turnStarted = time.Time{}
		r.turnRetrying = false
		r.mu.Unlock()
		_, err = activeApp.Run(ctx, req.Value)
	case "compact":
		err = activeApp.Compact(ctx, req.Value)
	case "model":
		provider, model, _ := strings.Cut(req.Value, "/")
		err = activeApp.SetModel(provider, model)
	case "thinking":
		err = activeApp.SetThinkingLevel(llm.ThinkingLevel(req.Value))
	case "resume":
		var next *app.App
		next, err = app.Open(req.Value, r.cfg, r.handleEvent)
		if err == nil {
			r.mu.Lock()
			r.app = next
			r.messages = nil
			r.replaceMessagesLocked(next.SessionContext())
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	if r.app != nil {
		var partial *tui.Message
		if req.Kind == "run" {
			partial = r.partialAssistantLocked()
		}
		r.replaceMessagesLocked(r.app.SessionContext())
		if partial != nil && !r.hasMessageLocked(*partial) {
			r.upsertLocked(*partial)
		}
	}
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return tui.OperationDoneMsg{Snapshot: &snapshot, Err: err}
}

func (r *tuiRuntime) cancelCurrent() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// waitCurrent 等待已接受的操作收尾；在 Bubble Tea 恢复终端后调用
func (r *tuiRuntime) waitCurrent() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

// handleEvent 只发送消息/状态事件；OperationDoneMsg 由请求命令在 app 返回后发送
func (r *tuiRuntime) handleEvent(event llm.AgentEvent) error {
	r.mu.Lock()
	var notifications []tea.Msg
	var message *tui.Message
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
			r.currentAssistantID = fmt.Sprintf("assistant-%d-%d", r.sequence, r.assistantTurn)
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
		if assistant, ok := event.Message.(llm.AssistantMessage); ok && assistant.StopReason != llm.StopReasonError {
			if r.currentAssistantID == "" {
				r.assistantTurn++
				r.currentAssistantID = fmt.Sprintf("assistant-%d-%d", r.sequence, r.assistantTurn)
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
	case llm.TurnEndEvent:
		// Tool loops stay in one compact group until visible text or agent_end.
	case llm.AgentEndEvent:
		if event.WillRetry {
			r.sealToolGroupLocked()
			r.turnRetrying = true
		} else {
			r.sealToolGroupLocked()
			r.addDividerLocked()
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
		} else {
			status = "Compaction failed"
		}
	}
	if message != nil {
		r.upsertLocked(*message)
		notifications = append(notifications, tui.MessageMsg(*message))
	}
	if previousToolAnchor != "" && r.activeToolAnchorID == "" {
		if index := r.messageIndexLocked(previousToolAnchor); index >= 0 {
			notifications = append(notifications, tui.MessageMsg(r.messages[index]))
		}
	}
	if _, ok := event.(llm.AgentEndEvent); ok {
		for _, current := range r.messages {
			if current.ToolGroup != nil || current.Divider != nil {
				notifications = append(notifications, tui.MessageMsg(current))
			}
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

func (r *tuiRuntime) upsertLocked(message tui.Message) {
	for i := range r.messages {
		if r.messages[i].ID == message.ID {
			r.messages[i] = message
			return
		}
	}
	r.messages = append(r.messages, message)
}

func (r *tuiRuntime) replaceMessagesLocked(proj session.SessionContext) {
	old := make(map[string]time.Duration)
	dividers := make(map[string]*tui.TurnDividerView)
	for _, message := range r.messages {
		key := message.Role + "\x00" + message.Text
		old[key] = message.Elapsed
		if message.Divider != nil {
			divider := *message.Divider
			dividers[key] = &divider
		}
	}
	messages := r.projectSessionMessages(proj.Messages)
	for i := range messages {
		key := messages[i].Role + "\x00" + messages[i].Text
		messages[i].Elapsed = old[key]
		messages[i].Divider = dividers[key]
	}
	r.messages = messages
	r.activeToolAnchorID = ""
	r.currentAssistantID = ""
	if len(messages) > 0 {
		r.sequence = len(messages)
	}
}

func (r *tuiRuntime) partialAssistantLocked() *tui.Message {
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

func (r *tuiRuntime) hasMessageLocked(message tui.Message) bool {
	for _, current := range r.messages {
		if current.Role == message.Role && current.Text == message.Text && current.Thinking == message.Thinking {
			return true
		}
	}
	return false
}

func (r *tuiRuntime) messageFromLLMLocked(message llm.Message, id string) (tui.Message, bool) {
	converted := tui.Message{ID: id}
	switch message := message.(type) {
	case llm.UserMessage:
		converted.Role, converted.Text = "user", textContent(message.Content)
		if strings.HasPrefix(converted.Text, "The conversation history before this point was compacted") {
			converted.Role = "summary"
		}
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

func (r *tuiRuntime) snapshotLocked() tui.Snapshot {
	model := r.app.CurrentModel()
	thinking := r.app.CurrentThinkingLevel()
	if thinking == "" {
		thinking = llm.ThinkingLevelOff
	}
	models := make([]tui.ModelOption, 0)
	for _, candidate := range r.app.AvailableModels() {
		if candidate.Provider != "deepseek" || candidate.ID != "deepseek-v4.1-flash" {
			continue
		}
		models = append(models, tui.ModelOption{ID: candidate.ID, Provider: candidate.Provider, Name: candidate.Name, ContextWindow: candidate.ContextWindow, Levels: thinkingLevels(candidate)})
	}
	current := modelOption(model)
	sessions, _ := session.ListSessions(r.cwd)
	sessionOptions := make([]tui.SessionOption, 0, len(sessions))
	for _, item := range sessions {
		sessionOptions = append(sessionOptions, tui.SessionOption{Path: item.Path, Summary: item.Summary, Modified: relativeTime(item.ModTime)})
	}
	return tui.Snapshot{Cwd: r.cwd, Branch: r.branch, ModelKey: model.Provider + "/" + model.ID, DefaultModelKey: r.cfg.ProviderID + "/" + r.cfg.ModelID, CurrentModel: current,
		Thinking: string(thinking), Models: models, Sessions: sessionOptions, Messages: append([]tui.Message(nil), r.messages...)}
}

func modelOption(model llm.Model) tui.ModelOption {
	return tui.ModelOption{ID: model.ID, Provider: model.Provider, Name: model.Name, ContextWindow: model.ContextWindow, Levels: thinkingLevels(model)}
}

func (r *tuiRuntime) snapshot() tui.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
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

func runTUI(cfg app.Config, continueRecent bool, stdin, stdout *os.File, stderr io.Writer) int {
	cwd, _ := os.Getwd()
	branchBytes, _ := exec.Command("git", "branch", "--show-current").Output()
	runtime := &tuiRuntime{cfg: cfg, cwd: cwd, branch: strings.TrimSpace(string(branchBytes))}
	var a *app.App
	var err error
	if continueRecent {
		a, err = resumeRecentApp(cfg, runtime.handleEvent)
	} else {
		a, err = app.New(cfg, runtime.handleEvent)
	}
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 2
	}
	runtime.app = a
	runtime.replaceMessagesLocked(a.SessionContext())
	model := tui.New(runtime.snapshot(), tui.Handlers{
		Request:       runtime.request,
		Cancel:        runtime.cancelCurrent,
		ReadClipboard: clipboard.ReadAll,
		WriteClipboard: func(text string) error {
			if err := clipboard.WriteAll(text); err == nil {
				return nil
			}
			_, err := fmt.Fprint(stderr, osc52.New(text))
			return err
		},
	})
	program := tea.NewProgram(model, tea.WithInput(tui.NewInputReader(stdin)), tea.WithOutput(tui.NewCursorWriter(stdout, model.CursorPosition)), tea.WithAltScreen(), tea.WithMouseCellMotion())
	runtime.setProgram(program)
	_, err = program.Run()
	runtime.cancelCurrent()
	runtime.waitCurrent()
	if err != nil {
		fmt.Fprintf(stderr, "Rivo: %v\n", err)
		return 1
	}
	return 0
}
