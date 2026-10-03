package compaction

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// DefaultMaxRetryDelay 单次重试延迟上限，对齐上游默认 60 秒
const DefaultMaxRetryDelay = 60 * time.Second

// RetryPolicy 摘要调用的有界重试策略：只重试瞬时错误，确定性错误（配额/账单耗尽）
// 立即失败，用户中止是终态永不重试
type RetryPolicy struct {
	Enabled    bool
	MaxRetries int           // 最大重试次数，初次调用不计入
	BaseDelay  time.Duration // 第 n 次重试延迟 = BaseDelay * 2^(n-1)
	MaxDelay   time.Duration // 单次延迟上限
}

// DefaultRetryPolicy 返回默认重试策略：重试 3 次，指数退避 2s/4s/8s，上限 60s
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Enabled:    true,
		MaxRetries: 3,
		BaseDelay:  2 * time.Second,
		MaxDelay:   DefaultMaxRetryDelay,
	}
}

// retryDelayMs 计算第 attempt 次重试的退避延迟（attempt 从 1 开始）
func retryDelayMs(policy RetryPolicy, attempt int) time.Duration {
	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultMaxRetryDelay
	}
	delay := policy.BaseDelay << (attempt - 1)
	if delay <= 0 || delay > maxDelay {
		return maxDelay
	}
	return delay
}

// nonRetryableErrorPattern 配额/订阅/账单耗尽类错误，重试无意义，立即失败
var nonRetryableErrorPattern = regexp.MustCompile(`(?i)` + strings.Join([]string{
	"GoUsageLimitError",
	"FreeUsageLimitError",
	"Monthly usage limit reached",
	"available balance",
	"insufficient_quota",
	"out of budget",
	"quota exceeded",
	"billing",
	"subscription_sharing_usage_limit_exceeded",
}, "|"))

// retryableErrorPattern 瞬时错误签名：负载/限流/5xx/网络与代理传输失败/流提前结束
var retryableErrorPattern = regexp.MustCompile(`(?i)` + strings.Join([]string{
	"overloaded",
	"currently experiencing high demand",
	"rate.?limit",
	"too many requests",
	"429",
	"500",
	"502",
	"503",
	"504",
	"520",
	"524",
	"service.?unavailable",
	"server.?error",
	"internal.?error",
	"provider.?returned.?error",
	"exceeded request buffer limit while retrying upstream",
	"network.?error",
	"connection.?error",
	"connection.?refused",
	"connection.?lost",
	"other side closed",
	"fetch failed",
	"getaddrinfo",
	"ENOTFOUND",
	"EAI_AGAIN",
	"upstream.?connect",
	"reset before headers",
	"socket hang up",
	"socket connection was closed",
	"timed? out",
	"timeout",
	"terminated",
	"websocket.?closed",
	"websocket.?error",
	"ended without",
	"stream ended before message_stop",
	"stream ended before a terminal response event",
	"http2 request did not get a response",
	"retry delay",
	"you can retry your request",
	"try your request again",
	"please retry your request",
	"ResourceExhausted",
	"subscription_sharing_usage_unavailable",
	"subscription_sharing_user_unavailable",
}, "|"))

// isRetryableError 判断摘要调用错误是否为可重试的瞬时错误：
// 先排除配额/账单类确定性错误，再匹配瞬时签名；上下文中止与超时是终态，永不重试
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := err.Error()
	if nonRetryableErrorPattern.MatchString(message) {
		return false
	}
	return retryableErrorPattern.MatchString(message)
}

// retrySummarize 以有界重试执行 produce：成功立即返回；中止是终态；确定性错误快速失败；
// 瞬时错误按指数退避重试至多 MaxRetries 次，退避期间响应取消
func retrySummarize(ctx context.Context, policy RetryPolicy, produce func() (string, llm.Usage, error)) (string, llm.Usage, error) {
	maxAttempts := 0
	if policy.Enabled {
		maxAttempts = policy.MaxRetries
	}
	for attempt := 0; ; attempt++ {
		text, usage, err := produce()
		if err == nil {
			return text, usage, nil
		}
		// 中止是终态，静默返回；确定性错误或重试预算耗尽立即失败
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", usage, err
		}
		if attempt >= maxAttempts || !isRetryableError(err) {
			return "", usage, err
		}
		delay := retryDelayMs(policy, attempt+1)
		select {
		case <-ctx.Done():
			return "", usage, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// validateSummary 在持久化前校验摘要响应：截断的摘要只有部分文本，绝不能变成会话检查点
func validateSummary(message *llm.AssistantMessage) error {
	if message == nil {
		return fmt.Errorf("compaction: 摘要响应为空")
	}
	switch message.StopReason {
	case llm.StopReasonError:
		return fmt.Errorf("compaction: 摘要生成失败")
	case llm.StopReasonLength:
		return fmt.Errorf("compaction: 摘要被输出长度截断")
	}
	for _, c := range message.Content {
		if _, ok := c.(llm.ToolCall); ok {
			return fmt.Errorf("compaction: 摘要试图调用工具")
		}
	}
	return nil
}
