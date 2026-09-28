package tool

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const powershellUTF8Prefix = "try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}\n"

type PowerShell struct{}

func (p *PowerShell) Name() string { return "powershell" }

func (p *PowerShell) Description() string {
	return fmt.Sprintf("Execute a PowerShell command in the current working directory. Returns stdout and stderr. Output is truncated to the last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.", defaultMaxLines, defaultMaxBytes/1024)
}

func (p *PowerShell) Parameters() map[string]any { return shellParameters() }

// Execute 执行 PowerShell 命令并返回清洗、截断后的合并输出
func (p *PowerShell) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	if runtime.GOOS != "windows" {
		return llm.ToolResult{}, fmt.Errorf("powershell: only available on Windows")
	}
	executable, err := findPowerShell()
	if err != nil {
		return llm.ToolResult{}, err
	}
	return executeShellCommand(ctx, args, "powershell", func(commandCtx context.Context, command string) (*exec.Cmd, error) {
		return exec.CommandContext(
			commandCtx,
			executable,
			"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
			powershellUTF8Prefix+command,
		), nil
	})
}

// findPowerShell 按 pi 的顺序查找 PowerShell 7 和 Windows PowerShell
func findPowerShell() (string, error) {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("No PowerShell executable found. Install PowerShell or add powershell.exe/pwsh.exe to PATH.")
}
