package llm

// Cost 保存一次模型调用的美元费用分项
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type costRates struct {
	input      float64
	output     float64
	cacheRead  float64
	cacheWrite float64
}

type modelPricing struct {
	standard         costRates
	longContext      *costRates
	longContextLimit int
}

const cnyToUSD = 0.14

var builtinPricing = map[string]modelPricing{
	"openai/gpt-6-astra": {
		standard:         costRates{input: 10, output: 50, cacheRead: 1, cacheWrite: 12.5},
		longContext:      ptrCostRates(costRates{input: 20, output: 75, cacheRead: 2, cacheWrite: 25}),
		longContextLimit: 272000,
	},
	"openai/gpt-6.1-sol": {
		standard:         costRates{input: 2, output: 10, cacheRead: 0.1, cacheWrite: 2.5},
		longContext:      ptrCostRates(costRates{input: 4, output: 15, cacheRead: 0.2, cacheWrite: 5}),
		longContextLimit: 272000,
	},
	"openai/gpt-6-luna": {
		standard:         costRates{input: 0.1, output: 0.5, cacheRead: 0.01, cacheWrite: 0.125},
		longContext:      ptrCostRates(costRates{input: 0.2, output: 0.75, cacheRead: 0.02, cacheWrite: 0.25}),
		longContextLimit: 272000,
	},
	"deepseek/deepseek-flash": {
		standard: costRates{
			input:     1 * cnyToUSD,
			output:    4 * cnyToUSD,
			cacheRead: 0.02 * cnyToUSD,
		},
	},
	"deepseek/deepseek-v4-pro": {
		standard: costRates{
			input:     4.5 * cnyToUSD,
			output:    13.5 * cnyToUSD,
			cacheRead: 0.15 * cnyToUSD,
		},
	},
}

func ptrCostRates(rates costRates) *costRates { return &rates }

// PriceUsage 根据 provider/model 和请求 usage 计算一次美元费用。
// OpenAI 长上下文价格按整次请求的 prompt token 总量选择。
func PriceUsage(model Model, usage Usage) *Cost {
	if usage.ContextTokens() <= 0 {
		return nil
	}
	pricing, ok := builtinPricing[model.Provider+"/"+model.ID]
	if !ok {
		return nil
	}
	rates := pricing.standard
	cacheWriteTokens := usage.CacheWriteTokens
	if cacheWriteTokens < usage.CacheWrite1HTokens {
		cacheWriteTokens = usage.CacheWrite1HTokens
	}
	promptTokens := usage.InputTokens + usage.CacheReadTokens + cacheWriteTokens
	if pricing.longContext != nil && promptTokens > pricing.longContextLimit {
		rates = *pricing.longContext
	}
	return &Cost{
		Input:      float64(usage.InputTokens) * rates.input / 1_000_000,
		Output:     float64(usage.OutputTokens) * rates.output / 1_000_000,
		CacheRead:  float64(usage.CacheReadTokens) * rates.cacheRead / 1_000_000,
		CacheWrite: float64(cacheWriteTokens) * rates.cacheWrite / 1_000_000,
		Total: (float64(usage.InputTokens)*rates.input +
			float64(usage.OutputTokens)*rates.output +
			float64(usage.CacheReadTokens)*rates.cacheRead +
			float64(cacheWriteTokens)*rates.cacheWrite) / 1_000_000,
	}
}
