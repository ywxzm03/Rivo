package llm

type AgentConfig struct {
	Model Model
	// ThinkingLevel 为空时未指定
	ThinkingLevel ThinkingLevel
	// SessionID 用于请求级缓存路由
	SessionID string
}
