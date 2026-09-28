//go:build windows

package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindBashUsesGitInstallRoot(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "bin", "bash.exe")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GIT_INSTALL_ROOT", root)
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LocalAppData", "")

	got, err := findBash()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("findBash() = %q, want %q", got, want)
	}
}
