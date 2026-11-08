package provider

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
)

func TestLoadCodemodeAcrossProviderSelectionPaths(t *testing.T) {
	for _, selection := range []string{"paired", "builtin", "first available", "invalid paired fallback", "broken models fallback"} {
		for _, setting := range []struct {
			name string
			json string
			want *bool
		}{
			{name: "unspecified", json: `{"theme":"dark"}`},
			{name: "empty", json: `{"codemode":{}}`},
			{name: "true", json: `{"codemode":{"enabled":true}}`, want: new(true)},
			{name: "false", json: `{"codemode":{"enabled":false}}`, want: new(false)},
		} {
			t.Run(selection+"/"+setting.name, func(t *testing.T) {
				dir := useConfigHome(t)
				body := setting.json
				wantProvider := deepseek.ProviderID
				switch selection {
				case "paired":
					body = `{"defaultProvider":"custom","defaultModel":"custom-model",` + body[1:]
					wantProvider = "custom"
				case "first available":
					wantProvider = "custom"
				case "invalid paired fallback":
					body = `{"defaultProvider":"missing","defaultModel":"missing",` + body[1:]
				}
				writeAgentConfig(t, dir, "settings.json", body)
				if selection != "first available" {
					t.Setenv("DEEPSEEK_API_KEY", "test-key")
				}
				if selection == "broken models fallback" {
					writeAgentConfig(t, dir, "models.json", `{broken`)
				} else {
					writeAgentConfig(t, dir, "models.json", `{"providers":{"custom":{"baseUrl":"http://127.0.0.1:1/v1","apiKey":"test-key","models":[{"id":"custom-model"}]}}}`)
				}
				cfg, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				if cfg.ProviderID != wantProvider {
					t.Fatalf("provider = %q, want %q", cfg.ProviderID, wantProvider)
				}
				got := cfg.Codemode.Enabled
				if setting.want == nil {
					if got != nil {
						t.Fatalf("enabled = %v, want unspecified", *got)
					}
				} else if got == nil || *got != *setting.want {
					t.Fatalf("enabled = %v, want %v", got, *setting.want)
				}
				if selection == "broken models fallback" && len(cfg.Warnings) != 1 {
					t.Fatalf("warnings = %v, want models parse warning", cfg.Warnings)
				}
			})
		}
	}
}

func TestLoadBrokenCodemodeSettingsWarnsAndDefaults(t *testing.T) {
	for _, body := range []string{"", `{"codemode":{"enabled":"false"}}`, `{"codemode":{"enabled":false},broken`} {
		t.Run(body, func(t *testing.T) {
			dir := useConfigHome(t)
			t.Setenv("DEEPSEEK_API_KEY", "test-key")
			if body != "" {
				writeAgentConfig(t, dir, "settings.json", body)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Codemode.Enabled != nil {
				t.Fatalf("invalid or absent settings disabled codemode: %+v", cfg.Codemode)
			}
			wantWarnings := 0
			if body != "" {
				wantWarnings = 1
			}
			if len(cfg.Warnings) != wantWarnings {
				t.Fatalf("warnings = %v, want %d parse warnings", cfg.Warnings, wantWarnings)
			}
		})
	}
}
