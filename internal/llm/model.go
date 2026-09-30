package llm

type API string

const APIOpenAIResponses API = "openai-responses"

type Model struct {
	ID       string
	Name     string
	Provider string
	API      API

	// SupportsImages 表示这个模型接受图片输入
	SupportsImages bool
	// Reasoning 表示这个模型支持 Responses reasoning
	Reasoning bool
	// ReasoningEffort 是模型默认的 reasoning effort
	ReasoningEffort ReasoningEffort
}

// ReasoningEffort 表示模型 reasoning 的计算强度
type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
	ReasoningEffortXHigh  ReasoningEffort = "xhigh"
	ReasoningEffortMax    ReasoningEffort = "max"
)
