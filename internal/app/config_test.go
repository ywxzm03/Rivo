package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

// useEnvFile 造一个空的 .env 并把工作目录切过去
// 空文件保证 godotenv 不会覆盖 t.Setenv 注入的值（loadFile 是 overload=false）
func useEnvFile(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

func TestConfigFromEnv(t *testing.T) {
	useEnvFile(t)

	tests := []struct {
		name         string
		env          map[string]string
		wantErr      string
		wantProvider string
		wantModel    string
		wantBase     string
	}{
		{
			name:         "missing api key",
			env:          map[string]string{},
			wantErr:      "API_KEY is not set",
			wantProvider: deepseek.ProviderID,
		},
		{
			name:         "blank api key",
			env:          map[string]string{apiKeyEnv: "   "},
			wantErr:      "API_KEY is not set",
			wantProvider: deepseek.ProviderID,
		},
		{
			name:         "falls back to default provider model when PROVIDER is unset",
			env:          map[string]string{apiKeyEnv: "test-key"},
			wantProvider: deepseek.ProviderID,
			wantModel:    deepseek.DefaultModel,
		},
		{
			name:         "blank model env falls back to default",
			env:          map[string]string{apiKeyEnv: "test-key", modelEnv: "  "},
			wantProvider: deepseek.ProviderID,
			wantModel:    deepseek.DefaultModel,
		},
		{
			name:         "model env overrides default",
			env:          map[string]string{apiKeyEnv: "test-key", modelEnv: "deepseek-v4-pro"},
			wantProvider: deepseek.ProviderID,
			wantModel:    "deepseek-v4-pro",
		},
		{
			name:         "trims api key and base url",
			env:          map[string]string{apiKeyEnv: "  test-key  ", baseURLEnv: " https://example.test/v1 "},
			wantProvider: deepseek.ProviderID,
			wantModel:    deepseek.DefaultModel,
			wantBase:     "https://example.test/v1",
		},
		{
			name:         "deepseek uses its own default model",
			env:          map[string]string{providerEnv: "deepseek", apiKeyEnv: "test-key"},
			wantProvider: deepseek.ProviderID,
			wantModel:    deepseek.DefaultModel,
		},
		{
			name:         "provider is case-insensitive",
			env:          map[string]string{providerEnv: "DeepSeek", apiKeyEnv: "test-key"},
			wantProvider: deepseek.ProviderID,
			wantModel:    deepseek.DefaultModel,
		},
		{
			name:         "provider is trimmed of surrounding whitespace",
			env:          map[string]string{providerEnv: "  openai  ", apiKeyEnv: "test-key"},
			wantProvider: openai.ProviderID,
			wantModel:    openai.DefaultModel,
		},
		{
			name:    "unknown provider errors",
			env:     map[string]string{providerEnv: "no-such-provider", apiKeyEnv: "test-key"},
			wantErr: `unknown provider "no-such-provider"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{providerEnv, apiKeyEnv, baseURLEnv, modelEnv} {
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
			if cfg.ProviderID != tt.wantProvider {
				t.Errorf("ConfigFromEnv() ProviderID = %q, want %q", cfg.ProviderID, tt.wantProvider)
			}
			if cfg.ModelID != tt.wantModel {
				t.Errorf("ConfigFromEnv() ModelID = %q, want %q", cfg.ModelID, tt.wantModel)
			}
			if cfg.BaseURL != tt.wantBase {
				t.Errorf("ConfigFromEnv() BaseURL = %q, want %q", cfg.BaseURL, tt.wantBase)
			}
			if cfg.APIKey == "" {
				t.Error("ConfigFromEnv() APIKey is empty")
			}
		})
	}
}
