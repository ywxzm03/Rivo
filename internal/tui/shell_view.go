package tui

import "time"

// ShellView 是单个后台 bash 任务的展示投影
type ShellView struct {
	// ID 是任务主键,弹窗内终止时用它定位
	ID string
	// Command 是启动时提交的命令原文
	Command string
	// Status 取 running / completed / failed / killed
	Status string
	// StartTime 是启动时刻,用于计算运行时长
	StartTime time.Time
	// EndTime 是终态时刻,运行中为 nil
	EndTime *time.Time
	// ExitCode 是进程退出码,未退出或未知为 nil
	ExitCode *int
	// OutputTail 是输出文件末尾若干行,无输出时为 nil
	OutputTail []string
	// Truncated 表示输出超出读取上限,OutputTail 只是尾部
	Truncated bool
	// OutputBytes 是输出文件总字节数,读不到为 0
	OutputBytes int64
	// OutputError 记录读取输出文件失败的原因,正常时为空
	OutputError string
}

// ShellsMsg 是 /ps 的查询结果
type ShellsMsg struct {
	Shells []ShellView
	Err    error
}

// ShellKilledMsg 是弹窗内按 x 终止单个 shell 的结果
type ShellKilledMsg struct {
	// ID 是被终止的任务 ID
	ID string
	// Err 是终止失败的原因,成功为 nil
	Err error
}

// ShellsStoppedMsg 是 /stop 的执行结果
type ShellsStoppedMsg struct {
	// Stopped 是本次调用结束时已不在运行的目标数量
	Stopped int
	Err     error
}
