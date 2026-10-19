package llm

type API string

const APIOpenAIResponses API = "openai-responses"
const APIOpenAICompletions API = "openai-completions"

type Model struct {
	ID       string
	Name     string
	Provider string
	API      API
	// Cost 是模型每百万 token 的美元费率；nil 表示未配置
	Cost *CostRates

	// SupportsImages 表示这个模型接受图片输入
	SupportsImages bool
	// Reasoning 表示这个模型支持 provider reasoning
	Reasoning bool
	// ReasoningEffort 是模型默认的 reasoning effort
	ReasoningEffort ReasoningEffort
	// ContextWindow 是模型上下文窗口 token 数，0 表示未知，未知时不触发自动压缩
	ContextWindow int
	// MaxTokens 是模型单次输出的最大 token 数，0 表示未知；用于区分输出截断与上下文耗尽
	MaxTokens int
	// ThinkingLevelMap 把思考等级映射为该模型的 reasoning effort；
	// 值为 nil 表示该等级禁用；缺失的等级按 pi 语义透传或收敛（xhigh/max 须显式映射才可用）
	ThinkingLevelMap map[ThinkingLevel]*ReasoningEffort
	// SamplingParams 是并入请求体的自由采样参数（temperature/top_p 等），nil 表示无
	SamplingParams map[string]any
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
