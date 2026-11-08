package tool

import (
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
	toolargs "github.com/ywxzm03/Rivo/internal/tool/args"
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
	absPath, isDirectory, err := resolveGrepPath(options.searchPath, toolWorkingDir(ctx, g.WorkingDir))
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

// grepParams 是 grep 工具的参数形状
type grepParams struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignoreCase"`
	Literal    bool   `json:"literal"`
	Context    int    `json:"context"`
	// Limit 用指针区分「未提供」与「显式传 0」
	Limit *int `json:"limit"`
}

func parseGrepArgs(values map[string]any) (grepOptions, error) {
	params, err := toolargs.Decode[grepParams](values)
	if err != nil {
		return grepOptions{}, err
	}
	if params.Pattern == "" {
		return grepOptions{}, fmt.Errorf("grep: missing required parameter: pattern")
	}
	searchPath := params.Path
	if searchPath == "" {
		searchPath = "."
	}
	contextLines := max(params.Context, 0)
	limit := grepDefaultLimit
	if params.Limit != nil {
		limit = max(*params.Limit, 1)
	}
	return grepOptions{
		pattern:      params.Pattern,
		searchPath:   searchPath,
		glob:         params.Glob,
		ignoreCase:   params.IgnoreCase,
		literal:      params.Literal,
		contextLines: contextLines,
		limit:        limit,
	}, nil
}

func resolveGrepPath(searchPath, workingDir string) (string, bool, error) {
	absPath, err := resolvePathFrom(searchPath, workingDir)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, fmt.Errorf("path not found: %s", absPath)
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
	result := grepResult{matches: make([]grepMatch, 0)}
	stopped, err := runRipgrepStream(ctx, rgPath, "", args, func(line []byte) bool {
		var event ripgrepEvent
		if err := json.Unmarshal(line, &event); err != nil || event.Type != "match" {
			return false
		}
		path := event.Data.Path.Text
		lineNumber := event.Data.LineNumber
		if path == "" || lineNumber <= 0 {
			return false
		}
		result.matches = append(result.matches, grepMatch{
			filePath:   path,
			lineNumber: lineNumber,
			lineText:   event.Data.Lines.Text,
		})
		result.matchCount++
		if result.matchCount >= options.limit {
			result.matchLimitReached = true
			return true
		}
		return false
	})
	if err != nil {
		return grepResult{}, err
	}
	_ = stopped
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
