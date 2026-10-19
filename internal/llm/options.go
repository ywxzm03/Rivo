package llm

// ThinkingLevel 表示用户选择的思考等级
type ThinkingLevel string

const (
	ThinkingLevelOff     ThinkingLevel = "off"
	ThinkingLevelMinimal ThinkingLevel = "minimal"
	ThinkingLevelLow     ThinkingLevel = "low"
	ThinkingLevelMedium  ThinkingLevel = "medium"
	ThinkingLevelHigh    ThinkingLevel = "high"
	ThinkingLevelXHigh   ThinkingLevel = "xhigh"
	ThinkingLevelMax     ThinkingLevel = "max"
)

type Options struct {
	// MaxTokens 为 0 表示不限制
	MaxTokens int
	// ThinkingLevel 为空时沿用模型默认的 reasoning effort
	ThinkingLevel ThinkingLevel
	// SessionID 用于 provider 缓存路由
	SessionID string
}
