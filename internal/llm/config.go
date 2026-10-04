package llm

type AgentConfig struct {
	Model Model
	// ThinkingLevel 是本次会话的思考等级；空串表示未指定
	ThinkingLevel ThinkingLevel
}
