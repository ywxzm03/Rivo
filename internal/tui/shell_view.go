package tui

// ShellView 是单个后台 bash 任务的展示投影
type ShellView struct {
	// Command 是启动时提交的命令原文
	Command string
	// Status 取 running / completed / failed / killed
	Status string
	// OutputTail 是输出文件末尾若干行,无输出时为 nil
	OutputTail []string
	// Truncated 表示输出达到上限被截断
	Truncated bool
	// OutputError 记录读取输出文件失败的原因,正常时为空
	OutputError string
}

// ShellsMsg 是 /ps 的查询结果
type ShellsMsg struct {
	Shells []ShellView
	Err    error
}

// ShellsStoppedMsg 是 /stop 的执行结果
type ShellsStoppedMsg struct {
	// Stopped 是本次调用结束时已不在运行的目标数量
	Stopped int
	Err     error
}
