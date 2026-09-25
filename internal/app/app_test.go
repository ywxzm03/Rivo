package app

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

func TestNewAcceptsDefaultModel(t *testing.T) {
	a, err := New(Config{
		ProviderID: openai.ProviderID,
		ModelID:    openai.DefaultModel,
		APIKey:     "test-key",
	}, nil)
	if err != nil {
		t.Fatalf("New() with default model %q error = %v, want nil", openai.DefaultModel, err)
	}
	if a == nil {
		t.Fatal("New() app = nil, want non-nil")
	}
	if len(a.agent.AgentContext.Tools) != 3 || a.agent.AgentContext.Tools[2].Name() != "grep" {
		t.Errorf("registered tools = %d, want read, ls, grep", len(a.agent.AgentContext.Tools))
	}
}

func TestNewAcceptsDeepSeekDefaultModel(t *testing.T) {
	a, err := New(Config{
		ProviderID: deepseek.ProviderID,
		ModelID:    deepseek.DefaultModel,
		APIKey:     "test-key",
	}, nil)
	if err != nil {
		t.Fatalf("New() with default model %q error = %v, want nil", deepseek.DefaultModel, err)
	}
	if a == nil {
		t.Fatal("New() app = nil, want non-nil")
	}
}

func TestNewRejectsUnknownModel(t *testing.T) {
	_, err := New(Config{
		ProviderID: openai.ProviderID,
		ModelID:    "no-such-model",
		APIKey:     "test-key",
	}, nil)
	if err == nil {
		t.Fatal("New() error = nil, want unknown model error")
	}
	if !strings.Contains(err.Error(), "no-such-model") {
		t.Errorf("New() error = %q, want it to name the model id", err)
	}
}

func TestNewRejectsModelFromOtherProvider(t *testing.T) {
	// 拿 OpenAI 的模型名去 deepseek 的目录里查，必须落空
	_, err := New(Config{
		ProviderID: deepseek.ProviderID,
		ModelID:    openai.DefaultModel,
		APIKey:     "test-key",
	}, nil)
	if err == nil {
		t.Fatal("New() error = nil, want unknown model error")
	}
	if !strings.Contains(err.Error(), openai.DefaultModel) {
		t.Errorf("New() error = %q, want it to name the model id", err)
	}
}

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(Config{
		ProviderID: "no-such-provider",
		ModelID:    openai.DefaultModel,
		APIKey:     "test-key",
	}, nil)
	if err == nil {
		t.Fatal("New() error = nil, want unknown provider error")
	}
	if !strings.Contains(err.Error(), "no-such-provider") {
		t.Errorf("New() error = %q, want it to name the provider id", err)
	}
}
