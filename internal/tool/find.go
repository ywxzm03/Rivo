package tool

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
	toolargs "github.com/ywxzm03/Rivo/internal/tool/args"
)

const findDefaultLimit = 1000

type Find struct{ WorkingDir string }

func (f *Find) Name() string { return "find" }

func (f *Find) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (f *Find) PromptSnippet() string { return "Find files by glob pattern (respects .gitignore)" }

func (f *Find) PromptGuidelines() []string { return nil }

func (f *Find) Description() string {
	return fmt.Sprintf(
		"Find files by glob pattern. Returns paths relative to the search directory. Includes hidden files and respects .gitignore. "+
			"Output is truncated to %d paths or %dKB (whichever is hit first).",
		findDefaultLimit, defaultMaxBytes/1024,
	)
}

func (f *Find) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"pattern"},
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern matched against basenames without '/' and relative paths with '/', e.g. '*.go' or 'src/**/*.spec.ts'",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to search (default: current directory)",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Maximum number of paths to return (default: 1000)",
			},
		},
	}
}

type findOptions struct {
	pattern    string
	searchPath string
	limit      int
}

type findResult struct {
	paths        []string
	limitReached bool
}

// Execute 使用 rg 按 glob 查找文件并返回相对搜索目录的路径
func (f *Find) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	// 1.读取参数并解析搜索目录
	options, err := parseFindArgs(args)
	if err != nil {
		return llm.ToolResult{}, err
	}
	absPath, err := resolvePathFrom(options.searchPath, toolWorkingDir(ctx, f.WorkingDir))
	if err != nil {
		return llm.ToolResult{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return llm.ToolResult{}, fmt.Errorf("path not found: %s", absPath)
		}
		return llm.ToolResult{}, err
	}
	if !info.IsDir() {
		return llm.ToolResult{}, fmt.Errorf("not a directory: %s", absPath)
	}

	// 2.查找 rg 并流式读取匹配路径
	rgPath, err := findRipgrep(ctx)
	if err != nil {
		return llm.ToolResult{}, err
	}
	result, err := runFindRipgrep(ctx, rgPath, absPath, options)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: formatFindOutput(result, options.limit)}, nil
}

// findParams 是 find 工具的参数形状
type findParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	// Limit 用指针区分「未提供」与「显式传 0」
	Limit *int `json:"limit"`
}

func parseFindArgs(values map[string]any) (findOptions, error) {
	params, err := toolargs.Decode[findParams](values)
	if err != nil {
		return findOptions{}, err
	}
	if params.Pattern == "" {
		return findOptions{}, fmt.Errorf("find: missing required parameter: pattern")
	}
	searchPath := params.Path
	if searchPath == "" {
		searchPath = "."
	}
	limit := findDefaultLimit
	if params.Limit != nil {
		limit = max(*params.Limit, 1)
	}
	return findOptions{pattern: params.Pattern, searchPath: searchPath, limit: limit}, nil
}

// buildFindRipgrepArgs 将 basename glob 转为 rg 的任意层级匹配
func buildFindRipgrepArgs(pattern string) []string {
	glob := strings.ReplaceAll(pattern, `\`, `/`)
	if !strings.Contains(glob, "/") {
		glob = "**/" + glob
	}
	return []string{"--files", "--hidden", "--color=never", "--path-separator=/", "--glob", glob, "--", "."}
}

func runFindRipgrep(ctx context.Context, rgPath, absPath string, options findOptions) (findResult, error) {
	result := findResult{paths: make([]string, 0)}
	_, err := runRipgrepStream(ctx, rgPath, absPath, buildFindRipgrepArgs(options.pattern), func(line []byte) bool {
		if len(result.paths) >= options.limit {
			result.limitReached = true
			return true
		}
		result.paths = append(result.paths, string(line))
		return false
	})
	if err != nil {
		return findResult{}, err
	}
	return result, nil
}

func formatFindOutput(result findResult, limit int) []llm.Content {
	if len(result.paths) == 0 {
		return textContents("No files found")
	}
	truncation := truncateHead(strings.Join(result.paths, "\n"), math.MaxInt, defaultMaxBytes)
	output := truncation.Content
	notices := make([]string, 0, 2)
	if result.limitReached {
		notices = append(notices, fmt.Sprintf("%d paths limit reached. Use limit=%d to see more", limit, limit*2))
	}
	if truncation.Truncated {
		notices = append(notices, formatSize(defaultMaxBytes)+" limit reached")
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return textContents(output)
}
