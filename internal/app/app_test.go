package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
	"github.com/ywxzm03/Rivo/internal/provider"
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
	tools := a.agent.Context().Tools
	wantTools := 11
	if runtime.GOOS == "windows" {
		wantTools = 12
	}
	if len(tools) != wantTools || tools[4].Name() != "bash" || tools[6].Name() != "edit" {
		t.Errorf("registered tools = %d, want %d tools with bash and edit", len(tools), wantTools)
	}
	// codemode 追加在所有直接工具之后
	if tools[len(tools)-1].Name() != "codemode" {
		t.Errorf("last registered tool = %q, want codemode", tools[len(tools)-1].Name())
	}
	if runtime.GOOS == "windows" && tools[len(tools)-2].Name() != "powershell" {
		t.Errorf("last direct tool = %q, want powershell", tools[len(tools)-2].Name())
	}
}

func TestNewInjectsSessionIDIntoAgentConfig(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := a.agent.Config().SessionID, a.SessionID(); got != want || got == "" {
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
	cfg = cfg.WithProviderForTest(customModel.Provider, []llm.Model{customModel},
		func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
			return nil, nil
		})
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
	read := a.agent.Context().Tools[0].(*tool.Read)
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
	if !a.AutoCompactionEnabled() || !a.agent.Context().Tools[0].(*tool.Read).SupportsImages {
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
func configWithBuiltin(providerID, modelID string) provider.Config {
	return provider.NewBuiltinForTest(providerID, modelID)
}

// 配置里的 thinking level 必须在新会话装配时生效
func TestNewSessionAppliesConfiguredThinkingLevel(t *testing.T) {
	cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
	cfg.ThinkingLevel = llm.ThinkingLevelHigh

	a, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if a.agent.Config().ThinkingLevel != llm.ThinkingLevelHigh {
		t.Errorf("agent ThinkingLevel = %q, want high", a.agent.Config().ThinkingLevel)
	}
}

// 内置模型被 modelOverrides 改过窗口后，装配出的 compactor 必须同步
func TestNewAppliesModelOverrideContextWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("RIVO_AGENT_DIR", filepath.Join(dir, "agent"))
	t.Setenv("OPENAI_API_KEY", "env-key")
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"providers":{"openai":{"modelOverrides":{"` + openai.DefaultModel + `":{"contextWindow":64000}}}}}`
	if err := os.WriteFile(filepath.Join(dir, "agent", "models.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := provider.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	a, err := NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewWithSessionDir() error = %v", err)
	}
	if a.CurrentModel().ContextWindow != 64000 {
		t.Errorf("model context window = %d, want 64000", a.CurrentModel().ContextWindow)
	}
	if a.coordinator.Compactor == nil || a.coordinator.Compactor.Config.ContextWindow != 64000 {
		t.Errorf("compactor config = %+v, want 64000", a.coordinator.Compactor)
	}
}
