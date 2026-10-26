package provider

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

// Resolve 用默认模型兜底空 modelID，并原样返回 provider 的流函数
func TestResolveUsesDefaultModelAndReturnsStream(t *testing.T) {
	cfg := NewBuiltinForTest(openai.ProviderID, "")
	resolved, err := cfg.Resolve(openai.ProviderID, "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.ProviderID != openai.ProviderID || resolved.Model.ID != openai.DefaultModel {
		t.Errorf("resolved = %s/%s, want %s/%s", resolved.ProviderID, resolved.Model.ID, openai.ProviderID, openai.DefaultModel)
	}
	if resolved.Stream == nil {
		t.Error("Resolve() Stream = nil, want provider stream function")
	}
}

// 未知 provider 与未知模型都必须报错，不能返回零值
func TestResolveRejectsUnknownProviderAndModel(t *testing.T) {
	cfg := NewBuiltinForTest(openai.ProviderID, openai.DefaultModel)
	if _, err := cfg.Resolve("no-such-provider", ""); err == nil {
		t.Error("Resolve(unknown provider) error = nil, want error")
	}
	if _, err := cfg.Resolve(openai.ProviderID, "no-such-model"); err == nil {
		t.Error("Resolve(unknown model) error = nil, want error")
	}
}

// DefaultModelID 只对已登记的 provider 返回模型名，未知 provider 返回空串
func TestDefaultModelID(t *testing.T) {
	cfg := NewBuiltinForTest(deepseek.ProviderID, deepseek.DefaultModel)
	if got := cfg.DefaultModelID(deepseek.ProviderID); got != deepseek.DefaultModel {
		t.Errorf("DefaultModelID(%s) = %q, want %q", deepseek.ProviderID, got, deepseek.DefaultModel)
	}
	if got := cfg.DefaultModelID("no-such-provider"); got != "" {
		t.Errorf("DefaultModelID(unknown) = %q, want empty", got)
	}
}

// modelOverrides 的每一类字段都要能覆盖到模型上，负数成本必须被拒
func TestApplyModelOverrideCoversEveryField(t *testing.T) {
	name, reasoning, contextWindow, maxTokens := "renamed", false, 128000, 8192
	one, two, three, four := 1.0, 2.0, 3.0, 4.0
	cost := config.CostOverride{Input: &one, Output: &two, CacheRead: &three, CacheWrite: &four}
	model := llm.Model{
		ID: "gpt-5.6", Name: "original", Provider: "openai",
		Reasoning: true, SupportsImages: true, ContextWindow: 400000, MaxTokens: 128000,
		Cost: &llm.CostRates{Input: 10},
		ThinkingLevelMap: map[llm.ThinkingLevel]*llm.ReasoningEffort{
			llm.ThinkingLevelHigh: effortPtr(llm.ReasoningEffortHigh),
		},
		SamplingParams: map[string]any{"temperature": 1.0},
	}
	override := config.ModelOverride{
		Name: &name, Reasoning: &reasoning,
		Input:         []string{"text"},
		Cost:          &cost,
		ContextWindow: &contextWindow, MaxTokens: &maxTokens,
		ThinkingLevelMap: map[string]*string{"low": stringPtr("low")},
		SamplingParams:   map[string]any{"top_p": 0.9},
	}
	got := applyModelOverride(model, override)

	if got.Name != name || got.Reasoning != reasoning {
		t.Errorf("name/reasoning = %q/%v, want %q/%v", got.Name, got.Reasoning, name, reasoning)
	}
	if got.SupportsImages {
		t.Error("SupportsImages = true, want false when input has no image")
	}
	if got.ContextWindow != contextWindow || got.MaxTokens != maxTokens {
		t.Errorf("limits = %d/%d, want %d/%d", got.ContextWindow, got.MaxTokens, contextWindow, maxTokens)
	}
	if got.Cost == nil || got.Cost.Input != 1 || got.Cost.CacheWrite != 4 {
		t.Errorf("Cost = %+v, want override values", got.Cost)
	}
	if _, ok := got.ThinkingLevelMap[llm.ThinkingLevelLow]; !ok {
		t.Errorf("ThinkingLevelMap = %v, want low merged in", got.ThinkingLevelMap)
	}
	if _, ok := got.ThinkingLevelMap[llm.ThinkingLevelHigh]; !ok {
		t.Error("ThinkingLevelMap lost existing high entry")
	}
	if got.SamplingParams["top_p"] != 0.9 || got.SamplingParams["temperature"] != 1.0 {
		t.Errorf("SamplingParams = %v, want both keys merged", got.SamplingParams)
	}
	// 覆盖必须发生在副本上，不能改动入参
	if model.Name != "original" || model.ContextWindow != 400000 {
		t.Error("applyModelOverride mutated its input model")
	}
}

// 负数成本与非法 contextWindow 都要在装配前被拒
func TestValidateModelOverridesRejectsInvalidValues(t *testing.T) {
	zero, negativeCost := 0, -1.0
	cases := []struct {
		name     string
		override config.ModelOverride
	}{
		{"零 contextWindow", config.ModelOverride{ContextWindow: &zero}},
		{"负成本", config.ModelOverride{Cost: &config.CostOverride{Input: &negativeCost}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateModelOverrides("p", map[string]config.ModelOverride{"m": tc.override}); err == nil {
				t.Error("validateModelOverrides() error = nil, want error")
			}
		})
	}
	if err := validateModelOverrides("p", nil); err != nil {
		t.Errorf("validateModelOverrides(empty) error = %v, want nil", err)
	}
}

func stringPtr(value string) *string { return &value }

func effortPtr(value llm.ReasoningEffort) *llm.ReasoningEffort { return &value }
