package llm

// ThinkingLevel 表示用户选择的思考等级，等级体系与 pi 一致
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
	// MaxTokens 限制本次请求的最大输出 token 数，0 表示不限制
	MaxTokens int
	// ThinkingLevel 是本次请求的思考等级；空串表示未指定，沿用模型默认的 reasoning effort
	ThinkingLevel ThinkingLevel
	// SessionID 是当前持久化会话的 UUID，provider 可用于缓存路由
	SessionID string
}
