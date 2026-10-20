package llm

type API string

const APIOpenAIResponses API = "openai-responses"
const APIOpenAICompletions API = "openai-completions"

type Model struct {
	ID              string
	Name            string
	Provider        string
	API             API
	Cost            *CostRates
	SupportsImages  bool
	Reasoning       bool
	ReasoningEffort ReasoningEffort

	// ContextWindow 为 0 表示未知，未知时不触发自动压缩
	ContextWindow int
	// MaxTokens 为 0 表示未知；已知时用于区分输出截断与上下文耗尽
	MaxTokens int

	// ThinkingLevelMap 把思考等级映射为该模型的 reasoning effort；值为 nil 表示禁用，
	// 缺失的等级透传或收敛（xhigh/max 须显式映射才可用）
	ThinkingLevelMap map[ThinkingLevel]*ReasoningEffort
	SamplingParams   map[string]any
}

type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
	ReasoningEffortXHigh  ReasoningEffort = "xhigh"
	ReasoningEffortMax    ReasoningEffort = "max"
)
