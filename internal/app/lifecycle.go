package app

import (
	"sync"
	"sync/atomic"
	"time"
)

// lifecycle 承载 App 的两条长生命周期循环：后台通知消费与终态任务驱逐。
// 两者共用同一个 stop 信号，关闭时一起退出
type lifecycle struct {
	// closed 在进程收尾时置位，让通知循环退出而不是继续起 run
	closed atomic.Bool
	// closeOnce 保证清理只执行一次
	closeOnce sync.Once
	// stop 关闭时停止两条循环
	stop     chan struct{}
	stopOnce sync.Once

	// notifyQueue 把子任务终态通知转成一次后台 run；缓冲 1 让重复通知合并
	notifyQueue chan struct{}
	// evictWake 在出现新的待驱逐任务时唤醒已停表的驱逐循环
	evictWake chan struct{}

	// evictionInterval 是驱逐轮询间隔，零值表示用 evictInterval
	evictionMu       sync.Mutex
	evictionInterval time.Duration
}

func newLifecycle() *lifecycle {
	return &lifecycle{
		stop:        make(chan struct{}),
		notifyQueue: make(chan struct{}, 1),
		evictWake:   make(chan struct{}, 1),
	}
}

// close 停止两条循环，可重复调用
func (l *lifecycle) close() {
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		l.stopOnce.Do(func() { close(l.stop) })
	})
}

// setEvictionInterval 覆盖驱逐轮询间隔，只对随后启动的循环生效
func (l *lifecycle) setEvictionInterval(interval time.Duration) {
	l.evictionMu.Lock()
	l.evictionInterval = interval
	l.evictionMu.Unlock()
}

// evictionTick 返回当前生效的轮询间隔
func (l *lifecycle) evictionTick() time.Duration {
	l.evictionMu.Lock()
	defer l.evictionMu.Unlock()
	if l.evictionInterval > 0 {
		return l.evictionInterval
	}
	return evictInterval
}

// wake 在子任务通知入队时启动一次消费 run。
// 已有操作在跑时不介入:它收尾时会检查队列并续跑
func (l *lifecycle) wake() {
	if l.closed.Load() {
		return
	}
	// 新的终态任务可能让已停表的驱逐循环重新有工作
	select {
	case l.evictWake <- struct{}{}:
	default:
	}
	select {
	case l.notifyQueue <- struct{}{}:
	default:
		// 队列已有一次待处理的唤醒,重复通知会被同一轮 run 一并消费
	}
}

// notificationHost 是通知循环需要的 App 能力
type notificationHost interface {
	hasPendingNotification() bool
	startNotificationRun() (operationWaiter, error)
	notifyBackgroundRunEnd()
}

// operationWaiter 只要求等待操作结束
type operationWaiter interface{ Wait() OperationResult }

// runNotifications 消费唤醒信号。用 Continue 驱动空请求,
// 通知作为 transient 附件挂在这一轮请求上,不写入主 session。
// Start 被运行中的操作拒绝时不能丢弃信号:否则通知会一直留在队列里,
// 只能等用户下次输入才投递
func (l *lifecycle) runNotifications(host notificationHost) {
	for {
		select {
		case <-l.stop:
			return
		case _, ok := <-l.notifyQueue:
			if !ok || l.closed.Load() {
				return
			}
		}
		if !host.hasPendingNotification() {
			continue
		}
		op, err := host.startNotificationRun()
		if err != nil {
			l.retryWake(host)
			continue
		}
		// 收尾一定落在操作真正结束后,不依赖 AgentEndEvent 是否成功送达
		op.Wait()
		host.notifyBackgroundRunEnd()
	}
}

// retryWake 在 Start 被拒后重新排队一次唤醒。
// 延迟重试避免占满 CPU;同一时刻只保留一个待处理信号
func (l *lifecycle) retryWake(host notificationHost) {
	if l.closed.Load() {
		return
	}
	time.AfterFunc(notificationRetryDelay, func() {
		if l.closed.Load() {
			return
		}
		l.wake()
		_ = host
	})
}

// evictionHost 是驱逐循环需要的 App 能力
type evictionHost interface {
	subagentEvictionStore() subagentEvictor
}

// subagentEvictor 是驱逐循环用到的最小 store 能力
type subagentEvictor interface {
	EvictExpired() int
	HasEvictionWork() bool
}

// runEviction 每秒回收宽限期已过的终态任务。
// 只靠请求边界驱逐不够:用户不再发消息时终态任务会永久留在 store 里,
// TUI 任务行也就一直不消失
func (l *lifecycle) runEviction(host evictionHost) {
	stop := l.stop
	ticker := time.NewTicker(l.evictionTick())
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			store := host.subagentEvictionStore()
			if store == nil {
				return
			}
			store.EvictExpired()
			// 没有待驱逐的终态任务时停表，避免空闲进程每秒空转；
			// 新任务完成时会重新启动循环
			if !store.HasEvictionWork() {
				ticker.Stop()
				select {
				case <-stop:
					return
				case <-l.evictWake:
				}
				ticker = time.NewTicker(l.evictionTick())
			}
		}
	}
}
