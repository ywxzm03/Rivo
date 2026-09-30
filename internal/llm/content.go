package llm

type ContentType string

const (
	ContentText     ContentType = "text"
	ContentImage    ContentType = "image"
	ContentThinking ContentType = "thinking"
	ContentToolCall ContentType = "toolCall"
)

type Content interface {
	Type() ContentType
	content()
}

type TextContent struct {
	Text string
}

func (TextContent) Type() ContentType { return ContentText }
func (TextContent) content()          {}

type ImageContent struct {
	Data     string
	MIMEType string
}

func (ImageContent) Type() ContentType { return ContentImage }
func (ImageContent) content()          {}

// ThinkingContent 保存模型 reasoning 的可展示文本和 provider 原始 item
type ThinkingContent struct {
	Thinking          string
	ThinkingSignature string
}

// Type 返回 ThinkingContent 的内容类型
func (ThinkingContent) Type() ContentType { return ContentThinking }

// content 标记 ThinkingContent 实现 Content 接口
func (ThinkingContent) content() {}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func (ToolCall) Type() ContentType { return ContentToolCall }
func (ToolCall) content()          {}

var (
	_ Content = TextContent{}
	_ Content = ImageContent{}
	_ Content = ThinkingContent{}
	_ Content = ToolCall{}
)
