package tui

// subagentPanel 承载子任务视图与选择器状态
type subagentPanel struct {
	// viewingTaskID 非空时 transcript 显示该任务
	viewingTaskID string
	// mainMessages 暂存主 agent 的消息，切回时恢复
	mainMessages []Message
	// selectedTaskID 与 mainSelected 表示当前选中项，mainSelected 为真时选中主 agent
	selectedTaskID string
	mainSelected   bool
	// taskSelector 表示按键焦点在任务列表
	taskSelector bool
	// refreshRunning 标记后台刷新已排程，避免重复起 goroutine
	refreshRunning bool
}
