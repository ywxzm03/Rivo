package llm

type content interface {
	content()
}

type TextContent struct {
	Text string
}

func (TextContent) content()          {}
func (t TextContent) GetText() string { return t.Text }

type ImageContent struct {
	Data     string
	MIMEType string
}

func (ImageContent) content()              {}
func (i ImageContent) GetData() string     { return i.Data }
func (i ImageContent) GetMIMEType() string { return i.MIMEType }

type ToolCall struct {
	ToolCallID string
	ToolName   string
	Arguments  map[string]any
}

func (ToolCall) content() {}
func (t ToolCall) GetToolCallID() string {
	return t.ToolCallID
}
func (t ToolCall) GetToolName() string {
	return t.ToolName
}

var (
	_ content = &ToolCall{}
	_ content = &ImageContent{}
	_ content = &TextContent{}
)
