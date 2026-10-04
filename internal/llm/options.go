package llm

type Options struct {
	// MaxTokens 限制本次请求的最大输出 token 数，0 表示不限制
	MaxTokens int
}
