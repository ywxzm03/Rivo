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
			wantErr:      "API_KEY 未设置",
			wantProvider: deepseek.ProviderID,
		},
		{
			name:         "blank api key",
			env:          map[string]string{apiKeyEnv: "   "},
			wantErr:      "API_KEY 未设置",
			wantProvider: deepseek.ProviderID,
		},
		{
			name:         "未设置 PROVIDER 时落到默认厂商的模型",
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
			name:         "deepseek 用自己的默认模型",
			env:          map[string]string{providerEnv: "deepseek", apiKeyEnv: "test-key"},
			wantProvider: deepseek.ProviderID,
			wantModel:    deepseek.DefaultModel,
		},
		{
			name:         "provider 大小写不敏感",
			env:          map[string]string{providerEnv: "DeepSeek", apiKeyEnv: "test-key"},
			wantProvider: deepseek.ProviderID,
			wantModel:    deepseek.DefaultModel,
		},
		{
			name:         "provider 两侧空白被裁掉",
			env:          map[string]string{providerEnv: "  openai  ", apiKeyEnv: "test-key"},
			wantProvider: openai.ProviderID,
			wantModel:    openai.DefaultModel,
		},
		{
			name:    "未知厂商报错",
			env:     map[string]string{providerEnv: "no-such-provider", apiKeyEnv: "test-key"},
			wantErr: `未知的厂商 "no-such-provider"`,
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
