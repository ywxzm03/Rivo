package app

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

func TestNewWithSessionDirUsesInjectedDir(t *testing.T) {
	dir := t.TempDir()
	a, err := NewWithSessionDir(Config{
		ProviderID: openai.ProviderID,
		ModelID:    openai.DefaultModel,
		APIKey:     "test-key",
	}, dir, nil)
	if err != nil {
		t.Fatalf("NewWithSessionDir() error = %v", err)
	}
	if a.session == nil {
		t.Fatal("session manager = nil, want non-nil")
	}
}

func TestNewWithSessionDirAcceptsDeepSeek(t *testing.T) {
	a, err := NewWithSessionDir(Config{
		ProviderID: deepseek.ProviderID,
		ModelID:    deepseek.DefaultModel,
		APIKey:     "test-key",
	}, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewWithSessionDir() error = %v", err)
	}
	if a.session == nil {
		t.Fatal("session manager = nil, want non-nil")
	}
}
