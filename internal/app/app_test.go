package app

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

func TestNewAcceptsDefaultModel(t *testing.T) {
	a, err := New(Config{
		ProviderID: openai.ProviderID,
		ModelID:    defaultModel,
		APIKey:     "test-key",
	}, nil)
	if err != nil {
		t.Fatalf("New() with default model %q error = %v, want nil", defaultModel, err)
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

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(Config{
		ProviderID: "no-such-provider",
		ModelID:    defaultModel,
		APIKey:     "test-key",
	}, nil)
	if err == nil {
		t.Fatal("New() error = nil, want unknown provider error")
	}
	if !strings.Contains(err.Error(), "no-such-provider") {
		t.Errorf("New() error = %q, want it to name the provider id", err)
	}
}
