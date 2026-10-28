package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// defaultGracePeriod 是终态任务在根投影中保留的默认时长,
// 让 TUI 面板有机会展示结束状态后再自动驱逐
const defaultGracePeriod = 30 * time.Second

// nextTaskSeq 是进程级任务序号，保证同一进程内不同 Store 生成的 ID 不重复
var nextTaskSeq atomic.Uint64

// Store 是任务状态、lifecycle runtime 句柄和通知的唯一所有者
type Store struct {
	mu            sync.Mutex
	tasks         map[string]State
	runtimes      map[string]Runtime
	notifications []Notification
	nextToken     uint64
	viewingTaskID string
	// notify 是通知入队时的唤醒回调。主 agent 空闲时靠它启动消费通知的 run,
	// 否则通知要等用户下次输入才投递
	notify func()
	// readTranscript 按字节偏移增量读取 sidechain transcript。
	// 由上层注入,避免任务层反向依赖会话持久化层
	readTranscript func(path string, offset int64) ([]llm.Message, int64, error)

	// GracePeriod 是终态任务的驱逐宽限期，零值表示用 defaultGracePeriod。
	// 测试注入毫秒级宽限期，避免真等 30 秒
	GracePeriod time.Duration

	// 下面三组子结构不持有独立锁，复用 Store 的锁与状态，
	// 只是把平铺方法按职责分组
	Notifications NotificationQueue
	Pending       PendingQueues
	Views         TaskViews
}

func NewStore() *Store {
	store := &Store{tasks: make(map[string]State), runtimes: make(map[string]Runtime)}
	store.Notifications = NotificationQueue{store: store}
	store.Pending = PendingQueues{store: store}
	store.Views = TaskViews{store: store}
	return store
}

// gracePeriod 返回生效的宽限期，零值时用默认值
func (s *Store) gracePeriod() time.Duration {
	if s.GracePeriod > 0 {
		return s.GracePeriod
	}
	return defaultGracePeriod
}

// tokenOf 返回任务当前的 lifecycle token，供子结构复用既有入口
func (s *Store) tokenOf(id string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return 0
	}
	return task.Base().LifecycleToken
}

// SetNotifyHook 安装通知入队回调。回调在锁外执行,避免宿主侧逻辑与 store 锁互等
func (s *Store) SetNotifyHook(hook func()) {
	s.mu.Lock()
	s.notify = hook
	s.mu.Unlock()
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
	Runtime      Runtime
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
		// task ID 同时也是 sidechain transcript 的文件名。加入进程时间分量,
		// 新起的 Rivo 进程就不会重开旧的 agent-1.jsonl 并在首次启动时
		// 因 O_EXCL 失败。序号取进程级计数器,同一进程内多个 Store
		// (每次 /new 都新建一个)才不会在同一纳秒撞出相同 ID
		id = fmt.Sprintf("%s-%d-%d", spec.Type, time.Now().UnixNano(), nextTaskSeq.Add(1))
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
		spec.AgentState.Model = cloneModelPtr(spec.Model)
		spec.AgentState.Thinking = spec.Thinking
		spec.AgentState.WorktreePath = spec.WorktreePath
		spec.AgentState.Worktree = spec.Worktree
		state = spec.AgentState
	}
	s.tasks[id] = state
	if spec.Runtime != nil {
		spec.Runtime.SetToken(token)
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
// 同时给任务打上 Retain:用户正在读的 transcript 不能被驱逐,
// 否则内容会在阅读中途消失
func (s *Store) EnterView(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok {
		return false
	}
	// 直接从一个任务切到另一个时没有 exit 请求,旧任务的 Retain 必须在这里释放,
	// 否则它永远不会被驱逐
	if previous, ok := s.tasks[s.viewingTaskID].(*AgentState); ok && previous != task {
		previous.Retain = false
		if previous.Base().Status.Terminal() {
			setEvictAfter(&previous.StateBase, false, time.Now(), s.gracePeriod())
		}
	}
	s.viewingTaskID = id
	task.Retain = true
	task.EvictAfter = nil
	return true
}

// ExitView 退出视图,并把 Retain 任务交还给宽限期策略
func (s *Store) ExitView() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewingTaskID = ""
	// 只有仍在终态的 agent 任务需要重新排期;运行中的任务不参与驱逐
	for _, state := range s.tasks {
		task, ok := state.(*AgentState)
		if !ok || !task.Retain {
			continue
		}
		task.Retain = false
		if task.Base().Status.Terminal() {
			setEvictAfter(&task.StateBase, false, time.Now(), s.gracePeriod())
		}
	}
}

func (s *Store) ViewingTaskID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewingTaskID
}

// SetTranscriptReader 注入 sidechain transcript 的读取实现
func (s *Store) SetTranscriptReader(read func(path string, offset int64) ([]llm.Message, int64, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readTranscript = read
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
	if s.readTranscript == nil {
		return nil, offset, errors.New("subagent: transcript reader is not configured")
	}
	messages, next, err := s.readTranscript(path, offset)
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
	task, ok := s.tasks[id]
	if !ok || !task.Base().Status.Terminal() {
		s.mu.Unlock()
		return false
	}
	// 正在查看的 transcript 不能手动驱逐,否则面板会在阅读中途消失
	if agent, ok := task.(*AgentState); ok && agent.Retain {
		s.mu.Unlock()
		return false
	}
	s.evictLocked(id)
	s.mu.Unlock()
	return true
}

// EvictExpired 驱逐宽限期已过、且通知已投递过的终态任务,返回驱逐数量。
// bash 与 agent 共用同一套策略:终态 + 已通知 + 过了宽限期
// HasEvictionWork 报告是否有等待宽限期的终态任务。
// 驱逐循环据此在空闲时停止轮询，而不是每秒空转
func (s *Store) HasEvictionWork() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, state := range s.tasks {
		base := state.Base()
		if base.Status.Terminal() && base.NotificationDelivered && base.EvictAfter != nil {
			return true
		}
	}
	return false
}

func (s *Store) EvictExpired() int {
	s.mu.Lock()
	now := time.Now()
	evicted := 0
	for id, state := range s.tasks {
		base := state.Base()
		if !base.Status.Terminal() || !base.NotificationDelivered {
			continue
		}
		// 用户正在查看的 transcript 永不自动回收,否则面板会在阅读中途消失
		if agent, ok := state.(*AgentState); ok && agent.Retain {
			continue
		}
		// EvictAfter 为 nil 表示永不驱逐:Retain 任务和尚未进入终态的路径都靠它
		if base.EvictAfter == nil || now.Before(*base.EvictAfter) {
			continue
		}
		s.evictLocked(id)
		evicted++
	}
	s.mu.Unlock()
	return evicted
}

// KillAllRunningTasks 终止全部运行中的任务。
// 主 agent 取消或进程退出时调用:子任务的 lifecycle 已与请求解绑,
// 没有这条路径它们会一直跑到自己结束
func (s *Store) KillAllRunningTasks() int {
	s.mu.Lock()
	var agentIDs, bashIDs []string
	for id, state := range s.tasks {
		if state.Base().Status != StatusRunning {
			continue
		}
		switch state.(type) {
		case *AgentState:
			agentIDs = append(agentIDs, id)
		case *BashState:
			bashIDs = append(bashIDs, id)
		}
	}
	runtimes := make([]Runtime, 0, len(agentIDs))
	for _, id := range agentIDs {
		runtimes = append(runtimes, s.runtimes[id])
	}
	s.mu.Unlock()
	killed := 0
	for _, id := range agentIDs {
		if err := KillAgentWithReason(id, s, StopReasonShutdown); err == nil {
			killed++
		}
	}
	// 取消只发信号。等 lifecycle 真正收尾,子 agent 才会在退出前
	// 走完工具层的进程树清理
	for _, runtime := range runtimes {
		if runtime != nil {
			select {
			case <-runtime.Done():
			case <-time.After(shutdownWait):
			}
		}
	}
	for _, id := range bashIDs {
		if err := KillBash(id, s); err == nil {
			killed++
		}
	}
	return killed
}

// shutdownWait 是退出时等待子任务收尾的上限
const shutdownWait = 3 * time.Second

// KillAllRunningBashTasks 终止全部运行中的后台 bash 任务,返回成功终止的数量。
// /stop 只停 shell:子 agent 有自己的取消链路,不能被这条用户命令连带终止
func (s *Store) KillAllRunningBashTasks() int {
	s.mu.Lock()
	var ids []string
	for id, state := range s.tasks {
		if _, ok := state.(*BashState); !ok || state.Base().Status != StatusRunning {
			continue
		}
		ids = append(ids, id)
	}
	s.mu.Unlock()
	killed := 0
	for _, id := range ids {
		if err := KillBash(id, s); err == nil {
			killed++
		}
	}
	return killed
}

// KillBashTasksForAgent 终止某子 agent 启动且仍在运行的后台 bash 任务,
// 返回成功终止的数量。子 agent 结束后它的 query loop 不再运行,
// 这些任务没人能再停止,因此必须在子 agent 收尾时回收
func (s *Store) KillBashTasksForAgent(agentID string) int {
	if agentID == "" {
		return 0
	}
	s.mu.Lock()
	var ids []string
	for id, state := range s.tasks {
		bash, ok := state.(*BashState)
		if !ok || bash.ParentAgentID != agentID || bash.Base().Status != StatusRunning {
			continue
		}
		ids = append(ids, id)
	}
	s.mu.Unlock()
	killed := 0
	for _, id := range ids {
		if err := KillBash(id, s); err == nil {
			killed++
		}
	}
	return killed
}

// evictLocked 从根投影移除任务,调用方必须持有 s.mu。
// bash 的临时输出文件不在这里删除:父 agent 要先读到通知里的路径,
// 日志的寿命由 AckNotifications 决定
func (s *Store) evictLocked(id string) {
	delete(s.tasks, id)
	delete(s.runtimes, id)
	if s.viewingTaskID == id {
		s.viewingTaskID = ""
	}
	// 不删除 s.notifications 中该任务的条目:驱逐时通知可能还没被模型消费,
	// 一并删掉父 agent 就永远收不到结果。队列与 task registry 解耦,
	// 通知只由 AckNotifications 移除
}

func (s *Store) EnqueueNotification(id string, token uint64, notification Notification) bool {
	s.mu.Lock()
	task, ok := s.tasks[id]
	if !ok || task.Base().LifecycleToken != token || !task.Base().Status.Terminal() || task.Base().Notified {
		s.mu.Unlock()
		return false
	}
	// Notified 只做入队去重;能否驱逐看 NotificationDelivered,
	// 它在通知真正落盘后才置位,任务行因此能保留到送达之后
	task.Base().Notified = true
	s.notifications = append(s.notifications, notification)
	notify := s.notify
	s.mu.Unlock()
	// 在锁外唤醒宿主:回调会去启动 run,持锁调用会与 run 的请求准备互等
	if notify != nil {
		notify()
	}
	return true
}

// DrainNotifications 取走全部通知并清空队列
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

// ClaimResult 是一次通知认领的结果
type ClaimResult struct {
	Notifications []Notification
	Count         int
}

// ClaimNotifications 认领当前队列里的全部通知但不移除,
// 供调用方在落盘成功后再 AckNotifications。落盘失败时通知仍在队列里
func (s *Store) ClaimNotifications() ClaimResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	claimed := append([]Notification(nil), s.notifications...)
	return ClaimResult{Notifications: claimed, Count: len(claimed)}
}

// MarkNotified 把指定任务标记为通知已落盘送达,之后才允许被 EvictExpired 回收
func (s *Store) MarkNotified(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if task, ok := s.tasks[id]; ok {
			task.Base().NotificationDelivered = true
		}
	}
}

// AckNotifications 确认前 count 条通知已被模型消费。
// 通知引用的输出文件不在这里删除:模型可能下一轮才去读它,
// 后台 bash 日志保留在系统临时目录,由操作系统清理
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

// EnqueuePendingMessage 把输入排进任务的 steering 队列,
// 在下一个工具调用边界注入,与主 agent 的 steering 语义一致
func (s *Store) EnqueuePendingMessage(id string, message llm.UserMessage) error {
	return s.EnqueueTaskMessage(id, message, false)
}

// EnqueueFollowUpMessage 把输入排进任务的 follow-up 队列,
// 在当前轮结束后注入
func (s *Store) EnqueueFollowUpMessage(id string, message llm.UserMessage) error {
	return s.EnqueueTaskMessage(id, message, true)
}

func (s *Store) EnqueueTaskMessage(id string, message llm.UserMessage, followUp bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().Status != StatusRunning {
		return errors.New("subagent: task is not running")
	}
	message = cloneUserMessage(message)
	if followUp {
		task.PendingFollowUp = append(task.PendingFollowUp, message)
	} else {
		task.PendingSteering = append(task.PendingSteering, message)
	}
	return nil
}

// PendingMessages 返回 steering 队列的防御性快照
func (s *Store) PendingMessages(id string) []llm.UserMessage {
	return s.pendingMessages(id, false)
}

// PendingFollowUpMessages 返回 follow-up 队列的防御性快照
func (s *Store) PendingFollowUpMessages(id string) []llm.UserMessage {
	return s.pendingMessages(id, true)
}

func (s *Store) pendingMessages(id string, followUp bool) []llm.UserMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok {
		return nil
	}
	queue := task.PendingSteering
	if followUp {
		queue = task.PendingFollowUp
	}
	result := make([]llm.UserMessage, len(queue))
	for i, message := range queue {
		result[i] = cloneUserMessage(message)
	}
	return result
}

// DrainPendingMessages 取走 steering 队列
func (s *Store) DrainPendingMessages(id string, token uint64) []llm.UserMessage {
	return s.drainTaskMessages(id, token, false)
}

// DrainPendingFollowUpMessages 取走 follow-up 队列
func (s *Store) DrainPendingFollowUpMessages(id string, token uint64) []llm.UserMessage {
	return s.drainTaskMessages(id, token, true)
}

func (s *Store) drainTaskMessages(id string, token uint64, followUp bool) []llm.UserMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StatusRunning {
		return nil
	}
	queue := task.PendingSteering
	if followUp {
		queue = task.PendingFollowUp
	}
	if len(queue) == 0 {
		return nil
	}
	messages := append([]llm.UserMessage(nil), queue...)
	if followUp {
		task.PendingFollowUp = nil
	} else {
		task.PendingSteering = nil
	}
	return messages
}

// ClearSteeringMessages 清空 steering 队列,用于 TUI 撤回
func (s *Store) ClearSteeringMessages(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id].(*AgentState); ok {
		task.PendingSteering = nil
	}
}

// ClearFollowUpMessages 清空 follow-up 队列,用于 TUI 撤回
func (s *Store) ClearFollowUpMessages(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, ok := s.tasks[id].(*AgentState); ok {
		task.PendingFollowUp = nil
	}
}

// RequeuePendingMessagesFront 把未投递的 steering 放回队列头部
func (s *Store) RequeuePendingMessagesFront(id string, token uint64, messages []llm.UserMessage) bool {
	return s.requeueTaskMessages(id, token, messages, false)
}

// RequeuePendingFollowUpMessagesFront 把未投递的 follow-up 放回队列头部
func (s *Store) RequeuePendingFollowUpMessagesFront(id string, token uint64, messages []llm.UserMessage) bool {
	return s.requeueTaskMessages(id, token, messages, true)
}

func (s *Store) requeueTaskMessages(id string, token uint64, messages []llm.UserMessage, followUp bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token || task.Base().Status != StatusRunning {
		return false
	}
	queue := CloneUserMessages(messages)
	if followUp {
		task.PendingFollowUp = append(queue, task.PendingFollowUp...)
	} else {
		task.PendingSteering = append(queue, task.PendingSteering...)
	}
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
	setEvictAfter(&task.StateBase, task.Retain, now, s.gracePeriod())
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
	// bash 与 agent 共用面板宽限期,通知投递后由 EvictExpired 回收
	setEvictAfter(&task.StateBase, false, now, s.gracePeriod())
	return true
}

func (s *Store) beginAgentKill(id string, reason StopReason) (Runtime, context.CancelFunc, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().Status != StatusRunning {
		return nil, nil, 0, &StopTaskError{Code: "not_running", Message: "task is not running"}
	}
	now := time.Now()
	token := task.Base().LifecycleToken
	abort := task.Abort
	// 启动窗口内没有 runtime 时仍要保留通知资格,KillAgent 会立即入队 stopped 通知
	runtime := s.runtimes[id]
	task.Base().Status = StatusKilled
	task.Base().StopReason = reason
	task.Base().EndTime = &now
	task.Abort = nil
	task.PendingSteering = nil
	task.PendingFollowUp = nil
	// 启动窗口内没有 runtime 时不会有通知入队,直接视为已送达,
	// 否则任务行永远不会被回收
	if runtime == nil {
		task.Base().NotificationDelivered = true
	}
	setEvictAfter(&task.StateBase, task.Retain, now, s.gracePeriod())
	return runtime, abort, token, nil
}

// setEvictAfter 在终态转移时写入面板宽限期;Retain 任务永不驱逐
func setEvictAfter(base *StateBase, retain bool, now time.Time, gracePeriod time.Duration) {
	if retain {
		base.EvictAfter = nil
		return
	}
	deadline := now.Add(gracePeriod)
	base.EvictAfter = &deadline
}

// ContinuationRuntime 返回当前 runtime 和 token,调用方据此取消它
// 并在替换任务状态前等待 Done。
func (s *Store) ContinuationRuntime(id string) (Runtime, uint64, error) {
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
func (s *Store) RegisterContinuation(id string, runtime Runtime, prompt string) (uint64, error) {
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
			LifecycleToken: token,
		},
		AgentID:      old.AgentID,
		Prompt:       prompt,
		AgentType:    old.AgentType,
		Model:        cloneModelPtr(old.Model),
		Thinking:     old.Thinking,
		WorktreePath: old.WorktreePath,
		Worktree:     old.Worktree,
		Progress:     old.Progress,
		Retain:       old.Retain,
		MessageCount: old.MessageCount,
	}
	s.tasks[id] = state
	if runtime != nil {
		runtime.SetToken(token)
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

// cloneModel 深拷贝模型配置,隔离可变 map 与指针字段

func (s *Store) beginBashKill(id string) (*BashState, BashProcess, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*BashState)
	if !ok || task.Base().Status != StatusRunning {
		return nil, nil, 0, &StopTaskError{Code: "not_running", Message: "task is not running"}
	}
	now := time.Now()
	token := task.Base().LifecycleToken
	process := task.Process
	task.Base().Status = StatusKilled
	task.Base().EndTime = &now
	task.Base().Notified = true
	task.Result = &BashResult{Code: 137, Interrupted: true}
	task.Process = nil
	setEvictAfter(&task.StateBase, false, now, s.gracePeriod())
	return task, process, token, nil
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
	current.Base().EvictAfter = nil
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

func (s *Store) AttachAgentRuntime(id string, runtime Runtime, prompt string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().Status != StatusRunning {
		return 0, errors.New("subagent: task is not running")
	}
	task.Prompt = prompt
	task.Abort = runtime.Abort()
	s.runtimes[id] = runtime
	return task.Base().LifecycleToken, nil
}

func (s *Store) SetRuntimeMetadata(id string, token uint64, runtime Runtime) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id].(*AgentState)
	if !ok || task.Base().LifecycleToken != token {
		return false
	}
	if path := runtime.OutputPath(); path != "" {
		task.Base().OutputFile = path
	}
	if path := runtime.Worktree(); path != "" {
		task.WorktreePath = path
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

// cloneDeadline 隔离驱逐截止时间指针
func cloneDeadline(deadline *time.Time) *time.Time {
	if deadline == nil {
		return nil
	}
	copy := *deadline
	return &copy
}

// cloneUsage 隔离 Usage 里的 Cost 指针
func cloneUsage(usage llm.Usage) llm.Usage {
	if usage.Cost != nil {
		cost := *usage.Cost
		usage.Cost = &cost
	}
	return usage
}

func cloneUserMessage(message llm.UserMessage) llm.UserMessage {
	message.Content = llm.CloneContent(message.Content)
	return message
}

func cloneTaskState(task State) State {
	switch state := task.(type) {
	case *BashState:
		copy := *state
		copy.Base().EvictAfter = cloneDeadline(state.Base().EvictAfter)
		copy.Process = nil
		if state.Result != nil {
			result := *state.Result
			copy.Result = &result
		}
		return &copy
	case *AgentState:
		copy := *state
		copy.Base().EvictAfter = cloneDeadline(state.Base().EvictAfter)
		copy.Abort = nil
		copy.PendingSteering = CloneUserMessages(state.PendingSteering)
		copy.PendingFollowUp = CloneUserMessages(state.PendingFollowUp)
		if state.Result != nil {
			result := *state.Result
			result.Content = append([]TextBlock(nil), state.Result.Content...)
			result.Usage = cloneUsage(state.Result.Usage)
			copy.Result = &result
		}
		copy.Model = cloneModelPtr(state.Model)
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

// cloneModelPtr 复制模型指针，nil 原样返回
func cloneModelPtr(model *llm.Model) *llm.Model {
	if model == nil {
		return nil
	}
	cloned := model.Clone()
	return &cloned
}
