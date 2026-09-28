//go:build windows

package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func runPowerShell(t *testing.T, ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	t.Helper()
	return (&PowerShell{}).Execute(ctx, args)
}

func powerShellText(result llm.ToolResult) string {
	return result.Content[0].(llm.TextContent).Text
}

func TestPowerShellExecutesUTF8Output(t *testing.T) {
	result, err := runPowerShell(t, context.Background(), map[string]any{
		"command": "[Console]::Write('你好，PowerShell')",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := powerShellText(result); got != "你好，PowerShell" {
		t.Errorf("output = %q, want UTF-8 PowerShell output", got)
	}
}

func TestPowerShellFindsPwshBeforeWindowsPowerShell(t *testing.T) {
	dir := t.TempDir()
	pwsh := filepath.Join(dir, "pwsh.exe")
	powershell := filepath.Join(dir, "powershell.exe")
	for _, path := range []string{pwsh, powershell} {
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	got, err := findPowerShell()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got, pwsh) {
		t.Errorf("findPowerShell() = %q, want %q", got, pwsh)
	}
}

func TestPowerShellFallsBackToWindowsPowerShell(t *testing.T) {
	dir := t.TempDir()
	powershell := filepath.Join(dir, "powershell.exe")
	if err := os.WriteFile(powershell, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := findPowerShell()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got, powershell) {
		t.Errorf("findPowerShell() = %q, want %q", got, powershell)
	}
}

func TestPowerShellTimeout(t *testing.T) {
	_, err := runPowerShell(t, context.Background(), map[string]any{
		"command": "Start-Sleep -Seconds 2",
		"timeout": float64(0.05),
	})
	if err == nil || !strings.Contains(err.Error(), "Command timed out after 0.05 seconds") {
		t.Errorf("timeout error = %v", err)
	}
}

func TestPowerShellCancellationKillsProcessTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err := runPowerShell(t, ctx, map[string]any{
		"command": "$shell = (Get-Process -Id $PID).Path; Start-Process -FilePath $shell -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 30' | Out-Null; Start-Sleep -Seconds 30",
	})
	if err == nil || !strings.Contains(err.Error(), "Command aborted") {
		t.Errorf("cancel error = %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("cancellation took %s, process tree may still be alive", time.Since(started))
	}
}
