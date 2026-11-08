package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/shell"
)

func shellParameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]any{
			"command":           map[string]any{"type": "string", "description": "Shell command to execute"},
			"timeout":           map[string]any{"type": "number", "description": "Timeout in seconds (optional, no default timeout)"},
			"run_in_background": map[string]any{"type": "boolean", "description": "Run the command as a background BashTask and return its task ID"},
		},
	}
}

// executeShellCommand 执行 shell 命令并统一处理取消、超时、退出码和输出
func executeShellCommand(
	ctx context.Context,
	args map[string]any,
	toolName string,
	workingDir string,
	buildCommand func(context.Context, string) (*exec.Cmd, error),
) (llm.ToolResult, error) {
	command, timeout, err := shell.ParseArgs(args, toolName)
	if err != nil {
		return llm.ToolResult{}, err
	}

	commandCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	workingDir = toolWorkingDir(ctx, workingDir)
	if workingDir == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			return llm.ToolResult{}, err
		}
	}
	cmd, err := buildCommand(commandCtx, command)
	if err != nil {
		return llm.ToolResult{}, err
	}
	cmd.Dir = workingDir
	cmd.Env = shell.Environment()
	shell.ConfigureProcess(cmd)
	output := newShellOutputAccumulator()
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return llm.ToolResult{}, err
	}

	waitErr := cmd.Wait()
	if commandCtx.Err() != nil {
		shell.KillProcessTree(cmd)
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return shellErrorResult(output, fmt.Sprintf("Command timed out after %s seconds", shell.FormatTimeout(timeout.Seconds())))
		}
		return shellErrorResult(output, "Command aborted")
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return shellErrorResult(output, fmt.Sprintf("Command exited with code %d", exitErr.ExitCode()))
		}
		return llm.ToolResult{}, waitErr
	}
	return output.result()
}

func shellErrorResult(output *shellOutputAccumulator, status string) (llm.ToolResult, error) {
	result, err := output.result()
	if err != nil {
		return llm.ToolResult{}, err
	}
	text := result.Content[0].(llm.TextContent).Text
	if text == "(no output)" {
		return llm.ToolResult{}, errors.New(status)
	}
	return llm.ToolResult{}, errors.New(text + "\n\n" + status)
}
