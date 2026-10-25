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

type Bash struct {
	Background BackgroundRunner
}

// guardShellCommand 拦截灾难性命令。explore/plan 的只读性由工具池与 prompt 保证,
// 这里只挡住模型无论如何都不该执行的操作
func guardShellCommand(command string) error {
	// 1.控制字符会被 bash 静默丢弃,让校验与实际执行产生分歧
	if controlCharPattern.MatchString(command) {
		return fmt.Errorf("bash: command contains non-printable control characters")
	}
	// 2.逐段检查,避免 `ls; rm -rf /` 这类复合命令绕过
	for _, segment := range splitShellSegments(command) {
		if reason := dangerousShellSegment(segment); reason != "" {
			return fmt.Errorf("bash: %s", reason)
		}
	}
	return nil
}

// controlCharPattern 匹配 bash 会静默丢弃的不可打印字符,保留 \t \n \r
var controlCharPattern = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)

// splitShellSegments 按 shell 运算符切分命令
func splitShellSegments(command string) []string {
	return strings.FieldsFunc(command, func(r rune) bool {
		return r == ';' || r == '|' || r == '&' || r == '\n' || r == '(' || r == ')'
	})
}

// dangerousShellSegment 返回该片段被拒绝的原因,空字符串表示放行
func dangerousShellSegment(segment string) string {
	fields := strings.Fields(strings.TrimSpace(segment))
	if len(fields) == 0 {
		return ""
	}
	// 3.zsh 模块内建命令在 bash 下不存在,拦掉可避免解析器分歧
	if zshModuleCommands[fields[0]] {
		return fmt.Sprintf("zsh module command %q is not allowed", fields[0])
	}
	// 4.递归删除根目录或家目录
	if fields[0] == "rm" && hasRecursiveForceFlag(fields[1:]) && targetsCriticalPath(fields[1:]) {
		return "refusing to recursively delete a critical path"
	}
	// 5.写块设备会不可逆地破坏磁盘
	if fields[0] == "dd" && writesBlockDevice(fields[1:]) {
		return "refusing to write directly to a block device"
	}
	return ""
}

var zshModuleCommands = map[string]bool{
	"zmodload": true, "emulate": true, "sysopen": true, "sysread": true,
	"syswrite": true, "sysseek": true, "zpty": true, "ztcp": true,
	"zsocket": true, "zf_rm": true, "zf_mv": true, "zf_ln": true,
	"zf_chmod": true, "zf_chown": true, "zf_mkdir": true, "zf_rmdir": true,
	"zf_chgrp": true,
}

// hasRecursiveForceFlag 判断 rm 是否同时带递归与强制
func hasRecursiveForceFlag(args []string) bool {
	recursive, force := false, false
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.ContainsAny(arg, "rR") {
			recursive = true
		}
		if strings.Contains(arg, "f") {
			force = true
		}
	}
	return recursive && force
}

// targetsCriticalPath 判断 rm 的目标是否包含根目录或家目录本身
func targetsCriticalPath(args []string) bool {
	home, _ := os.UserHomeDir()
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		cleaned := strings.TrimRight(arg, "/")
		if cleaned == "" || cleaned == "/" || cleaned == "." || cleaned == ".." ||
			cleaned == "/*" || cleaned == "~" || (home != "" && cleaned == home) {
			return true
		}
	}
	return false
}

// writesBlockDevice 判断 dd 的 of= 是否指向块设备
func writesBlockDevice(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "of=") {
			continue
		}
		target := strings.TrimPrefix(arg, "of=")
		if strings.HasPrefix(target, "/dev/") && !strings.HasPrefix(target, "/dev/null") {
			return true
		}
	}
	return false
}

type BackgroundRunner interface {
	RunBackground(context.Context, map[string]any) (llm.ToolResult, error)
}

func (b *Bash) Name() string { return "bash" }

func (b *Bash) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (b *Bash) PromptSnippet() string { return "Execute bash commands" }

// PromptGuidelines 引导模型优先使用内置工具,并把 bash 限定在工具做不到的场合
func (b *Bash) PromptGuidelines() []string {
	return []string{
		// 1.工具偏好:内置工具在审查与授权上体验更好
		"Prefer the find tool for file pattern matching, and the ls tool for listing a directory; use bash `find` or `ls` only when a dedicated tool cannot accomplish the task",
		"Prefer the grep tool for searching file contents; use bash `grep` or `rg` only when a dedicated tool cannot accomplish the task",
		"Prefer the edit tool for editing files; use bash `sed` or `awk` only when a dedicated tool cannot accomplish the task",
		"Prefer the write tool for creating files; use bash redirection or heredocs only when a dedicated tool cannot accomplish the task",
		"Output text directly to communicate; do not use bash `echo` or `printf` for messages to the user",
		// 2.多命令组织
		"When several independent commands can run in parallel, issue multiple bash tool calls in a single message instead of chaining them",
		"When commands depend on each other, chain them with `&&` in one bash call",
		"Use `;` only when you need to run commands sequentially but do not care whether earlier ones fail",
		"Do not use newlines to separate commands; newlines are fine inside quoted strings",
		// 3.通用约定
		"Before creating files or directories, use `ls` to verify the parent directory exists and is the correct location",
		"Always quote file paths that contain spaces with double quotes",
		"Maintain the current working directory by using absolute paths; only use `cd` when the user explicitly asks for it",
		// 4.git 约定
		"Prefer creating a new commit over amending an existing one",
		"Before destructive git operations such as `git reset --hard`, `git push --force`, or `git checkout --`, consider whether a safer alternative reaches the same goal; only use them when they are truly the best approach",
		"Never skip hooks with `--no-verify` or bypass signing unless the user has explicitly asked for it; if a hook fails, investigate and fix the underlying issue",
		// 5.sleep 约定
		"Do not sleep between commands that can run immediately; just run them",
		"For long-running commands use `run_in_background` instead of sleeping; you are notified when the task completes, so do not poll",
		"Do not retry failing commands in a sleep loop; diagnose the root cause instead",
		"If you must poll an external process, use a check command rather than sleeping first, and keep any sleep short",
	}
}

func (b *Bash) Description() string {
	return fmt.Sprintf("Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to the last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds or run_in_background=true to start a BashTask and return its task ID.", defaultMaxLines, defaultMaxBytes/1024)
}

func (b *Bash) Parameters() map[string]any {
	return bashParameters()
}

func (b *Bash) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	command, _ := args["command"].(string)
	// 主 agent 与子 agent 共用同一层灾难性命令拦截
	if err := guardShellCommand(command); err != nil {
		return llm.ToolResult{}, err
	}
	if background, ok := args["run_in_background"].(bool); ok && background {
		if b.Background == nil {
			return llm.ToolResult{}, fmt.Errorf("bash: background execution is unavailable")
		}
		return b.Background.RunBackground(ctx, args)
	}
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
