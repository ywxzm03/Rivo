package llm

type Message interface {
	Message()
	Role() string
	GetContent() []content
}

type UserMessage struct {
	Content []content
}

func (UserMessage) Message()                {}
func (UserMessage) Role() string            { return "user" }
func (u UserMessage) GetContent() []content { return u.Content }

type AssistantMessage struct {
	Content []content
}

func (AssistantMessage) Message()                {}
func (AssistantMessage) Role() string            { return "assistant" }
func (a AssistantMessage) GetContent() []content { return a.Content }

type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []content
}

func (ToolResultMessage) Message()                {}
func (ToolResultMessage) Role() string            { return "tool_result" }
func (a ToolResultMessage) GetContent() []content { return a.Content }

var (
	_ Message = &UserMessage{}
	_ Message = &AssistantMessage{}
	_ Message = &ToolResultMessage{}
)
