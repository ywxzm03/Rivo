package adapter

import (
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// requestSpec 描述一种用户操作在适配层的处理方式。
// 新增操作时只改这张表，不会漏掉领域映射、快照或提示文案
type requestSpec struct {
	// operation 是领域层的操作类型
	operation app.OperationKind
}

var requestSpecs = map[tui.RequestKind]requestSpec{
	tui.RequestRun:      {operation: app.OperationRun},
	tui.RequestCompact:  {operation: app.OperationCompact},
	tui.RequestModel:    {operation: app.OperationModel},
	tui.RequestThinking: {operation: app.OperationThinking},
	tui.RequestResume:   {operation: app.OperationResume},
	tui.RequestRename:   {operation: app.OperationRename},
	tui.RequestNew:      {operation: app.OperationNew},
	tui.RequestReload:   {operation: app.OperationReload},
	tui.RequestTree:     {operation: app.OperationTree},
	tui.RequestClear:    {operation: app.OperationClear},
	tui.RequestDelete:   {operation: app.OperationDelete},
}

// operationKind 把 TUI 请求类型映射成领域操作类型
func operationKind(kind tui.RequestKind) app.OperationKind {
	return requestSpecs[kind].operation
}

// operationSnapshot 记录操作前的设置，完成后只在值真正变化时输出提示
type operationSnapshot struct {
	Model    string
	Thinking string
	Name     string
}
