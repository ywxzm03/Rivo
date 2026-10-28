package task

import (
	"github.com/ywxzm03/Rivo/internal/llm"
)

// NotificationQueue 是终态通知队列
type NotificationQueue struct{ store *Store }

// PendingQueues 是任务级的待注入输入队列
type PendingQueues struct{ store *Store }

// TaskViews 是根 TUI 的任务视图元数据
type TaskViews struct{ store *Store }

// Peek 返回通知队列的副本，不清空
func (q NotificationQueue) Peek() []Notification { return q.store.PeekNotifications() }

// Drain 取走全部通知并清空队列
func (q NotificationQueue) Drain() []Notification { return q.store.DrainNotifications() }

// Ack 确认前 count 条通知已被消费
func (q NotificationQueue) Ack(count int) { q.store.AckNotifications(count) }

// Enqueue 把一条输入排进指定任务的队列，followUp 决定注入时机
func (q PendingQueues) Enqueue(id string, followUp bool, message llm.UserMessage) error {
	return q.store.EnqueueTaskMessage(id, message, followUp)
}

// Drain 取走指定任务的某一类队列
func (q PendingQueues) Drain(id string, followUp bool) []llm.UserMessage {
	if followUp {
		return q.store.DrainPendingFollowUpMessages(id, q.store.tokenOf(id))
	}
	return q.store.DrainPendingMessages(id, q.store.tokenOf(id))
}

// Enter 记录根 TUI 正在显示的任务
func (v TaskViews) Enter(id string) bool { return v.store.EnterView(id) }

// Exit 退出任务视图
func (v TaskViews) Exit() { v.store.ExitView() }

// Viewing 返回当前正在显示的任务 ID
func (v TaskViews) Viewing() string { return v.store.ViewingTaskID() }

// PendingQueue 返回指定任务某一类待注入队列的快照，供展示层读取
func (q PendingQueues) Snapshot(id string, followUp bool) []llm.UserMessage {
	if followUp {
		return q.store.PendingFollowUpMessages(id)
	}
	return q.store.PendingMessages(id)
}

// Requeue 把一批输入按顺序放回队列头部，用于注入失败后的回滚
func (q PendingQueues) Requeue(id string, followUp bool, messages []llm.UserMessage) {
	if followUp {
		q.store.RequeuePendingFollowUpMessagesFront(id, q.store.tokenOf(id), messages)
		return
	}
	q.store.RequeuePendingMessagesFront(id, q.store.tokenOf(id), messages)
}

// Clear 清空指定任务的某一类队列
func (q PendingQueues) Clear(id string, followUp bool) {
	if followUp {
		q.store.ClearFollowUpMessages(id)
		return
	}
	q.store.ClearSteeringMessages(id)
}
