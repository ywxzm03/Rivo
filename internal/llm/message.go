package llm

type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
	RoleSystem     Role = "system"
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
	API        API
	Provider   string
	Model      string
	ResponseID string
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

// SystemMessage 一条 system prompt 声明：Sections 承载分节文本，空串表示该节被删除
type SystemMessage struct {
	Sections map[string]string
}

func (SystemMessage) Role() Role { return RoleSystem }
func (SystemMessage) message()   {}

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
	_ Message = SystemMessage{}
)
