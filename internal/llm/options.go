package llm

import "strings"

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

// ThinkingLevels 按从低到高列出全部受支持的等级
var ThinkingLevels = []ThinkingLevel{
	ThinkingLevelOff, ThinkingLevelMinimal, ThinkingLevelLow, ThinkingLevelMedium,
	ThinkingLevelHigh, ThinkingLevelXHigh, ThinkingLevelMax,
}

// ValidThinkingLevel 判断等级是否为受支持的值。空串表示未指定,视为合法
func ValidThinkingLevel(level ThinkingLevel) bool {
	switch level {
	case "", ThinkingLevelOff, ThinkingLevelMinimal, ThinkingLevelLow, ThinkingLevelMedium,
		ThinkingLevelHigh, ThinkingLevelXHigh, ThinkingLevelMax:
		return true
	default:
		return false
	}
}

// ThinkingLevelList 返回用于错误提示的等级列表,与 ValidThinkingLevel 同源
func ThinkingLevelList() string {
	names := make([]string, len(ThinkingLevels))
	for i, level := range ThinkingLevels {
		names[i] = string(level)
	}
	return strings.Join(names, ", ")
}

type Options struct {
	// MaxTokens 为 0 表示不限制
	MaxTokens int
	// ThinkingLevel 为空时沿用模型默认的 reasoning effort
	ThinkingLevel ThinkingLevel
	// SessionID 用于 provider 缓存路由
	SessionID string
}

// ThinkingLevelNames 返回全部等级的名字，供帮助文本与错误提示使用
func ThinkingLevelNames() []string {
	names := make([]string, len(ThinkingLevels))
	for i, level := range ThinkingLevels {
		names[i] = string(level)
	}
	return names
}
