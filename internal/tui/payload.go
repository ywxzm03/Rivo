package tui

// ImagePart 是编辑器里待发送的一张图片
type ImagePart struct {
	// Data 是 base64 编码的图片内容
	Data     string
	MIMEType string
}

// ComposerPayload 是编辑器产出的待发送内容，TUI 之外由适配器转成领域消息
type ComposerPayload struct {
	Text   string
	Images []ImagePart
}

// RestoreKind 是树恢复方式，与领域层的恢复模式一一对应
type RestoreKind string

const (
	RestoreContext        RestoreKind = "context"
	RestoreCode           RestoreKind = "code"
	RestoreCodeAndContext RestoreKind = "code_and_context"
)

// RequestKind 是用户操作的类型，与领域层的操作类型一一对应
type RequestKind string

const (
	RequestRun      RequestKind = "run"
	RequestCompact  RequestKind = "compact"
	RequestModel    RequestKind = "model"
	RequestThinking RequestKind = "thinking"
	RequestResume   RequestKind = "resume"
	RequestRename   RequestKind = "rename"
	RequestNew      RequestKind = "new"
	RequestReload   RequestKind = "reload"
	RequestTree     RequestKind = "tree"
	RequestClear    RequestKind = "clear"
	RequestDelete   RequestKind = "delete"
	// RequestTreeRestore 只在 TUI 内部区分选择器阶段，不会作为请求发出
	RequestTreeRestore RequestKind = "tree_restore"
)
