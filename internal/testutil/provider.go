package testutil

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// Provider 是 provider 包对外暴露的最小面，契约断言只依赖这些方法
type Provider interface {
	Models() []llm.Model
	Stream(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error)
}

// ProviderContract 描述一个 provider 必须满足的契约
type ProviderContract struct {
	// ProviderID 是模型上必须标注的 provider 名
	ProviderID string
	// DefaultModel 是默认模型 ID
	DefaultModel string
	// Models 是模型目录，顺序必须与 Models() 一致
	Models []string
	// ContextWindow 与 MaxTokens 是所有内置模型的统一限额
	ContextWindow int
	MaxTokens     int
	// DefaultAPI 是默认模型使用的协议
	DefaultAPI llm.API
	// SupportsImages 表示默认模型是否支持图片
	SupportsImages bool
}

// AssertProviderContract 校验 provider 的模型目录与默认模型元数据
func AssertProviderContract(t *testing.T, newProvider func() Provider, want ProviderContract) {
	t.Helper()

	p := newProvider()
	if p == nil {
		t.Fatal("New() = nil, want non-nil")
	}
	models := p.Models()
	if len(models) != len(want.Models) {
		t.Fatalf("model count = %d, want %d", len(models), len(want.Models))
	}
	for i, model := range models {
		if model.ID != want.Models[i] {
			t.Errorf("model[%d].ID = %q, want %q", i, model.ID, want.Models[i])
		}
		if model.ContextWindow != want.ContextWindow || model.MaxTokens != want.MaxTokens {
			t.Errorf("model[%d] limits = %d/%d, want %d/%d",
				i, model.ContextWindow, model.MaxTokens, want.ContextWindow, want.MaxTokens)
		}
		if model.Provider != want.ProviderID {
			t.Errorf("model[%d].Provider = %q, want %q", i, model.Provider, want.ProviderID)
		}
	}

	var found bool
	for _, model := range models {
		if model.ID != want.DefaultModel {
			continue
		}
		found = true
		if model.API != want.DefaultAPI {
			t.Errorf("default model API = %q, want %q", model.API, want.DefaultAPI)
		}
		if model.SupportsImages != want.SupportsImages {
			t.Errorf("default model SupportsImages = %v, want %v", model.SupportsImages, want.SupportsImages)
		}
	}
	if !found {
		t.Errorf("Models() lacks default model %q", want.DefaultModel)
	}
}

// AssertRejectsUnknownProtocol 校验未知协议被拒绝，而不是静默降级
func AssertRejectsUnknownProtocol(t *testing.T, p Provider, modelID string) {
	t.Helper()
	if _, err := p.Stream(context.Background(), llm.Model{ID: modelID, API: llm.API("unknown")}, llm.Context{}, llm.Options{}); err == nil {
		t.Error("Stream(unknown API) error = nil, want a protocol mismatch error")
	}
}
