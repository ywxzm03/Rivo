package app

import (
	"runtime"
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
	wantTools := 7
	if runtime.GOOS == "windows" {
		wantTools = 8
	}
	if len(a.agent.AgentContext.Tools) != wantTools || a.agent.AgentContext.Tools[4].Name() != "bash" || a.agent.AgentContext.Tools[6].Name() != "edit" {
		t.Errorf("registered tools = %d, want %d tools with bash and edit", len(a.agent.AgentContext.Tools), wantTools)
	}
	if runtime.GOOS == "windows" && a.agent.AgentContext.Tools[7].Name() != "powershell" {
		t.Errorf("last registered tool = %q, want powershell", a.agent.AgentContext.Tools[7].Name())
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
