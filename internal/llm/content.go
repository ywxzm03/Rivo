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

// CloneContent 深拷贝一组 content,隔离 ToolCall 的 Arguments
func CloneContent(content []Content) []Content {
	result := make([]Content, len(content))
	for i, item := range content {
		if call, ok := item.(ToolCall); ok {
			cloned := call
			cloned.Arguments = cloneArguments(call.Arguments)
			result[i] = cloned
			continue
		}
		result[i] = item
	}
	return result
}

// cloneArguments 递归复制 tool 参数里的复合值。
// 参数来自 JSON 解码,只可能出现 map[string]any / []any 两类容器
func cloneArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	result := make(map[string]any, len(arguments))
	for key, value := range arguments {
		result[key] = cloneArgumentValue(value)
	}
	return result
}

func cloneArgumentValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneArguments(value)
	case []any:
		cloned := make([]any, len(value))
		for i, item := range value {
			cloned[i] = cloneArgumentValue(item)
		}
		return cloned
	default:
		return value
	}
}
