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
	// Hidden 标记这条消息只供模型阅读,不进显示层
	// 子任务通知落盘时置位:模型下一轮照常读到正文,TUI 投影与会话树跳过渲染
	// 标记随会话文件持久化,重开会话后仍然生效
	Hidden bool
}

func (UserMessage) Role() Role { return RoleUser }
func (UserMessage) message()   {}

// IsHiddenMessage 报告该消息是否只供模型阅读
// 它让显示层与会话元数据不必各自展开 UserMessage 判空
func IsHiddenMessage(m Message) bool {
	user, ok := m.(UserMessage)
	return ok && user.Hidden
}

type AssistantMessage struct {
	API          API
	Provider     string
	Model        string
	ResponseID   string
	Content      []Content
	StopReason   StopReason
	ErrorMessage string
	Usage        Usage
}

func (AssistantMessage) Role() Role { return RoleAssistant }
func (AssistantMessage) message()   {}

type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []Content
	// Details 是工具专属的结构化附加信息，见 ToolDetails
	Details ToolDetails
	IsError bool
	Usage   *Usage
}

func (ToolResultMessage) Role() Role { return RoleToolResult }
func (ToolResultMessage) message()   {}

// SystemMessage 一条 system prompt 声明；Sections 中空串表示该节被删除
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
	StopReasonAborted StopReason = "aborted"
)

// Usage 是持久化到会话文件的 token 用量，字段名即磁盘契约
type Usage struct {
	// CacheWrite1HTokens 是 CacheWriteTokens 的细分项，ReasoningTokens 含在 OutputTokens 内
	InputTokens        int   `json:"inputTokens"`
	OutputTokens       int   `json:"outputTokens"`
	CacheReadTokens    int   `json:"cacheReadTokens"`
	CacheWriteTokens   int   `json:"cacheWriteTokens"`
	CacheWrite1HTokens int   `json:"cacheWrite1hTokens"`
	ReasoningTokens    int   `json:"reasoningTokens"`
	TotalTokens        int   `json:"totalTokens"`
	Cost               *Cost `json:"cost,omitempty"`
}

// ContextTokens 返回该 usage 对上下文窗口的 token 贡献，total 缺失时按分项回退计算
func (u Usage) ContextTokens() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + max(u.CacheWriteTokens, u.CacheWrite1HTokens)
}

var (
	_ Message = UserMessage{}
	_ Message = AssistantMessage{}
	_ Message = ToolResultMessage{}
	_ Message = SystemMessage{}
)
