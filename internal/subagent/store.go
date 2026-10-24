package subagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// Store 是任务状态、lifecycle runtime 句柄和通知的唯一所有者
type Store struct {
	mu            sync.Mutex
	tasks         map[string]TaskState
	runtimes      map[string]*AgentRuntime
	notifications []TaskNotification
	nextID        uint64
	nextToken     uint64
	viewingTaskID string
}

func NewStore() *Store {
	return &Store{tasks: make(map[string]TaskState), runtimes: make(map[string]*AgentRuntime)}
}

type RegisterSpec struct {
	ID           string
	Type         TaskType
	Description  string
	ToolUseID    string
	OutputFile   string
	AgentID      string
	Prompt       string
	AgentType    string
	Model        *llm.Model
	Thinking     llm.ThinkingLevel
	WorktreePath string
	Worktree     bool
	Runtime      *AgentRuntime
	BashState    *BashTaskState
	AgentState   *AgentTaskState
}

func (s *Store) Register(spec RegisterSpec) (string, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if spec.Type != TaskTypeBash && spec.Type != TaskTypeAgent {
		return "", 0, fmt.Errorf("subagent: unsupported task type %q", spec.Type)
	}
	id := spec.ID
	if id == "" {
		s.nextID++
		// task ID 同时也是 sidechain transcript 的文件名。加入进程时间分量,
		// 新起的 Rivo 进程就不会重开旧的 agent-1.jsonl 并在首次启动时
		// 因 O_EXCL 失败。
		id = fmt.Sprintf("%s-%d-%d", spec.Type, time.Now().UnixNano(), s.nextID)
	} else if existing, exists := s.tasks[id]; exists {
		if existing.Base().Status == StateRunning {
			return "", 0, fmt.Errorf("subagent: task %s is already running", id)
		}
		return "", 0, fmt.Errorf("subagent: task %s already exists; use continuation", id)
	}
	s.nextToken++
	token := s.nextToken
	base := TaskStateBase{ID: id, Type: spec.Type, Status: StateRunning, Description: spec.Description, ToolUseID: spec.ToolUseID, StartTime: time.Now(), OutputFile: spec.OutputFile, LifecycleToken: token}
	var state TaskState
	switch spec.Type {
	case TaskTypeBash:
		if spec.BashState == nil {
			spec.BashState = &BashTaskState{}
		}
		spec.BashState.TaskStateBase = base
		state = spec.BashState
	case TaskTypeAgent:
		if spec.AgentState == nil {
			spec.AgentState = &AgentTaskState{}
		}
		spec.AgentState.TaskStateBase = base
		spec.AgentState.AgentID = id
		spec.AgentState.Prompt = spec.Prompt
		spec.AgentState.AgentType = spec.AgentType
		spec.AgentState.SelectedAgent = spec.AgentType
		spec.AgentState.Model = cloneModel(spec.Model)
		spec.AgentState.Thinking = spec.Thinking
		spec.AgentState.WorktreePath = spec.WorktreePath
		spec.AgentState.Worktree = spec.Worktree
		state = spec.AgentState
	}
	s.tasks[id] = state
	if spec.Runtime != nil {
		spec.Runtime.LifecycleToken = token
		s.runtimes[id] = spec.Runtime
	}
	return id, token, nil
}

func (s *Store) Task(id string) (TaskState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	return cloneTaskState(task), true
}

func (s *Store) Snapshot() []TaskState {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]TaskState, 0, len(s.tasks))
	for _, task := range s.tasks {
		result = append(result, cloneTaskState(task))
	}
	return result
}

// EnterView 记录根 TUI 正在显示哪个任务的 transcript。
// viewing 只是展示层元数据,绝不启动或恢复任务。
func (s *Store) EnterView(id string) bool {
	s.mu.Lock()
	if _, ok := s.tasks[id]; !ok {
		s.mu.Unlock()
		return false
	}
	s.viewingTaskID = id
	s.mu.Unlock()
	// 磁盘 bootstrap 在 store 锁之外进行;打开 transcript 期间运行中的任务
	// 仍可继续发布进度。
	s.LoadTaskMessages(id)
	return true
}

func (s *Store) ExitView() {
	s.mu.Lock()
	if task, ok := s.tasks[s.viewingTaskID].(*AgentTaskState); ok && !task.Retain {
		task.Messages = nil
		task.DiskLoaded = false
	}
	s.viewingTaskID = ""
	s.mu.Unlock()
}

func (s *Store) ViewingTaskID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewingTaskID
}

// LoadTaskMessages 在视图打开时加载一次 sidechain transcript。
// 之后重复的 TUI 快照复用内存副本,不必每帧重新解析 JSONL 文件。
func (s *Store) LoadTaskMessages(id string) bool {
	s.mu.Lock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.DiskLoaded || task.Base().OutputFile == "" {
		s.mu.Unlock()
		return ok
	}
	path := task.Base().OutputFile
	s.mu.Unlock()
	sm, err := session.Open(path, "")
	if err != nil {
		return false
	}
	messages := sm.BuildContext().Messages()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.tasks[id].(*AgentTaskState)
	if !ok || current.Base().OutputFile != path {
		return false
	}
	current.Messages = append([]llm.Message(nil), messages...)
	current.DiskLoaded = true
	return true
}

// Evict 从易失的根投影中移除终态任务。
// 其 sidechain transcript 和元数据仍留在磁盘上,可供后续查看。
func (s *Store) Evict(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok || !task.Base().Status.Terminal() {
		return false
	}
	delete(s.tasks, id)
	delete(s.runtimes, id)
	if s.viewingTaskID == id {
		s.viewingTaskID = ""
	}
	filtered := s.notifications[:0]
	for _, notification := range s.notifications {
		if notification.TaskID != id {
			filtered = append(filtered, notification)
		}
	}
	s.notifications = filtered
	return true
}

func (s *Store) EnqueueNotification(id string, token uint64, notification TaskNotification) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok || task.Base().LifecycleToken != token || !task.Base().Status.Terminal() || task.Base().Notified {
		return false
	}
	task.Base().Notified = true
	s.notifications = append(s.notifications, notification)
	return true
}

func (s *Store) DrainNotifications() []TaskNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := append([]TaskNotification(nil), s.notifications...)
	s.notifications = nil
	return result
}

func (s *Store) PeekNotifications() []TaskNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TaskNotification(nil), s.notifications...)
}

func (s *Store) AckNotifications(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if count <= 0 {
		return
	}
	if count >= len(s.notifications) {
		s.notifications = nil
		return
	}
	s.notifications = append([]TaskNotification(nil), s.notifications[count:]...)
}

func (s *Store) EnqueuePendingMessage(id string, message llm.UserMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().Status != StateRunning {
		return errors.New("subagent: task is not running")
	}
	message = cloneUserMessage(message)
	task.PendingMessages = append(task.PendingMessages, message)
	task.Messages = append(task.Messages, message)
	return nil
}

// PendingMessages 返回任务本地 FIFO 的防御性快照
func (s *Store) PendingMessages(id string) []llm.UserMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok {
		return nil
	}
	result := make([]llm.UserMessage, len(task.PendingMessages))
	for i, message := range task.PendingMessages {
		result[i] = cloneUserMessage(message)
	}
	return result
}

func (s *Store) ClearPendingMessages(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id].(*AgentTaskState); ok {
		task.PendingMessages = nil
	}
}

func (s *Store) DrainPendingMessages(id string, token uint64) []llm.UserMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StateRunning || len(task.PendingMessages) == 0 {
		return nil
	}
	messages := append([]llm.UserMessage(nil), task.PendingMessages...)
	task.PendingMessages = nil
	return messages
}

func (s *Store) RequeuePendingMessagesFront(id string, token uint64, messages []llm.UserMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StateRunning {
		return false
	}
	queue := cloneUserMessages(messages)
	queue = append(queue, task.PendingMessages...)
	task.PendingMessages = queue
	return true
}

func (s *Store) UpdateAgentProgress(id string, token uint64, update func(*AgentProgress)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StateRunning {
		return false
	}
	if task.Progress == nil {
		task.Progress = &AgentProgress{}
	}
	update(task.Progress)
	return true
}

func (s *Store) RecordAgentMessages(id string, token uint64, messages []llm.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	if task.Base().ID != s.viewingTaskID && !task.Retain {
		// sidechain transcript 是持久化的。内存副本只保留给当前展示中
		// (或被显式 retain)的任务,避免多个长跑 agent 成倍放大父 context。
		task.Messages = nil
		return true
	}
	task.Messages = append([]llm.Message(nil), messages...)
	for _, pending := range task.PendingMessages {
		task.Messages = append(task.Messages, cloneUserMessage(pending))
	}
	return true
}

func (s *Store) CompleteAgent(id string, token uint64, result *AgentToolResult) bool {
	return s.completeAgent(id, token, result, nil)
}

func (s *Store) FailAgent(id string, token uint64, err error) bool {
	return s.completeAgent(id, token, nil, err)
}

func (s *Store) CompleteBash(id string, token uint64, result *BashTaskResult) bool {
	return s.completeBash(id, token, result, nil)
}

func (s *Store) SetBashOutputStats(id string, token uint64, truncated bool, bytes int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*BashTaskState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	task.OutputTruncated = truncated
	task.OutputBytes = bytes
	return true
}

func (s *Store) FailBash(id string, token uint64, result *BashTaskResult, err error) bool {
	return s.completeBash(id, token, result, err)
}

func (s *Store) completeAgent(id string, token uint64, result *AgentToolResult, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StateRunning {
		return false
	}
	now := time.Now()
	task.Base().EndTime = &now
	task.Abort = nil
	task.UnregisterCleanup = nil
	if err != nil {
		task.Base().Status = StateFailed
		task.Error = err.Error()
		if task.Base().ID != s.viewingTaskID && !task.Retain {
			task.Messages = nil
		}
		return true
	}
	task.Base().Status = StateCompleted
	task.Result = result
	if task.Base().ID != s.viewingTaskID && !task.Retain {
		task.Messages = nil
	}
	return true
}

func (s *Store) completeBash(id string, token uint64, result *BashTaskResult, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*BashTaskState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StateRunning {
		return false
	}
	now := time.Now()
	task.Base().EndTime = &now
	task.Process = nil
	if err != nil {
		task.Base().Status = StateFailed
	} else {
		task.Base().Status = StateCompleted
	}
	task.Result = result
	return true
}

func (s *Store) beginAgentKill(id string) (*AgentRuntime, context.CancelFunc, func(), uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	runtime := s.runtimes[id]
	if !ok || runtime == nil || task.Base().Status != StateRunning {
		return nil, nil, nil, 0, &StopTaskError{Code: "not_running", Message: "task is not running"}
	}
	now := time.Now()
	token := task.Base().LifecycleToken
	abort := task.Abort
	cleanup := task.UnregisterCleanup
	task.Base().Status = StateKilled
	task.Base().EndTime = &now
	task.Abort = nil
	task.UnregisterCleanup = nil
	task.PendingMessages = nil
	return runtime, abort, cleanup, token, nil
}

// continuationRuntime 返回当前 runtime 和 token,调用方据此取消它
// 并在替换任务状态前等待 Done。
func (s *Store) continuationRuntime(id string) (*AgentRuntime, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok {
		return nil, 0, &StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s", id)}
	}
	runtime := s.runtimes[id]
	if task.Base().Status.Terminal() {
		return runtime, task.Base().LifecycleToken, nil
	}
	if runtime == nil {
		return nil, 0, errors.New("subagent: running task has no runtime")
	}
	return runtime, task.Base().LifecycleToken, nil
}

// RegisterContinuation 替换任务的运行中 lifecycle,同时保留稳定 task ID
// 和 Claude 风格的 UI/transcript 字段。
func (s *Store) RegisterContinuation(id string, runtime *AgentRuntime, prompt string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.tasks[id].(*AgentTaskState)
	if !ok {
		return 0, &StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s", id)}
	}
	if old.Base().Status == StateRunning {
		return 0, errors.New("subagent: task is still running")
	}
	s.nextToken++
	token := s.nextToken
	state := &AgentTaskState{
		TaskStateBase: TaskStateBase{
			ID:             old.Base().ID,
			Type:           TaskTypeAgent,
			Status:         StateRunning,
			Description:    old.Base().Description,
			ToolUseID:      old.Base().ToolUseID,
			StartTime:      old.Base().StartTime,
			OutputFile:     old.Base().OutputFile,
			OutputOffset:   old.Base().OutputOffset,
			LifecycleToken: token,
		},
		AgentID:             old.AgentID,
		Prompt:              prompt,
		AgentType:           old.AgentType,
		SelectedAgent:       old.SelectedAgent,
		Model:               old.Model,
		Thinking:            old.Thinking,
		WorktreePath:        old.WorktreePath,
		Worktree:            old.Worktree,
		Progress:            old.Progress,
		PendingMessages:     nil,
		Retain:              old.Retain,
		DiskLoaded:          old.DiskLoaded,
		Messages:            append([]llm.Message(nil), old.Messages...),
		ContentReplacements: cloneContentReplacements(old.ContentReplacements),
		EvictAfter:          old.EvictAfter,
	}
	s.tasks[id] = state
	if runtime != nil {
		runtime.LifecycleToken = token
		s.runtimes[id] = runtime
	}
	return token, nil
}

func cloneUserMessages(messages []llm.UserMessage) []llm.UserMessage {
	result := make([]llm.UserMessage, len(messages))
	for i, message := range messages {
		result[i] = cloneUserMessage(message)
	}
	return result
}

func cloneModel(model *llm.Model) *llm.Model {
	if model == nil {
		return nil
	}
	copy := *model
	return &copy
}

func cloneContentReplacements(source map[string][]llm.Content) map[string][]llm.Content {
	if source == nil {
		return nil
	}
	result := make(map[string][]llm.Content, len(source))
	for key, content := range source {
		copy := make([]llm.Content, len(content))
		copy = append(copy[:0], content...)
		result[key] = copy
	}
	return result
}

func (s *Store) beginBashKill(id string) (*BashTaskState, BashProcess, func(), uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*BashTaskState)
	if !ok || task.Base().Status != StateRunning {
		return nil, nil, nil, 0, &StopTaskError{Code: "not_running", Message: "task is not running"}
	}
	now := time.Now()
	token := task.Base().LifecycleToken
	process := task.Process
	cleanup := task.Unregister
	task.Base().Status = StateKilled
	task.Base().EndTime = &now
	task.Base().Notified = true
	task.Result = &BashTaskResult{Code: 137, Interrupted: true}
	task.Process = nil
	task.Unregister = nil
	if task.CleanupTimeout != nil {
		task.CleanupTimeout.Stop()
		task.CleanupTimeout = nil
	}
	return task, process, cleanup, token, nil
}

func (s *Store) restoreBashKill(task *BashTaskState, process BashProcess, token uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.tasks[task.Base().ID].(*BashTaskState)
	if !ok || current.Base().LifecycleToken != token || current.Base().Status != StateKilled {
		return false
	}
	current.Base().Status = StateRunning
	current.Base().EndTime = nil
	current.Base().Notified = false
	current.Result = nil
	current.Process = process
	return true
}

func (s *Store) removeRuntime(id string, token uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id]; ok && task.Base().LifecycleToken == token {
		delete(s.runtimes, id)
	}
}

func (s *Store) attachAgentRuntime(id string, runtime *AgentRuntime, prompt string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().Status != StateRunning {
		return 0, errors.New("subagent: task is not running")
	}
	task.Prompt = prompt
	task.Abort = runtime.Cancel
	s.runtimes[id] = runtime
	return task.Base().LifecycleToken, nil
}

func (s *Store) setRuntimeMetadata(id string, token uint64, runtime *AgentRuntime) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	if runtime.OutputFile != "" {
		task.Base().OutputFile = runtime.OutputFile
	}
	if runtime.WorktreePath != "" {
		task.WorktreePath = runtime.WorktreePath
	}
	if runtime.ContentReplacementState != nil {
		task.ContentReplacements = cloneContentReplacements(runtime.ContentReplacementState)
	}
	return true
}

func (s *Store) ClearWorktreePath(id string, token uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentTaskState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	task.WorktreePath = ""
	return true
}

func (s *Store) failReserved(id string, _ error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok || task.Base().Status != StateRunning {
		return false
	}
	delete(s.tasks, id)
	delete(s.runtimes, id)
	return true
}

func cloneUserMessage(message llm.UserMessage) llm.UserMessage {
	message.Content = cloneContent(message.Content)
	return message
}

func cloneContent(content []llm.Content) []llm.Content {
	result := make([]llm.Content, len(content))
	for i, item := range content {
		switch value := item.(type) {
		case llm.ToolCall:
			copy := value
			copy.Arguments = make(map[string]any, len(value.Arguments))
			for key, argument := range value.Arguments {
				copy.Arguments[key] = argument
			}
			result[i] = copy
		default:
			result[i] = item
		}
	}
	return result
}

func cloneTaskState(task TaskState) TaskState {
	switch state := task.(type) {
	case *BashTaskState:
		copy := *state
		copy.Process = nil
		copy.Unregister = nil
		return &copy
	case *AgentTaskState:
		copy := *state
		copy.Abort = nil
		copy.UnregisterCleanup = nil
		copy.PendingMessages = append([]llm.UserMessage(nil), state.PendingMessages...)
		// Messages 是不可变追加序列:写入方只 append 新消息,从不改写已有元素,
		// 因此共享底层数组安全,避免每次 snapshot 复制整份 transcript
		copy.Messages = state.Messages
		copy.ContentReplacements = cloneContentReplacements(state.ContentReplacements)
		copy.Model = cloneModel(state.Model)
		if state.Progress != nil {
			progress := *state.Progress
			progress.RecentActivities = append([]ToolActivity(nil), state.Progress.RecentActivities...)
			if state.Progress.LastActivity != nil {
				activity := *state.Progress.LastActivity
				progress.LastActivity = &activity
			}
			copy.Progress = &progress
		}
		return &copy
	default:
		return task
	}
}
