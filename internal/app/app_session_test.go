package app

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
	"github.com/ywxzm03/Rivo/internal/tool"
)

func TestNewWithSessionDirUsesInjectedDir(t *testing.T) {
	dir := t.TempDir()
	a, err := NewWithSessionDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), dir, nil)
	if err != nil {
		t.Fatalf("NewWithSessionDir() error = %v", err)
	}
	if a.session == nil {
		t.Fatal("session manager = nil, want non-nil")
	}
}

func TestNewWithSessionDirAcceptsDeepSeek(t *testing.T) {
	a, err := NewWithSessionDir(configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewWithSessionDir() error = %v", err)
	}
	if a.session == nil {
		t.Fatal("session manager = nil, want non-nil")
	}
}

func TestNewWithSessionIDPreservesStableIdentity(t *testing.T) {
	a, err := NewWithSessionIDInDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), "stable-session", nil)
	if err != nil {
		t.Fatalf("NewWithSessionIDInDir() error = %v", err)
	}
	if a.SessionID() != "stable-session" || a.Cwd() == "" {
		t.Fatalf("session = id %q cwd %q", a.SessionID(), a.Cwd())
	}
}

func TestNewWithSessionIDInjectsWorkingDirIntoTools(t *testing.T) {
	a, err := NewWithSessionIDInDir(configWithBuiltin(openai.ProviderID, openai.DefaultModel), t.TempDir(), "tool-cwd", nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, candidate := range a.tools {
		var workingDir string
		switch value := candidate.(type) {
		case *tool.Read:
			workingDir = value.WorkingDir
		case *tool.Ls:
			workingDir = value.WorkingDir
		case *tool.Grep:
			workingDir = value.WorkingDir
		case *tool.Find:
			workingDir = value.WorkingDir
		case *tool.Bash:
			workingDir = value.WorkingDir
		case *tool.Write:
			workingDir = value.WorkingDir
		case *tool.Edit:
			workingDir = value.WorkingDir
		}
		if workingDir != "" {
			seen++
			if workingDir != a.Cwd() {
				t.Fatalf("tool working dir = %q, want %q", workingDir, a.Cwd())
			}
		}
	}
	if seen != 7 {
		t.Fatalf("configured tool count = %d, want 7", seen)
	}
}
