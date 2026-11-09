package provider

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

func TestApplyOverridesUnknownThinkingLevel(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "", "", "ultra"); err == nil {
		t.Fatal("ApplyOverrides() error = nil, want unknown thinking level error")
	}
}

func TestApplyOverridesProviderRequiresModel(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "openai", "", ""); err == nil || !strings.Contains(err.Error(), "--provider requires --model") {
		t.Fatalf("ApplyOverrides() error = %v, want provider-requires-model error", err)
	}
}

func TestApplyOverridesUnknownProvider(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "no-such", "m", ""); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("ApplyOverrides() error = %v, want unknown provider error", err)
	}
}

func TestApplyOverridesExactModelWithProvider(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "openai", openai.DefaultModel, "high"); err != nil {
		t.Fatalf("ApplyOverrides() error = %v", err)
	}
	if cfg.ProviderID != openai.ProviderID || cfg.ModelID != openai.DefaultModel {
		t.Errorf("ApplyOverrides() = %s/%s, want openai default", cfg.ProviderID, cfg.ModelID)
	}
	if cfg.ThinkingLevel != llm.ThinkingLevelHigh {
		t.Errorf("ThinkingLevel = %q, want high", cfg.ThinkingLevel)
	}
}

func TestApplyOverridesSlashInfersProvider(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "", "openai/"+openai.DefaultModel, ""); err != nil {
		t.Fatalf("ApplyOverrides() error = %v", err)
	}
	if cfg.ProviderID != openai.ProviderID {
		t.Errorf("provider = %q, want openai inferred from slash", cfg.ProviderID)
	}
}

func TestApplyOverridesExactAcrossProviders(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "", openai.DefaultModel, ""); err != nil {
		t.Fatalf("ApplyOverrides() error = %v", err)
	}
	if cfg.ProviderID != openai.ProviderID {
		t.Errorf("provider = %q, want openai resolved by exact id", cfg.ProviderID)
	}
}

func TestApplyOverridesAmbiguousExactErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	writeAgentConfig(t, home, "models.json", `{"providers":{
		"p1": {"baseUrl":"http://a/v1","apiKey":"k","models":[{"id":"shared"}]},
		"p2": {"baseUrl":"http://b/v1","apiKey":"k","models":[{"id":"shared"}]}
	}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	err = ApplyOverrides(&cfg, "", "shared", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous across providers") {
		t.Fatalf("ApplyOverrides() error = %v, want ambiguity error", err)
	}
}

func TestApplyOverridesSubstringPicksNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	writeAgentConfig(t, home, "models.json", `{"providers":{"magpie":{
		"baseUrl":"http://x/v1","apiKey":"k",
		"models":[{"id":"glm-5.2"},{"id":"glm-5.3"},{"id":"other"}]
	}}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// 1.A 决策：多个子串命中按 id 字典序取「最新」
	if err := ApplyOverrides(&cfg, "", "glm", ""); err != nil {
		t.Fatalf("ApplyOverrides() error = %v", err)
	}
	if cfg.ModelID != "glm-5.3" {
		t.Errorf("model = %q, want glm-5.3 (newest by id)", cfg.ModelID)
	}
}

func TestApplyOverridesSubstringNoMatch(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "", "no-such-model-xyz", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ApplyOverrides() error = %v, want not-found error", err)
	}
}

func TestApplyOverridesPatternLevelSuffix(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "", openai.DefaultModel+":low", ""); err != nil {
		t.Fatalf("ApplyOverrides() error = %v", err)
	}
	if cfg.ModelID != openai.DefaultModel {
		t.Errorf("model = %q, want pattern without suffix", cfg.ModelID)
	}
	if cfg.ThinkingLevel != llm.ThinkingLevelLow {
		t.Errorf("ThinkingLevel = %q, want low from pattern suffix", cfg.ThinkingLevel)
	}
}

func TestApplyOverridesCLIThinkingWinsOverPatternSuffix(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if err := ApplyOverrides(&cfg, "", openai.DefaultModel+":low", "high"); err != nil {
		t.Fatalf("ApplyOverrides() error = %v", err)
	}
	if cfg.ThinkingLevel != llm.ThinkingLevelHigh {
		t.Errorf("ThinkingLevel = %q, want high (CLI overrides pattern suffix)", cfg.ThinkingLevel)
	}
}

func TestApplyOverridesInvalidPatternLevelFallsToNotFound(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	// 非法 :level 后缀使精确匹配失效，最终按未找到处理（对齐 pi allowInvalidThinkingLevelFallback=false）
	if err := ApplyOverrides(&cfg, "", openai.DefaultModel+":ultra", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ApplyOverrides() error = %v, want not-found error", err)
	}
}

func TestApplyOverridesCombinedSlashAndLevelSuffix(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	// 回归：斜杠推断与 :level 后缀组合时，后缀不得被带回匹配串
	if err := ApplyOverrides(&cfg, "", "openai/"+openai.DefaultModel+":low", ""); err != nil {
		t.Fatalf("ApplyOverrides() error = %v", err)
	}
	if cfg.ProviderID != openai.ProviderID || cfg.ModelID != openai.DefaultModel {
		t.Errorf("ApplyOverrides() = %s/%s, want openai default", cfg.ProviderID, cfg.ModelID)
	}
	if cfg.ThinkingLevel != llm.ThinkingLevelLow {
		t.Errorf("ThinkingLevel = %q, want low from pattern suffix", cfg.ThinkingLevel)
	}
}

func TestApplyOverridesProviderPrefixWithoutModelName(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	err := ApplyOverrides(&cfg, "openai", "openai/", "")
	if err == nil || !strings.Contains(err.Error(), "missing a model name") {
		t.Fatalf("ApplyOverrides() error = %v, want missing-model-name error", err)
	}
}

func TestResolveUsesGlobalReserveTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	writeAgentConfig(t, home, "settings.json", `{"defaultProvider":"custom","defaultModel":"m","reserveTokens":600000}`)
	writeAgentConfig(t, home, "models.json", `{"providers":{"custom":{"baseUrl":"http://x/v1","apiKey":"k","models":[{"id":"m"}]}}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.Resolve("custom", "m")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Model.ReserveTokens != 600000 {
		t.Errorf("ReserveTokens = %d, want 600000 from settings", resolved.Model.ReserveTokens)
	}
}

func TestResolveKeepsModelReserveTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	writeAgentConfig(t, home, "settings.json", `{"defaultProvider":"custom","defaultModel":"m","reserveTokens":600000}`)
	writeAgentConfig(t, home, "models.json", `{"providers":{"custom":{"baseUrl":"http://x/v1","apiKey":"k","models":[{"id":"m","reserveTokens":1234}]}}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.Resolve("custom", "m")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Model.ReserveTokens != 1234 {
		t.Errorf("ReserveTokens = %d, want 1234 from model", resolved.Model.ReserveTokens)
	}
}

func TestResolveAppliesModelOverrideReserveTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	writeAgentConfig(t, home, "settings.json", `{"defaultProvider":"custom","defaultModel":"m","reserveTokens":600000}`)
	writeAgentConfig(t, home, "models.json", `{"providers":{"custom":{"baseUrl":"http://x/v1","apiKey":"k","models":[{"id":"m"}],"modelOverrides":{"m":{"reserveTokens":4321}}}}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.Resolve("custom", "m")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Model.ReserveTokens != 4321 {
		t.Errorf("ReserveTokens = %d, want 4321 from override", resolved.Model.ReserveTokens)
	}
}
