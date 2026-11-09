package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodemodeSettingsPreservesExplicitEnabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want *bool
	}{
		{name: "missing file"},
		{name: "missing section", body: `{}`},
		{name: "empty section", body: `{"codemode":{}}`},
		{name: "enabled", body: `{"codemode":{"enabled":true}}`, want: new(true)},
		{name: "disabled", body: `{"codemode":{"enabled":false}}`, want: new(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("RIVO_AGENT_DIR", dir)
			if tc.body != "" {
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			settings, err := LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			got := settings.Codemode.Enabled
			if tc.want == nil {
				if got != nil {
					t.Fatalf("enabled = %v, want unspecified", *got)
				}
			} else if got == nil || *got != *tc.want {
				t.Fatalf("enabled = %v, want %v", got, *tc.want)
			}
		})
	}
}
