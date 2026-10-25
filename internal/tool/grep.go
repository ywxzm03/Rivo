package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const (
	grepDefaultLimit = 100
	grepMaxLine      = 500
)

type Grep struct{ WorkingDir string }

func (g *Grep) Name() string { return "grep" }

func (g *Grep) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (g *Grep) PromptSnippet() string {
	return "Search file contents for patterns (respects .gitignore)"
}

func (g *Grep) PromptGuidelines() []string { return nil }

func (g *Grep) Description() string {
	return fmt.Sprintf(
		"Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. "+
			"Output is truncated to %d matches or %dKB (whichever is hit first). Long lines are truncated to %d chars.",
		grepDefaultLimit, defaultMaxBytes/1024, grepMaxLine,
	)
}

func (g *Grep) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"pattern"},
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Search pattern (regex or literal string)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory or file to search (default: current directory)",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'",
			},
			"ignoreCase": map[string]any{
				"type":        "boolean",
				"description": "Case-insensitive search (default: false)",
			},
			"literal": map[string]any{
				"type":        "boolean",
				"description": "Treat pattern as literal string instead of regex (default: false)",
			},
			"context": map[string]any{
				"type":        "number",
				"description": "Number of lines to show before and after each match (default: 0)",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Maximum number of matches to return (default: 100)",
			},
		},
	}
}

type ripgrepEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

type grepOptions struct {
	pattern      string
	searchPath   string
	glob         string
	ignoreCase   bool
	literal      bool
	contextLines int
	limit        int
}

type grepResult struct {
	matches           []grepMatch
	matchCount        int
	matchLimitReached bool
}

type grepMatch struct {
	filePath   string
	lineNumber int
	lineText   string
}

// Execute 使用 rg 搜索文件内容并返回匹配行与上下文
func (g *Grep) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	// 1.获取参数
	options, err := parseGrepArgs(args)
	if err != nil {
		return llm.ToolResult{}, err
	}
	// 2.转化为绝对路径
	absPath, isDirectory, err := resolveGrepPath(options.searchPath, g.WorkingDir)
	if err != nil {
		return llm.ToolResult{}, err
	}
	// 3.定位 rg，缺失时从 GitHub Releases 下载
	rgPath, err := findRipgrep(ctx)
	if err != nil {
		return llm.ToolResult{}, err
	}
	result, err := runRipgrep(ctx, rgPath, buildRipgrepArgs(options, absPath), absPath, isDirectory, options)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: formatGrepOutput(result, options.limit, options.contextLines, absPath, isDirectory)}, nil
}

func parseGrepArgs(args map[string]any) (grepOptions, error) {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return grepOptions{}, fmt.Errorf("grep: missing required parameter: pattern")
	}
	searchPath, _ := args["path"].(string)
	if searchPath == "" {
		searchPath = "."
	}
	contextLines, _ := argInt(args, "context")
	if contextLines < 0 {
		contextLines = 0
	}
	limit, hasLimit := argInt(args, "limit")
	if !hasLimit {
		limit = grepDefaultLimit
	} else if limit < 1 {
		limit = 1
	}
	glob, _ := args["glob"].(string)
	return grepOptions{
		pattern:      pattern,
		searchPath:   searchPath,
		glob:         glob,
		ignoreCase:   boolArg(args, "ignoreCase"),
		literal:      boolArg(args, "literal"),
		contextLines: contextLines,
		limit:        limit,
	}, nil
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func resolveGrepPath(searchPath, workingDir string) (string, bool, error) {
	absPath, err := resolvePathFrom(searchPath, workingDir)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, fmt.Errorf("Path not found: %s", absPath)
		}
		return "", false, err
	}
	return absPath, info.IsDir(), nil
}

func buildRipgrepArgs(options grepOptions, absPath string) []string {
	args := []string{"--json", "--line-number", "--color=never", "--hidden"}
	if options.ignoreCase {
		args = append(args, "--ignore-case")
	}
	if options.literal {
		args = append(args, "--fixed-strings")
	}
	if options.glob != "" {
		args = append(args, "--glob", options.glob)
	}
	return append(args, "--", options.pattern, absPath)
}

// runRipgrep 执行 rg，流式解析匹配和上下文事件
func runRipgrep(ctx context.Context, rgPath string, args []string, absPath string, isDirectory bool, options grepOptions) (grepResult, error) {
	// 1.执行 rg 命令
	command := exec.CommandContext(ctx, rgPath, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return grepResult{}, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	configureShellProcess(command)
	if err := command.Start(); err != nil {
		return grepResult{}, err
	}

	result := grepResult{matches: make([]grepMatch, 0)}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	killedDueToLimit := false
	// 2.逐条解析 match 事件，达到匹配上限后停止搜索
	for scanner.Scan() {
		if killedDueToLimit {
			break
		}
		var event ripgrepEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type != "match" {
			continue
		}
		path := event.Data.Path.Text
		lineNumber := event.Data.LineNumber
		if path == "" || lineNumber <= 0 {
			continue
		}
		result.matches = append(result.matches, grepMatch{
			filePath:   path,
			lineNumber: lineNumber,
			lineText:   event.Data.Lines.Text,
		})
		result.matchCount++
		if result.matchCount >= options.limit {
			result.matchLimitReached = true
			killedDueToLimit = true
			killShellProcessTree(command)
			break
		}
	}
	// 3.等待进程结束，并区分主动终止、无匹配与执行错误
	if err := scanner.Err(); err != nil {
		if !killedDueToLimit {
			killShellProcessTree(command)
		}
		_ = command.Wait()
		return grepResult{}, err
	}
	waitErr := command.Wait()
	if err := ctx.Err(); err != nil {
		return grepResult{}, err
	}
	if err := ripgrepExitError(waitErr, stderr.String(), killedDueToLimit); err != nil {
		return grepResult{}, err
	}
	return result, nil
}

func grepPathFormatter(absPath string, isDirectory bool) func(string) string {
	return func(path string) string {
		if isDirectory {
			relative, err := filepath.Rel(absPath, path)
			if err == nil && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != ".." {
				return filepath.ToSlash(relative)
			}
		}
		return filepath.Base(path)
	}
}

// ripgrepExitError 把 rg 的退出状态转换为工具错误
func ripgrepExitError(waitErr error, stderr string, killedDueToLimit bool) error {
	if killedDueToLimit || waitErr == nil {
		return nil
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) && exitError.ExitCode() == 1 {
		return nil
	}
	message := strings.TrimSpace(stderr)
	if message == "" {
		if exitError != nil {
			message = fmt.Sprintf("ripgrep exited with code %d", exitError.ExitCode())
		} else {
			message = waitErr.Error()
		}
	}
	return errors.New(message)
}

func formatGrepOutput(result grepResult, limit, contextLines int, absPath string, isDirectory bool) []llm.Content {
	if result.matchCount == 0 {
		return textContents("No matches found")
	}
	formatPath := grepPathFormatter(absPath, isDirectory)
	outputLines := make([]string, 0)
	linesTruncated := false
	fileCache := make(map[string][]string)
	for _, match := range result.matches {
		if contextLines == 0 {
			line, truncated := truncateLine(strings.TrimRight(match.lineText, "\r\n"), grepMaxLine)
			if truncated {
				linesTruncated = true
			}
			outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", formatPath(match.filePath), match.lineNumber, line))
			continue
		}
		lines, ok := fileCache[match.filePath]
		if !ok {
			var err error
			lines, err = readGrepFileLines(match.filePath)
			if err != nil {
				fileCache[match.filePath] = nil
				outputLines = append(outputLines, fmt.Sprintf("%s:%d: (unable to read file)", formatPath(match.filePath), match.lineNumber))
				continue
			}
			fileCache[match.filePath] = lines
		}
		if len(lines) == 0 {
			outputLines = append(outputLines, fmt.Sprintf("%s:%d: (unable to read file)", formatPath(match.filePath), match.lineNumber))
			continue
		}
		start := max(1, match.lineNumber-contextLines)
		end := min(len(lines), match.lineNumber+contextLines)
		for lineNumber := start; lineNumber <= end; lineNumber++ {
			line, truncated := truncateLine(lines[lineNumber-1], grepMaxLine)
			if truncated {
				linesTruncated = true
			}
			separator := "-"
			if lineNumber == match.lineNumber {
				separator = ":"
			}
			outputLines = append(outputLines, fmt.Sprintf("%s%s%d%s %s", formatPath(match.filePath), separator, lineNumber, separator, line))
		}
	}
	truncation := truncateHead(strings.Join(outputLines, "\n"), math.MaxInt, defaultMaxBytes)
	output := truncation.Content
	notices := make([]string, 0, 3)
	if result.matchLimitReached {
		notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern", limit, limit*2))
	}
	if truncation.Truncated {
		notices = append(notices, formatSize(defaultMaxBytes)+" limit reached")
	}
	if linesTruncated {
		notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", grepMaxLine))
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return textContents(output)
}

func readGrepFileLines(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	content = bytes.ReplaceAll(content, []byte("\r"), []byte("\n"))
	return strings.Split(string(content), "\n"), nil
}
