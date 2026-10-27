package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/shell"
)

func runBash(t *testing.T, ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	t.Helper()
	return (&Bash{}).Execute(ctx, args)
}

func bashText(result llm.ToolResult) string { return result.Content[0].(llm.TextContent).Text }

func TestBashExecutesAndCombinesOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	result, err := runBash(t, context.Background(), map[string]any{"command": "printf out; printf err >&2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashText(result); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("output = %q, want merged stdout and stderr", got)
	}
	if result.Details != nil {
		t.Errorf("Details = %#v, want nil", result.Details)
	}
}

func TestBashUsesConfiguredWorkingDir(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	if runtime.GOOS == "windows" {
		t.Skip("bash working directory test requires Unix shell")
	}
	workingDir := t.TempDir()
	result, err := (&Bash{WorkingDir: workingDir}).Execute(context.Background(), map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(bashText(result)); got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

func TestBashEmptyOutputAndNonzeroExit(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	result, err := runBash(t, context.Background(), map[string]any{"command": "true"})
	if err != nil || bashText(result) != "(no output)" {
		t.Errorf("empty result = %q, %v", bashText(result), err)
	}
	_, err = runBash(t, context.Background(), map[string]any{"command": "printf detail; exit 7"})
	if err == nil || !strings.Contains(err.Error(), "detail") || !strings.Contains(err.Error(), "Command exited with code 7") {
		t.Errorf("nonzero error = %v", err)
	}
}

func TestBashValidatesArgumentsAndTimeout(t *testing.T) {
	for _, args := range []map[string]any{
		{}, {"command": ""}, {"command": "true", "timeout": "1"}, {"command": "true", "timeout": float64(0)},
		{"command": "true", "timeout": float64(1e100)},
	} {
		if _, err := runBash(t, context.Background(), args); err == nil {
			t.Errorf("Execute(%#v) error = nil", args)
		}
	}
	_, err := runBash(t, context.Background(), map[string]any{"command": "sleep 2", "timeout": float64(0.05)})
	if err == nil || !strings.Contains(err.Error(), "Command timed out after 0.05 seconds") {
		t.Errorf("timeout error = %v", err)
	}
}

func TestBashContextCancellationKillsProcessGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err := runBash(t, ctx, map[string]any{"command": "sleep 30 & wait"})
	if err == nil || !strings.Contains(err.Error(), "Command aborted") {
		t.Errorf("cancel error = %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Errorf("cancel took %s, process group may still be alive", time.Since(started))
	}
}

func TestBashEnvironmentAndSanitization(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(cache, "rivo", "bin")
	t.Setenv("PATH", "/usr/bin")
	result, err := runBash(t, context.Background(), map[string]any{"command": "printf '\\033[31mred\\033[0m\\r\\nA\\001B'"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bashText(result), "red\nAB"; got != want {
		t.Errorf("sanitized output = %q, want %q", got, want)
	}
	if !strings.Contains(strings.Join(shell.Environment(), "\n"), binDir) {
		t.Errorf("bash environment does not include cache bin %q", binDir)
	}
}

func TestBashTailTruncationAndFullOutput(t *testing.T) {
	command := "awk 'BEGIN { for (i=1; i<=2100; i++) print i }'"
	result, err := runBash(t, context.Background(), map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	output := bashText(result)
	if !strings.HasPrefix(output, "101\n102\n") || !strings.Contains(output, "2100") || strings.Contains(output, "\n100\n") {
		t.Errorf("tail output has unexpected content")
	}
	details, ok := result.Details.(ShellDetails)
	if !ok {
		t.Fatalf("Details = %T, want ShellDetails", result.Details)
	}
	path := details.FullOutputPath
	if path == "" {
		t.Fatalf("fullOutputPath = %#v", path)
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(full), "1\n2\n") || !strings.HasSuffix(string(full), "2100\n") {
		t.Errorf("full output file contents are incorrect")
	}
	_ = os.Remove(path)
}

func TestBashByteTruncation(t *testing.T) {
	result, err := runBash(t, context.Background(), map[string]any{"command": "head -c 60000 /dev/zero | tr '\\0' x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bashText(result), "Showing last 50.0KB of line") {
		t.Errorf("output missing partial-line truncation note")
	}
	details := result.Details.(ShellDetails)
	if details.Truncation == nil || details.Truncation.TruncatedBy != truncatedByBytes {
		t.Errorf("truncatedBy = %#v, want bytes", details.Truncation)
	}
	_ = os.Remove(details.FullOutputPath)
}

func TestTruncateBashTailAllowsPartialFinalLine(t *testing.T) {
	input := strings.Repeat("x", defaultMaxBytes+100)
	result := truncateShellTail(input)
	if !result.Truncated || !result.LastLinePartial || result.OutputBytes != defaultMaxBytes {
		t.Errorf("truncation = %+v", result)
	}
}
