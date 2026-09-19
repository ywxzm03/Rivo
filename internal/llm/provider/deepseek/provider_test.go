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
		if m.API != llm.APIOpenAIResponses {
			t.Errorf("%s 的 API = %q, want %q", m.ID, m.API, llm.APIOpenAIResponses)
		}
		if m.Provider != ProviderID {
			t.Errorf("%s 的 Provider = %q, want %q", m.ID, m.Provider, ProviderID)
		}
	}
	if !found {
		t.Errorf("Models() 里没有默认模型 %q", DefaultModel)
	}
}

func TestNewFallsBackToDefaultBaseURL(t *testing.T) {
	if p := New(Config{}); p == nil {
		t.Fatal("New() = nil, want 非空")
	}
}

func TestStreamRejectsUnknownProtocol(t *testing.T) {
	p := New(Config{})
	_, err := p.Stream(context.Background(),
		llm.Model{ID: DefaultModel, API: llm.API("unknown")}, llm.Context{}, llm.Options{})
	if err == nil {
		t.Error("Stream() error = nil, want 协议不匹配报错")
	}
}
