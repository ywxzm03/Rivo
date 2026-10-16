package llm

type AgentConfig struct {
	Model Model
	// ThinkingLevel 是本次会话的思考等级；空串表示未指定
	ThinkingLevel ThinkingLevel
	// SessionID 是当前持久化会话的 UUID，用于请求级缓存路由
	SessionID string
}
