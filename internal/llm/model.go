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
	// ReserveTokens 是上下文压缩预留 token；0 表示未配置，回退到默认值
	ReserveTokens int

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

// Clone 深拷贝模型的指针与 map 字段，避免多个持有者共享同一份可变状态
func (m Model) Clone() Model {
	if m.Cost != nil {
		cost := *m.Cost
		m.Cost = &cost
	}
	if m.ThinkingLevelMap != nil {
		mapping := make(map[ThinkingLevel]*ReasoningEffort, len(m.ThinkingLevelMap))
		for level, effort := range m.ThinkingLevelMap {
			if effort == nil {
				mapping[level] = nil
				continue
			}
			value := *effort
			mapping[level] = &value
		}
		m.ThinkingLevelMap = mapping
	}
	m.SamplingParams = cloneParams(m.SamplingParams)
	return m
}

// cloneParams 深拷贝采样参数，嵌套 map 与 slice 也要隔离
func cloneParams(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	cloned := make(map[string]any, len(params))
	for key, value := range params {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneParams(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneValue(item)
		}
		return cloned
	default:
		return value
	}
}
