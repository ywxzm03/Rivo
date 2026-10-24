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

type ThinkingContent struct {
	Thinking          string
	ThinkingSignature string
}

func (ThinkingContent) Type() ContentType { return ContentThinking }

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

// CloneContent 深拷贝一组 content,隔离 ToolCall 的 Arguments map
func CloneContent(content []Content) []Content {
	result := make([]Content, len(content))
	for i, item := range content {
		if call, ok := item.(ToolCall); ok {
			cloned := call
			cloned.Arguments = make(map[string]any, len(call.Arguments))
			for key, value := range call.Arguments {
				cloned.Arguments[key] = value
			}
			result[i] = cloned
			continue
		}
		result[i] = item
	}
	return result
}
