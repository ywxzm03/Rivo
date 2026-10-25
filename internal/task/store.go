package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// panelGracePeriod 是终态任务在根投影中保留的时长,
// 让 TUI 面板有机会展示结束状态后再自动驱逐
const panelGracePeriod = 30 * time.Second

// Store 是任务状态、lifecycle runtime 句柄和通知的唯一所有者
type Store struct {
	mu            sync.Mutex
	tasks         map[string]State
	runtimes      map[string]*AgentRuntime
	notifications []Notification
	nextID        uint64
	nextToken     uint64
	viewingTaskID string
}

func NewStore() *Store {
	return &Store{tasks: make(map[string]State), runtimes: make(map[string]*AgentRuntime)}
}

type RegisterSpec struct {
	ID           string
	Type         Type
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
	BashState    *BashState
	AgentState   *AgentState
}

func (s *Store) Register(spec RegisterSpec) (string, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if spec.Type != TypeBash && spec.Type != TypeAgent {
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
		if existing.Base().Status == StatusRunning {
			return "", 0, fmt.Errorf("subagent: task %s is already running", id)
		}
		return "", 0, fmt.Errorf("subagent: task %s already exists; use continuation", id)
	}
	s.nextToken++
	token := s.nextToken
	base := StateBase{ID: id, Type: spec.Type, Status: StatusRunning, Description: spec.Description, ToolUseID: spec.ToolUseID, StartTime: time.Now(), OutputFile: spec.OutputFile, LifecycleToken: token}
	var state State
	switch spec.Type {
	case TypeBash:
		if spec.BashState == nil {
			spec.BashState = &BashState{}
		}
		spec.BashState.StateBase = base
		state = spec.BashState
	case TypeAgent:
		if spec.AgentState == nil {
			spec.AgentState = &AgentState{}
		}
		spec.AgentState.StateBase = base
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

func (s *Store) Task(id string) (State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	return cloneTaskState(task), true
}

func (s *Store) Snapshot() []State {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]State, 0, len(s.tasks))
	for _, task := range s.tasks {
		result = append(result, cloneTaskState(task))
	}
	return result
}

// EnterView 记录根 TUI 正在显示哪个任务的 transcript。
// viewing 只是展示层元数据,绝不启动或恢复任务。
func (s *Store) EnterView(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return false
	}
	s.viewingTaskID = id
	return true
}

func (s *Store) ExitView() {
	s.mu.Lock()
	s.viewingTaskID = ""
	s.mu.Unlock()
}

func (s *Store) ViewingTaskID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewingTaskID
}

// ReadTaskMessages 从任务的 sidechain transcript 按字节偏移增量读取消息,
// 返回消息和新的偏移。transcript 常驻磁盘,内存里不保留完整副本;
// 调用方缓存已投影的前缀,只消费新增部分。
// sidechain 不做压缩,因此按文件序读取 message entry 就等于它的展示上下文。
func (s *Store) ReadTaskMessages(id string, offset int64) ([]llm.Message, int64, error) {
	s.mu.Lock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok {
		s.mu.Unlock()
		return nil, offset, fmt.Errorf("subagent: task %s not found", id)
	}
	path := task.Base().OutputFile
	s.mu.Unlock()
	if path == "" {
		return nil, offset, nil
	}
	messages, next, err := session.ReadMessages(path, offset)
	if errors.Is(err, os.ErrNotExist) {
		// transcript 在首条消息落盘前还不存在,按空增量处理
		return nil, offset, nil
	}
	return messages, next, err
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
	s.evictLocked(id)
	return true
}

// EvictExpired 驱逐宽限期已过、且通知已投递过的终态任务,返回驱逐数量
func (s *Store) EvictExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	evicted := 0
	for id, state := range s.tasks {
		agent, ok := state.(*AgentState)
		if !ok || !agent.Base().Status.Terminal() || !agent.Base().Notified || agent.EvictAfter == nil {
			continue
		}
		if now.Before(*agent.EvictAfter) {
			continue
		}
		// 正在查看的 transcript 不自动回收,否则面板会在阅读中途消失;
		// ExitView 之后的下一次清扫即可回收
		if id == s.viewingTaskID {
			continue
		}
		s.evictLocked(id)
		evicted++
	}
	return evicted
}

// evictLocked 从根投影移除任务,调用方必须持有 s.mu
func (s *Store) evictLocked(id string) {
	delete(s.tasks, id)
	delete(s.runtimes, id)
	if s.viewingTaskID == id {
		s.viewingTaskID = ""
	}
	// 不删除 s.notifications 中该任务的条目:驱逐时通知可能还没被模型消费,
	// 一并删掉父 agent 就永远收不到结果。队列与任务 registry 解耦,
	// 通知只由 AckNotifications 移除
}

func (s *Store) EnqueueNotification(id string, token uint64, notification Notification) bool {
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

func (s *Store) DrainNotifications() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := append([]Notification(nil), s.notifications...)
	s.notifications = nil
	return result
}

func (s *Store) PeekNotifications() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Notification(nil), s.notifications...)
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
	s.notifications = append([]Notification(nil), s.notifications[count:]...)
}

func (s *Store) EnqueuePendingMessage(id string, message llm.UserMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().Status != StatusRunning {
		return errors.New("subagent: task is not running")
	}
	message = cloneUserMessage(message)
	task.PendingMessages = append(task.PendingMessages, message)
	return nil
}

// PendingMessages 返回任务本地 FIFO 的防御性快照
func (s *Store) PendingMessages(id string) []llm.UserMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
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
	if task, ok := s.tasks[id].(*AgentState); ok {
		task.PendingMessages = nil
	}
}

func (s *Store) DrainPendingMessages(id string, token uint64) []llm.UserMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StatusRunning || len(task.PendingMessages) == 0 {
		return nil
	}
	messages := append([]llm.UserMessage(nil), task.PendingMessages...)
	task.PendingMessages = nil
	return messages
}

func (s *Store) RequeuePendingMessagesFront(id string, token uint64, messages []llm.UserMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StatusRunning {
		return false
	}
	queue := CloneUserMessages(messages)
	queue = append(queue, task.PendingMessages...)
	task.PendingMessages = queue
	return true
}

func (s *Store) UpdateAgentProgress(id string, token uint64, update func(*AgentProgress)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StatusRunning {
		return false
	}
	if task.Progress == nil {
		task.Progress = &AgentProgress{}
	}
	update(task.Progress)
	return true
}

// RecordAgentMessages 记录 sidechain transcript 的消息条数。
// 消息本身只落盘,内存不常驻完整 transcript;TUI 需要时按偏移从磁盘增量读取。
func (s *Store) RecordAgentMessages(id string, token uint64, messages []llm.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	task.MessageCount = len(messages)
	return true
}

func (s *Store) CompleteAgent(id string, token uint64, result *AgentResult) bool {
	return s.completeAgent(id, token, result, nil)
}

func (s *Store) FailAgent(id string, token uint64, err error) bool {
	return s.completeAgent(id, token, nil, err)
}

func (s *Store) CompleteBash(id string, token uint64, result *BashResult) bool {
	return s.completeBash(id, token, result, nil)
}

func (s *Store) SetBashOutputStats(id string, token uint64, truncated bool, bytes int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*BashState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	task.OutputTruncated = truncated
	task.OutputBytes = bytes
	return true
}

func (s *Store) FailBash(id string, token uint64, result *BashResult, err error) bool {
	return s.completeBash(id, token, result, err)
}

func (s *Store) completeAgent(id string, token uint64, result *AgentResult, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StatusRunning {
		return false
	}
	now := time.Now()
	task.Base().EndTime = &now
	task.Abort = nil
	task.UnregisterCleanup = nil
	setEvictAfter(task, now)
	if err != nil {
		task.Base().Status = StatusFailed
		task.Error = err.Error()
		return true
	}
	task.Base().Status = StatusCompleted
	task.Result = result
	return true
}

func (s *Store) completeBash(id string, token uint64, result *BashResult, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*BashState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StatusRunning {
		return false
	}
	now := time.Now()
	task.Base().EndTime = &now
	task.Process = nil
	if err != nil {
		task.Base().Status = StatusFailed
	} else {
		task.Base().Status = StatusCompleted
	}
	task.Result = result
	return true
}

func (s *Store) beginAgentKill(id string) (*AgentRuntime, context.CancelFunc, func(), uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().Status != StatusRunning {
		return nil, nil, nil, 0, &StopTaskError{Code: "not_running", Message: "task is not running"}
	}
	now := time.Now()
	token := task.Base().LifecycleToken
	abort := task.Abort
	cleanup := task.UnregisterCleanup
	// runtime 要等 AttachAgentRuntime 才挂上,启动窗口内它是 nil。
	// 此时状态照常转移,runtime 缺失只意味着没有可取消的句柄;
	// 后续 AttachAgentRuntime 会因任务已非 running 而失败。
	runtime := s.runtimes[id]
	if runtime == nil {
		// 没有 lifecycle 就再也没有回调能投递通知,直接标记已通知,
		// 否则这个终态任务永远不满足驱逐条件
		task.Base().Notified = true
	}
	task.Base().Status = StatusKilled
	task.Base().EndTime = &now
	task.Abort = nil
	task.UnregisterCleanup = nil
	task.PendingMessages = nil
	setEvictAfter(task, now)
	return runtime, abort, cleanup, token, nil
}

// setEvictAfter 在终态转移时写入面板宽限期;Retain 任务永不驱逐
func setEvictAfter(task *AgentState, now time.Time) {
	if task.Retain {
		task.EvictAfter = nil
		return
	}
	deadline := now.Add(panelGracePeriod)
	task.EvictAfter = &deadline
}

// ContinuationRuntime 返回当前 runtime 和 token,调用方据此取消它
// 并在替换任务状态前等待 Done。
func (s *Store) ContinuationRuntime(id string) (*AgentRuntime, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
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
	old, ok := s.tasks[id].(*AgentState)
	if !ok {
		return 0, &StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s", id)}
	}
	if old.Base().Status == StatusRunning {
		return 0, errors.New("subagent: task is still running")
	}
	s.nextToken++
	token := s.nextToken
	state := &AgentState{
		StateBase: StateBase{
			ID:             old.Base().ID,
			Type:           TypeAgent,
			Status:         StatusRunning,
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
		MessageCount:        old.MessageCount,
		ContentReplacements: cloneContentReplacements(old.ContentReplacements),
	}
	s.tasks[id] = state
	if runtime != nil {
		runtime.LifecycleToken = token
		s.runtimes[id] = runtime
	}
	return token, nil
}

func CloneUserMessages(messages []llm.UserMessage) []llm.UserMessage {
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
		result[key] = llm.CloneContent(content)
	}
	return result
}

func (s *Store) beginBashKill(id string) (*BashState, BashProcess, func(), uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*BashState)
	if !ok || task.Base().Status != StatusRunning {
		return nil, nil, nil, 0, &StopTaskError{Code: "not_running", Message: "task is not running"}
	}
	now := time.Now()
	token := task.Base().LifecycleToken
	process := task.Process
	cleanup := task.Unregister
	task.Base().Status = StatusKilled
	task.Base().EndTime = &now
	task.Base().Notified = true
	task.Result = &BashResult{Code: 137, Interrupted: true}
	task.Process = nil
	task.Unregister = nil
	if task.CleanupTimeout != nil {
		task.CleanupTimeout.Stop()
		task.CleanupTimeout = nil
	}
	return task, process, cleanup, token, nil
}

func (s *Store) restoreBashKill(task *BashState, process BashProcess, token uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.tasks[task.Base().ID].(*BashState)
	if !ok || current.Base().LifecycleToken != token || current.Base().Status != StatusKilled {
		return false
	}
	current.Base().Status = StatusRunning
	current.Base().EndTime = nil
	current.Base().Notified = false
	current.Result = nil
	current.Process = process
	return true
}

func (s *Store) RemoveRuntime(id string, token uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id]; ok && task.Base().LifecycleToken == token {
		delete(s.runtimes, id)
	}
}

func (s *Store) AttachAgentRuntime(id string, runtime *AgentRuntime, prompt string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().Status != StatusRunning {
		return 0, errors.New("subagent: task is not running")
	}
	task.Prompt = prompt
	task.Abort = runtime.Cancel
	s.runtimes[id] = runtime
	return task.Base().LifecycleToken, nil
}

func (s *Store) SetRuntimeMetadata(id string, token uint64, runtime *AgentRuntime) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
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
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	task.WorktreePath = ""
	return true
}

// DiscardReserved 丢弃启动失败时占位的任务,不留终态记录
func (s *Store) DiscardReserved(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok || task.Base().Status != StatusRunning {
		return false
	}
	delete(s.tasks, id)
	delete(s.runtimes, id)
	return true
}

func cloneUserMessage(message llm.UserMessage) llm.UserMessage {
	message.Content = llm.CloneContent(message.Content)
	return message
}

func cloneTaskState(task State) State {
	switch state := task.(type) {
	case *BashState:
		copy := *state
		copy.Process = nil
		copy.Unregister = nil
		return &copy
	case *AgentState:
		copy := *state
		copy.Abort = nil
		copy.UnregisterCleanup = nil
		copy.PendingMessages = append([]llm.UserMessage(nil), state.PendingMessages...)
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
