package openai

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestNewRegistersBuiltinModels(t *testing.T) {
	p := New(Config{APIKey: "test-key"})

	var found bool
	for _, m := range p.Models() {
		if m.ID != DefaultModel {
			continue
		}
		found = true
		if m.API != llm.APIOpenAIResponses {
			t.Errorf("%s API = %q, want %q", m.ID, m.API, llm.APIOpenAIResponses)
		}
		if m.Provider != ProviderID {
			t.Errorf("%s Provider = %q, want %q", m.ID, m.Provider, ProviderID)
		}
		if !m.SupportsImages {
			t.Errorf("%s SupportsImages = false, want true", m.ID)
		}
	}
	if !found {
		t.Errorf("Models() lacks default model %q", DefaultModel)
	}
}

func TestNewAcceptsEmptyConfig(t *testing.T) {
	if p := New(Config{}); p == nil {
		t.Fatal("New() = nil, want non-nil")
	}
}

func TestBuiltinModelCatalog(t *testing.T) {
	models := New(Config{}).Models()
	want := []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna"}
	if len(models) != len(want) {
		t.Fatalf("model count = %d, want %d", len(models), len(want))
	}
	for i, model := range models {
		if model.ID != want[i] || model.ContextWindow != 1_050_000 || model.MaxTokens != 128_000 {
			t.Errorf("model[%d] = %+v, want %s with current limits", i, model, want[i])
		}
	}
}

func TestStreamRejectsUnknownProtocol(t *testing.T) {
	p := New(Config{})
	_, err := p.Stream(context.Background(),
		llm.Model{ID: DefaultModel, API: llm.API("unknown")}, llm.Context{}, llm.Options{})
	if err == nil {
		t.Error("Stream() error = nil, want a protocol mismatch error")
	}
}
