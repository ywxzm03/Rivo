package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirUsesEnvOverride(t *testing.T) {
	// RIVO_AGENT_DIR 覆盖优先于 HOME/USERPROFILE（对齐 pi 的 PI_CODING_AGENT_DIR）
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("RIVO_AGENT_DIR", filepath.Join(home, "custom-agent-dir"))
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	if want := filepath.Join(home, "custom-agent-dir"); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}

func TestDirDefaultsToHomeAndProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("RIVO_AGENT_DIR", "")
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	// 无覆盖时回落用户主目录；Windows 的 UserHomeDir 读 USERPROFILE，Unix 读 HOME
	if want := filepath.Join(home, ".rivo", "agent"); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}

func TestLoadSettingsMissingFileReturnsZero(t *testing.T) {
	t.Setenv("RIVO_AGENT_DIR", t.TempDir())
	s, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if s.DefaultProvider != "" || s.DefaultModel != "" {
		t.Errorf("LoadSettings() = %+v, want zero value", s)
	}
}

func TestLoadSettingsReadsDefaultModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	dir := home
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"theme":"dark","defaultProvider":"magpie","defaultModel":"group/auto-glm-5-3"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	// 未知字段（theme）忽略，默认模型字段正确读出
	if s.DefaultProvider != "magpie" || s.DefaultModel != "group/auto-glm-5-3" {
		t.Errorf("LoadSettings() = %+v, want magpie defaults", s)
	}
}

func TestLoadSettingsInvalidJSONErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	dir := home
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(); err == nil {
		t.Fatal("LoadSettings() error = nil, want invalid JSON error")
	}
}

func TestLoadModelsMissingFileReturnsEmpty(t *testing.T) {
	t.Setenv("RIVO_AGENT_DIR", t.TempDir())
	m, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if len(m.Providers) != 0 {
		t.Errorf("LoadModels() = %d providers, want 0", len(m.Providers))
	}
}

func writeModelsFile(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", home)
	dir := home
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadModelsPreservesOrderAndDefaultsAPI(t *testing.T) {
	writeModelsFile(t, `{
		"providers": {
			"magpie": {"name": "Magpie", "baseUrl": "http://127.0.0.1:3425/v1", "apiKey": "magpie", "models": [{"id": "group/auto-glm-5-3"}]},
			"proxy": {"baseUrl": "http://127.0.0.1:9000/v1", "models": [{"id": "m1"}]}
		}
	}`)

	m, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if len(m.Providers) != 2 {
		t.Fatalf("LoadModels() = %d providers, want 2", len(m.Providers))
	}
	// 1.保持文件顺序
	if m.Providers[0].ID != "magpie" || m.Providers[1].ID != "proxy" {
		t.Errorf("provider order = [%s, %s], want [magpie, proxy]", m.Providers[0].ID, m.Providers[1].ID)
	}
	// 2.api 缺省补 openai-responses
	if got := m.Providers[1].Provider.API; got != APIOpenAIResponses {
		t.Errorf("default api = %q, want %q", got, APIOpenAIResponses)
	}
}

func TestLoadModelsInterpolatesEnvAndKeepsPlaintext(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "env-secret")
	writeModelsFile(t, `{
		"providers": {
			"a": {"baseUrl": "http://a/v1", "apiKey": "$TEST_PROVIDER_KEY", "models": [{"id": "m"}]},
			"b": {"baseUrl": "http://b/v1", "apiKey": "${TEST_PROVIDER_KEY}", "models": [{"id": "m"}]},
			"c": {"baseUrl": "http://c/v1", "apiKey": "plain", "models": [{"id": "m"}]},
			"d": {"baseUrl": "http://d/v1", "apiKey": "$UNSET_VAR_XYZ", "models": [{"id": "m"}]}
		}
	}`)

	m, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	got := map[string]string{}
	for _, np := range m.Providers {
		got[np.ID] = np.Provider.APIKey
	}
	// $VAR 与 ${VAR} 等价；明文原样；未设置变量替换为空串
	for _, id := range []string{"a", "b"} {
		if got[id] != "env-secret" {
			t.Errorf("provider %s apiKey = %q, want interpolated value", id, got[id])
		}
	}
	if got["c"] != "plain" {
		t.Errorf("plaintext apiKey = %q, want plain", got["c"])
	}
	if got["d"] != "" {
		t.Errorf("unset env apiKey = %q, want empty", got["d"])
	}
}

func TestLoadModelsValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "unsupported api",
			body:    `{"providers": {"x": {"baseUrl": "http://x/v1", "api": "chat-completions", "models": [{"id": "m"}]}}}`,
			wantErr: `unsupported api "chat-completions"`,
		},
		{
			name:    "missing baseUrl",
			body:    `{"providers": {"x": {"apiKey": "k", "models": [{"id": "m"}]}}}`,
			wantErr: `provider "x" is missing baseUrl`,
		},
		{
			name:    "no models",
			body:    `{"providers": {"x": {"baseUrl": "http://x/v1"}}}`,
			wantErr: `provider "x" has no models`,
		},
		{
			name:    "model without id",
			body:    `{"providers": {"x": {"baseUrl": "http://x/v1", "models": [{"name": "n"}]}}}`,
			wantErr: `provider "x" has a model without id`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeModelsFile(t, tt.body)
			_, err := LoadModels()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadModels() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadModelsRequiresProvidersWrapper(t *testing.T) {
	writeModelsFile(t, `{"magpie": {"baseUrl": "http://x/v1", "models": [{"id": "m"}]}}`)
	if _, err := LoadModels(); err == nil || !strings.Contains(err.Error(), `"providers"`) {
		t.Fatalf("LoadModels() error = %v, want missing providers object", err)
	}
}

func TestLoadModelsReadsProviderName(t *testing.T) {
	writeModelsFile(t, `{"providers": {"magpie": {"name": "Magpie", "baseUrl": "http://x/v1", "models": [{"id": "m"}]}}}`)
	m, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if m.Providers[0].Provider.Name != "Magpie" {
		t.Errorf("provider name = %q, want Magpie", m.Providers[0].Provider.Name)
	}
}

func TestLoadModelsRejectsDuplicateProvidersKey(t *testing.T) {
	writeModelsFile(t, `{"providers": {"a": {"baseUrl": "http://a/v1", "models": [{"id": "m"}]}}, "providers": {"b": {"baseUrl": "http://b/v1", "models": [{"id": "m"}]}}}`)
	_, err := LoadModels()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("LoadModels() error = %v, want duplicate providers key error", err)
	}
}
