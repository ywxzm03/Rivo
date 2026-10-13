package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type Bash struct{}

func (b *Bash) Name() string { return "bash" }

func (b *Bash) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (b *Bash) PromptSnippet() string { return "Execute bash commands (ls, grep, find, etc.)" }

func (b *Bash) PromptGuidelines() []string { return nil }

func (b *Bash) Description() string {
	return fmt.Sprintf("Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to the last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.", defaultMaxLines, defaultMaxBytes/1024)
}

func (b *Bash) Parameters() map[string]any {
	return shellParameters()
}

// Execute 执行 bash 命令并返回清洗、截断后的合并输出
func (b *Bash) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	return executeShellCommand(ctx, args, "bash", func(commandCtx context.Context, command string) (*exec.Cmd, error) {
		bashPath, err := findBash()
		if err != nil {
			return nil, err
		}
		return exec.CommandContext(commandCtx, bashPath, "-c", command), nil
	})
}

var shellANSI = regexp.MustCompile(`(?:\x1b\[[0-?]*[ -/]*[@-~])|(?:\x1b\][^\x07]*(?:\x07|\x1b\\))|(?:\x1b[@-_])`)

type shellOutputAccumulator struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func newShellOutputAccumulator() *shellOutputAccumulator { return &shellOutputAccumulator{} }

func (o *shellOutputAccumulator) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.data.Write(data)
}

func (o *shellOutputAccumulator) result() (llm.ToolResult, error) {
	o.mu.Lock()
	data := append([]byte(nil), o.data.Bytes()...)
	o.mu.Unlock()
	content := sanitizeShellOutput(data)
	truncation := truncateShellTail(content)
	text := truncation.Content
	details := map[string]any(nil)
	if truncation.Truncated {
		fullOutputPath, err := writeShellFullOutput(content)
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
		text += shellTruncationNote(truncation, fullOutputPath)
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

func sanitizeShellOutput(data []byte) string {
	text := shellANSI.ReplaceAllString(string(data), "")
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

type shellTruncation struct {
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

func truncateShellTail(content string) shellTruncation {
	lines := splitLines(content)
	totalBytes := len([]byte(content))
	if len(lines) <= defaultMaxLines && totalBytes <= defaultMaxBytes {
		return shellTruncation{Content: content, TotalLines: len(lines), TotalBytes: totalBytes, OutputLines: len(lines), OutputBytes: totalBytes}
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
				line = truncateShellStringFromEnd(line, defaultMaxBytes)
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
	return shellTruncation{
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

func truncateShellStringFromEnd(text string, maxBytes int) string {
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

func writeShellFullOutput(content string) (string, error) {
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

func shellTruncationNote(truncation shellTruncation, fullOutputPath string) string {
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
