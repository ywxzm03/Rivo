package tool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const shellMaxTimeoutMS = 2_147_483_647

func shellParameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Shell command to execute"},
			"timeout": map[string]any{"type": "number", "description": "Timeout in seconds (optional, no default timeout)"},
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
	command, timeout, err := parseShellArgs(args, toolName)
	if err != nil {
		return llm.ToolResult{}, err
	}

	commandCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
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
	cmd.Env = shellEnvironment()
	configureShellProcess(cmd)
	output := newShellOutputAccumulator()
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return llm.ToolResult{}, err
	}

	waitErr := cmd.Wait()
	if commandCtx.Err() != nil {
		killShellProcessTree(cmd)
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return shellErrorResult(output, fmt.Sprintf("Command timed out after %s seconds", formatShellTimeout(timeout.Seconds())))
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

func parseShellArgs(args map[string]any, toolName string) (string, time.Duration, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", 0, fmt.Errorf("%s: command must be a non-empty string", toolName)
	}
	v, exists := args["timeout"]
	if !exists {
		return command, 0, nil
	}
	seconds, ok := v.(float64)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return "", 0, fmt.Errorf("%s: invalid timeout: must be a finite number of seconds greater than 0", toolName)
	}
	if seconds*1000 > shellMaxTimeoutMS {
		return "", 0, fmt.Errorf("%s: invalid timeout: maximum is %s seconds", toolName, formatShellTimeout(float64(shellMaxTimeoutMS)/1000))
	}
	return command, time.Duration(seconds * float64(time.Second)), nil
}

func shellEnvironment() []string {
	env := append([]string(nil), os.Environ()...)
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return env
	}
	binDir := filepath.Join(cacheDir, "rivo", "bin")
	for i, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, "PATH") {
			env[i] = key + "=" + prependPath(binDir, value)
			return env
		}
	}
	return append(env, "PATH="+binDir)
}

func prependPath(entry, current string) string {
	for _, item := range strings.Split(current, string(os.PathListSeparator)) {
		if item == entry {
			return current
		}
	}
	if current == "" {
		return entry
	}
	return entry + string(os.PathListSeparator) + current
}

func formatShellTimeout(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}
