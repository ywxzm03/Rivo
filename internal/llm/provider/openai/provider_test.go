package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/testutil"
)

// 内置模型目录与默认模型的元数据由共享契约断言校验，这里只声明差异
func TestBuiltinProviderContract(t *testing.T) {
	testutil.AssertProviderContract(t, func() testutil.Provider {
		return New(Config{APIKey: "test-key"})
	}, testutil.ProviderContract{
		ProviderID:     ProviderID,
		DefaultModel:   DefaultModel,
		Models:         []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna"},
		ContextWindow:  1_050_000,
		MaxTokens:      128_000,
		DefaultAPI:     llm.APIOpenAIResponses,
		SupportsImages: true,
	})
}

// Stream 必须把两种协议都路由到对应实现，而不是只支持一种
func TestStreamRoutesBothProtocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 两种协议都返回空流，只验证路由成功
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL})
	for _, api := range []llm.API{llm.APIOpenAIResponses, llm.APIOpenAICompletions} {
		t.Run(string(api), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			events, err := p.Stream(ctx, llm.Model{ID: DefaultModel, API: api, Provider: ProviderID}, llm.Context{}, llm.Options{})
			if err != nil {
				t.Fatalf("Stream(%s) error = %v, want the request to be routed", api, err)
			}
			// 排空通道，避免客户端 goroutine 泄漏
			for range events {
			}
		})
	}
}

func TestStreamRejectsUnknownProtocol(t *testing.T) {
	testutil.AssertRejectsUnknownProtocol(t, New(Config{}), DefaultModel)
}
