package llm

type ContentType string

const (
	ContentText     ContentType = "text"
	ContentImage    ContentType = "image"
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
	_ Content = ToolCall{}
)
