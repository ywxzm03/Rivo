package llm

type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

type Message interface {
	Role() Role
	message()
}

type UserMessage struct {
	Content []Content
}

func (UserMessage) Role() Role { return RoleUser }
func (UserMessage) message()   {}

type AssistantMessage struct {
	Content    []Content
	StopReason StopReason
	Usage      Usage
}

func (AssistantMessage) Role() Role { return RoleAssistant }
func (AssistantMessage) message()   {}

type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []Content
	Details    any
	IsError    bool
}

func (ToolResultMessage) Role() Role { return RoleToolResult }
func (ToolResultMessage) message()   {}

type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonLength  StopReason = "length"
	StopReasonError   StopReason = "error"
)

type Usage struct {
	InputTokens  int
	OutputTokens int
}

var (
	_ Message = UserMessage{}
	_ Message = AssistantMessage{}
	_ Message = ToolResultMessage{}
)
