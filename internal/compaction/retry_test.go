package compaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "rate limit", err: errors.New("openai: 429: too many requests"), want: true},
		{name: "server error", err: errors.New("openai: 500: internal error"), want: true},
		{name: "overloaded", err: errors.New("provider is overloaded"), want: true},
		{name: "connection refused", err: errors.New("dial tcp: connection refused"), want: true},
		{name: "stream terminated", err: errors.New("stream terminated unexpectedly"), want: true},
		{name: "quota exhausted", err: errors.New("insufficient_quota: billing limit"), want: false},
		{name: "usage limit", err: errors.New("Monthly usage limit reached"), want: false},
		{name: "deterministic bad request", err: errors.New("openai: 400: bad json body"), want: false},
		{name: "context canceled", err: context.Canceled, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableError(tt.err); got != tt.want {
				t.Fatalf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryDelayMs(t *testing.T) {
	policy := RetryPolicy{BaseDelay: 2 * time.Second, MaxDelay: DefaultMaxRetryDelay}
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 2 * time.Second},
		{attempt: 2, want: 4 * time.Second},
		{attempt: 3, want: 8 * time.Second},
		{attempt: 10, want: DefaultMaxRetryDelay},
	}
	for _, tt := range tests {
		if got := retryDelayMs(policy, tt.attempt); got != tt.want {
			t.Errorf("retryDelayMs(attempt %d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

// countingProduce 记录调用次数并按序返回预设结果
func countingProduce(calls *int, results ...func() (string, llm.Usage, error)) func() (string, llm.Usage, error) {
	return func() (string, llm.Usage, error) {
		i := *calls
		*calls++
		if i >= len(results) {
			i = len(results) - 1
		}
		return results[i]()
	}
}

func fastRetryPolicy() RetryPolicy {
	return RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
}

func TestRetrySummarizeRetriesTransientErrors(t *testing.T) {
	calls := 0
	produce := countingProduce(&calls,
		func() (string, llm.Usage, error) {
			return "", llm.Usage{}, errors.New("openai: 429: too many requests")
		},
		func() (string, llm.Usage, error) {
			return "", llm.Usage{}, errors.New("openai: 503: service unavailable")
		},
		func() (string, llm.Usage, error) { return "SUMMARY", llm.Usage{InputTokens: 10}, nil },
	)
	text, _, err := retrySummarize(context.Background(), fastRetryPolicy(), produce)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("produce calls = %d, want 3", calls)
	}
	if text != "SUMMARY" {
		t.Errorf("text = %q, want SUMMARY", text)
	}
}

func TestRetrySummarizeFailsFastOnDeterministicError(t *testing.T) {
	calls := 0
	produce := countingProduce(&calls,
		func() (string, llm.Usage, error) { return "", llm.Usage{}, errors.New("insufficient_quota: billing") },
	)
	if _, _, err := retrySummarize(context.Background(), fastRetryPolicy(), produce); err == nil {
		t.Fatal("deterministic error should fail immediately")
	}
	if calls != 1 {
		t.Errorf("produce calls = %d, want 1", calls)
	}
}

func TestRetrySummarizeNeverRetriesAbort(t *testing.T) {
	calls := 0
	produce := countingProduce(&calls,
		func() (string, llm.Usage, error) { return "", llm.Usage{}, context.Canceled },
	)
	if _, _, err := retrySummarize(context.Background(), fastRetryPolicy(), produce); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("produce calls = %d, want 1", calls)
	}
}

func TestRetrySummarizeExhaustsRetries(t *testing.T) {
	calls := 0
	produce := countingProduce(&calls,
		func() (string, llm.Usage, error) {
			return "", llm.Usage{}, errors.New("openai: 503: service unavailable")
		},
	)
	policy := RetryPolicy{Enabled: true, MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	if _, _, err := retrySummarize(context.Background(), policy, produce); err == nil {
		t.Fatal("exhausted retries should return the error")
	}
	// 初次调用不计入重试次数
	if calls != 3 {
		t.Errorf("produce calls = %d, want 3", calls)
	}
}

func TestRetrySummarizeRespondsToCancelDuringBackoff(t *testing.T) {
	calls := 0
	produce := countingProduce(&calls,
		func() (string, llm.Usage, error) { return "", llm.Usage{}, errors.New("openai: 500: server error") },
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy := RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelay: time.Hour, MaxDelay: time.Hour}
	if _, _, err := retrySummarize(ctx, policy, produce); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled during backoff", err)
	}
}

func TestValidateSummary(t *testing.T) {
	if err := validateSummary(&llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}}); err != nil {
		t.Errorf("valid summary rejected: %v", err)
	}
	if err := validateSummary(&llm.AssistantMessage{StopReason: llm.StopReasonLength}); err == nil {
		t.Error("truncated summary should be rejected")
	}
	if err := validateSummary(&llm.AssistantMessage{StopReason: llm.StopReasonError}); err == nil {
		t.Error("errored summary should be rejected")
	}
	if err := validateSummary(&llm.AssistantMessage{Content: []llm.Content{
		llm.TextContent{Text: "ok"},
		llm.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{}},
	}}); err == nil {
		t.Error("summary with tool call should be rejected")
	}
}
