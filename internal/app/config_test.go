package app

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

func TestConfigFromEnv(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantErr   string
		wantModel string
		wantBase  string
	}{
		{
			name:    "missing api key",
			env:     map[string]string{},
			wantErr: "OPENAI_API_KEY 未设置",
		},
		{
			name:      "blank api key",
			env:       map[string]string{apiKeyEnv: "   "},
			wantErr:   "OPENAI_API_KEY 未设置",
			wantModel: "",
		},
		{
			name:      "falls back to default model",
			env:       map[string]string{apiKeyEnv: "test-key"},
			wantModel: defaultModel,
		},
		{
			name:      "blank model env falls back to default",
			env:       map[string]string{apiKeyEnv: "test-key", modelEnv: "  "},
			wantModel: defaultModel,
		},
		{
			name:      "model env overrides default",
			env:       map[string]string{apiKeyEnv: "test-key", modelEnv: "gpt-5.6-luna"},
			wantModel: "gpt-5.6-luna",
		},
		{
			name:      "trims api key and base url",
			env:       map[string]string{apiKeyEnv: "  test-key  ", baseURLEnv: " https://example.test/v1 "},
			wantModel: defaultModel,
			wantBase:  "https://example.test/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{apiKeyEnv, baseURLEnv, modelEnv} {
				t.Setenv(key, "")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			cfg, err := ConfigFromEnv()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ConfigFromEnv() error = nil, want %q", tt.wantErr)
				}
				if got := err.Error(); got != tt.wantErr {
					t.Fatalf("ConfigFromEnv() error = %q, want %q", got, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfigFromEnv() error = %v, want nil", err)
			}
			if cfg.ModelID != tt.wantModel {
				t.Errorf("ConfigFromEnv() ModelID = %q, want %q", cfg.ModelID, tt.wantModel)
			}
			if cfg.BaseURL != tt.wantBase {
				t.Errorf("ConfigFromEnv() BaseURL = %q, want %q", cfg.BaseURL, tt.wantBase)
			}
			if cfg.ProviderID != openai.ProviderID {
				t.Errorf("ConfigFromEnv() ProviderID = %q, want %q", cfg.ProviderID, openai.ProviderID)
			}
			if cfg.APIKey == "" {
				t.Error("ConfigFromEnv() APIKey is empty")
			}
		})
	}
}
