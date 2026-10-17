package deepseek

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
		if m.API != llm.APIOpenAICompletions {
			t.Errorf("%s API = %q, want %q", m.ID, m.API, llm.APIOpenAICompletions)
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

func TestNewFallsBackToDefaultBaseURL(t *testing.T) {
	if p := New(Config{}); p == nil {
		t.Fatal("New() = nil, want non-nil")
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
