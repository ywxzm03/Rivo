package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const bashMaxTimeoutMS = 2_147_483_647

type Bash struct{}

func (b *Bash) Name() string { return "bash" }

func (b *Bash) Description() string {
	return fmt.Sprintf("Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to the last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.", defaultMaxLines, defaultMaxBytes/1024)
}

func (b *Bash) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Shell command to execute"},
			"timeout": map[string]any{"type": "number", "description": "Timeout in seconds (optional, no default timeout)"},
		},
	}
}

// Execute 执行 bash 命令并返回清洗、截断后的合并输出
func (b *Bash) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	// 1.读取并校验命令参数
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return llm.ToolResult{}, fmt.Errorf("bash: command must be a non-empty string")
	}
	timeout, err := bashTimeout(args)
	if err != nil {
		return llm.ToolResult{}, err
	}

	// 2.准备工作目录、环境与取消上下文
	cwd, err := os.Getwd()
	if err != nil {
		return llm.ToolResult{}, err
	}
	env := bashEnvironment()
	commandCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// 3.启动独立进程组并收集合并输出
	cmd := exec.CommandContext(commandCtx, "/bin/bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = env
	configureBashProcess(cmd)
	output := newBashOutputAccumulator()
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return llm.ToolResult{}, err
	}
	waitErr := cmd.Wait()
	if commandCtx.Err() != nil {
		killBashProcessGroup(cmd)
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return bashErrorResult(output, fmt.Sprintf("Command timed out after %s seconds", formatBashTimeout(timeout.Seconds())))
		}
		return bashErrorResult(output, "Command aborted")
	}

	// 4.生成最终输出，非零退出码作为工具错误
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return bashErrorResult(output, fmt.Sprintf("Command exited with code %d", exitErr.ExitCode()))
		}
		return llm.ToolResult{}, waitErr
	}
	return output.result()
}

func bashTimeout(args map[string]any) (time.Duration, error) {
	v, exists := args["timeout"]
	if !exists {
		return 0, nil
	}
	seconds, ok := v.(float64)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0, fmt.Errorf("bash: invalid timeout: must be a finite number of seconds greater than 0")
	}
	if seconds*1000 > bashMaxTimeoutMS {
		return 0, fmt.Errorf("bash: invalid timeout: maximum is %s seconds", formatBashTimeout(float64(bashMaxTimeoutMS)/1000))
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func bashEnvironment() []string {
	env := append([]string(nil), os.Environ()...)
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return env
	}
	binDir := filepath.Join(cacheDir, "rivo", "bin")
	pathKey, pathValue := "PATH", ""
	for i, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			pathValue = strings.TrimPrefix(item, "PATH=")
			env[i] = "PATH=" + prependPath(binDir, pathValue)
			return env
		}
	}
	env = append(env, pathKey+"="+binDir)
	return env
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

func bashErrorResult(output *bashOutputAccumulator, status string) (llm.ToolResult, error) {
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

func formatBashTimeout(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}

var bashANSI = regexp.MustCompile(`(?:\x1b\[[0-?]*[ -/]*[@-~])|(?:\x1b\][^\x07]*(?:\x07|\x1b\\))|(?:\x1b[@-_])`)

type bashOutputAccumulator struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func newBashOutputAccumulator() *bashOutputAccumulator { return &bashOutputAccumulator{} }

func (o *bashOutputAccumulator) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.data.Write(data)
}

func (o *bashOutputAccumulator) result() (llm.ToolResult, error) {
	o.mu.Lock()
	data := append([]byte(nil), o.data.Bytes()...)
	o.mu.Unlock()
	content := sanitizeBashOutput(data)
	truncation := truncateBashTail(content)
	text := truncation.Content
	details := map[string]any(nil)
	if truncation.Truncated {
		fullOutputPath, err := writeBashFullOutput(content)
		if err != nil {
			return llm.ToolResult{}, err
		}
		details = map[string]any{
			"truncation": map[string]any{
				"truncated":       true,
				"truncatedBy":     truncation.TruncatedBy,
				"totalLines":      truncation.TotalLines,
				"totalBytes":      truncation.TotalBytes,
				"outputLines":     truncation.OutputLines,
				"outputBytes":     truncation.OutputBytes,
				"lastLinePartial": truncation.LastLinePartial,
				"maxLines":        defaultMaxLines,
				"maxBytes":        defaultMaxBytes,
			},
			"fullOutputPath": fullOutputPath,
		}
		text += bashTruncationNote(truncation, fullOutputPath)
	}
	if text == "" {
		text = "(no output)"
	}
	result := llm.ToolResult{Content: textContents(text)}
	if details != nil {
		result.Details = details
	}
	return result, nil
}

func sanitizeBashOutput(data []byte) string {
	text := bashANSI.ReplaceAllString(string(data), "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var builder strings.Builder
	for _, r := range text {
		if r <= 0x1f && r != '\t' && r != '\n' {
			continue
		}
		if r >= 0xfff9 && r <= 0xfffb {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

type bashTruncation struct {
	Content         string
	Truncated       bool
	TruncatedBy     string
	TotalLines      int
	TotalBytes      int
	OutputLines     int
	OutputBytes     int
	LastLinePartial bool
	LastLineBytes   int
}

func truncateBashTail(content string) bashTruncation {
	lines := splitLines(content)
	totalBytes := len([]byte(content))
	if len(lines) <= defaultMaxLines && totalBytes <= defaultMaxBytes {
		return bashTruncation{Content: content, TotalLines: len(lines), TotalBytes: totalBytes, OutputLines: len(lines), OutputBytes: totalBytes}
	}

	kept := make([]string, 0, min(len(lines), defaultMaxLines))
	used := 0
	truncatedBy := truncatedByLines
	partial := false
	for i := len(lines) - 1; i >= 0 && len(kept) < defaultMaxLines; i-- {
		line := lines[i]
		lineSize := len([]byte(line))
		if len(kept) > 0 {
			lineSize++
		}
		if used+lineSize > defaultMaxBytes {
			truncatedBy = truncatedByBytes
			if len(kept) == 0 {
				line = truncateBashStringFromEnd(line, defaultMaxBytes)
				kept = append(kept, line)
				partial = true
			}
			break
		}
		kept = append(kept, line)
		used += lineSize
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	if len(kept) == defaultMaxLines && used <= defaultMaxBytes {
		truncatedBy = truncatedByLines
	}
	text := strings.Join(kept, "\n")
	return bashTruncation{
		Content:         text,
		Truncated:       true,
		TruncatedBy:     truncatedBy,
		TotalLines:      len(lines),
		TotalBytes:      totalBytes,
		OutputLines:     len(kept),
		OutputBytes:     len([]byte(text)),
		LastLinePartial: partial,
		LastLineBytes:   len([]byte(lines[len(lines)-1])),
	}
}

func truncateBashStringFromEnd(text string, maxBytes int) string {
	data := []byte(text)
	if len(data) <= maxBytes {
		return text
	}
	data = data[len(data)-maxBytes:]
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[1:]
	}
	return string(data)
}

func writeBashFullOutput(content string) (string, error) {
	file, err := os.CreateTemp("", "rivo-bash-*.log")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func bashTruncationNote(truncation bashTruncation, fullOutputPath string) string {
	endLine := truncation.TotalLines
	if truncation.LastLinePartial {
		lineSize := formatSize(truncation.LastLineBytes)
		return fmt.Sprintf("\n\n[Showing last %s of line %d (line is %s). Full output: %s]", formatSize(truncation.OutputBytes), endLine, lineSize, fullOutputPath)
	}
	startLine := truncation.TotalLines - truncation.OutputLines + 1
	note := fmt.Sprintf("\n\n[Showing lines %d-%d of %d", startLine, endLine, truncation.TotalLines)
	if truncation.TruncatedBy == truncatedByBytes {
		note += fmt.Sprintf(" (%s limit)", formatSize(defaultMaxBytes))
	}
	return note + fmt.Sprintf(". Full output: %s]", fullOutputPath)
}
