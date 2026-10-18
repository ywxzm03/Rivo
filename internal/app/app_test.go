package app

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
	"github.com/ywxzm03/Rivo/internal/tool"
)

func TestNewAcceptsDefaultModel(t *testing.T) {
	a, err := New(configWithBuiltin(openai.ProviderID, openai.DefaultModel), nil)
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

func TestNewInjectsSessionIDIntoAgentConfig(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := a.agent.AgentConfig.SessionID, a.SessionID(); got != want || got == "" {
		t.Fatalf("agent SessionID = %q, app SessionID = %q", got, want)
	}
}

func TestNewAcceptsDeepSeekDefaultModel(t *testing.T) {
	a, err := New(configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel), nil)
	if err != nil {
		t.Fatalf("New() with default model %q error = %v, want nil", deepseek.DefaultModel, err)
	}
	if a == nil {
		t.Fatal("New() app = nil, want non-nil")
	}
}

func TestNewRejectsUnknownModel(t *testing.T) {
	_, err := New(configWithBuiltin(openai.ProviderID, "no-such-model"), nil)
	if err == nil {
		t.Fatal("New() error = nil, want unknown model error")
	}
	if !strings.Contains(err.Error(), "no-such-model") {
		t.Errorf("New() error = %q, want it to name the model id", err)
	}
}

func TestNewRejectsModelFromOtherProvider(t *testing.T) {
	// 拿 OpenAI 的模型名去 deepseek 的目录里查，必须落空
	_, err := New(configWithBuiltin(deepseek.ProviderID, openai.DefaultModel), nil)
	if err == nil {
		t.Fatal("New() error = nil, want unknown model error")
	}
	if !strings.Contains(err.Error(), openai.DefaultModel) {
		t.Errorf("New() error = %q, want it to name the model id", err)
	}
}

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(configWithBuiltin("no-such-provider", openai.DefaultModel), nil)
	if err == nil {
		t.Fatal("New() error = nil, want unknown provider error")
	}
	if !strings.Contains(err.Error(), "no-such-provider") {
		t.Errorf("New() error = %q, want it to name the provider id", err)
	}
}

func TestSetModelSwitchesProviderAndRuntimeCapabilities(t *testing.T) {
	customModel := llm.Model{ID: "custom-no-vision", Name: "Custom", Provider: "custom", API: llm.APIOpenAIResponses}
	cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
	cfg.registry[customModel.Provider] = providerSpec{
		id:     customModel.Provider,
		apiKey: "test-key",
		models: []llm.Model{customModel},
		new: func(string, string) ([]llm.Model, llm.StreamFn) {
			return []llm.Model{customModel}, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
				return nil, nil
			}
		},
	}
	a, err := NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.SetModel(customModel.Provider, customModel.ID); err != nil {
		t.Fatalf("SetModel(custom) error = %v", err)
	}
	if got := a.CurrentModel(); got.Provider != customModel.Provider || got.ID != customModel.ID {
		t.Errorf("current model = %s/%s, want %s/%s", got.Provider, got.ID, customModel.Provider, customModel.ID)
	}
	read := a.agent.AgentContext.Tools[0].(*tool.Read)
	if read.SupportsImages {
		t.Error("read tool supports images after switching to a non-vision model")
	}
	if a.AutoCompactionEnabled() {
		t.Error("auto compaction enabled for a model with unknown context window")
	}
	if ctx := a.ContextView(); ctx.Provider != customModel.Provider || ctx.Model != customModel.ID {
		t.Errorf("session model = %s/%s, want %s/%s", ctx.Provider, ctx.Model, customModel.Provider, customModel.ID)
	}

	if err := a.SetModel(deepseek.ProviderID, deepseek.DefaultModel); err != nil {
		t.Fatalf("SetModel(deepseek) error = %v", err)
	}
	if !a.AutoCompactionEnabled() || !a.agent.AgentContext.Tools[0].(*tool.Read).SupportsImages {
		t.Error("switching back to deepseek did not restore compaction/image capabilities")
	}
	if err := a.SetModel(customModel.Provider, "missing"); err == nil {
		t.Fatal("SetModel(missing) error = nil, want validation error")
	}
	if got := a.CurrentModel(); got.Provider != deepseek.ProviderID || got.ID != deepseek.DefaultModel {
		t.Errorf("failed model switch changed current model to %s/%s", got.Provider, got.ID)
	}
}

// configWithBuiltin 返回只含内置 provider 的 Config，供测试装配
func configWithBuiltin(providerID, modelID string) Config {
	return Config{ProviderID: providerID, ModelID: modelID, registry: builtinRegistry()}
}
