package tool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const findDefaultLimit = 1000

type Find struct{}

func (f *Find) Name() string { return "find" }

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
func (f *Find) Execute(ctx context.Context, args map[string]any) ([]llm.Content, error) {
	// 1.读取参数并解析搜索目录
	options, err := parseFindArgs(args)
	if err != nil {
		return nil, err
	}
	absPath, err := resolvePath(options.searchPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("Path not found: %s", absPath)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Not a directory: %s", absPath)
	}

	// 2.查找 rg 并流式读取匹配路径
	rgPath, err := findRipgrep(ctx)
	if err != nil {
		return nil, err
	}
	result, err := runFindRipgrep(ctx, rgPath, absPath, options)
	if err != nil {
		return nil, err
	}
	return formatFindOutput(result, options.limit), nil
}

// parseFindArgs 读取 find 参数并应用默认值
func parseFindArgs(args map[string]any) (findOptions, error) {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return findOptions{}, fmt.Errorf("find: missing required parameter: pattern")
	}
	searchPath, _ := args["path"].(string)
	if searchPath == "" {
		searchPath = "."
	}
	limit, hasLimit := argInt(args, "limit")
	if !hasLimit || limit < 0 {
		limit = findDefaultLimit
	}
	return findOptions{pattern: pattern, searchPath: searchPath, limit: limit}, nil
}

// buildFindRipgrepArgs 将 basename glob 转为 rg 的任意层级匹配
func buildFindRipgrepArgs(pattern string) []string {
	glob := strings.ReplaceAll(pattern, `\`, `/`)
	if !strings.Contains(glob, "/") {
		glob = "**/" + glob
	}
	return []string{"--files", "--hidden", "--color=never", "--path-separator=/", "--glob", glob, "--", "."}
}

// runFindRipgrep 执行 rg 并在发现超限结果后停止搜索
func runFindRipgrep(ctx context.Context, rgPath, absPath string, options findOptions) (findResult, error) {
	command := exec.CommandContext(ctx, rgPath, buildFindRipgrepArgs(options.pattern)...)
	command.Dir = absPath
	stdout, err := command.StdoutPipe()
	if err != nil {
		return findResult{}, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return findResult{}, err
	}

	result := findResult{paths: make([]string, 0)}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	stoppedDueToLimit := false
	for scanner.Scan() {
		path := scanner.Text()
		if len(result.paths) >= options.limit {
			result.limitReached = true
			stoppedDueToLimit = true
			_ = command.Process.Kill()
			break
		}
		result.paths = append(result.paths, path)
	}
	if err := scanner.Err(); err != nil {
		if !stoppedDueToLimit {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		return findResult{}, err
	}
	waitErr := command.Wait()
	if err := ctx.Err(); err != nil {
		return findResult{}, err
	}
	if err := ripgrepExitError(waitErr, stderr.String(), stoppedDueToLimit); err != nil {
		return findResult{}, err
	}
	return result, nil
}

// formatFindOutput 应用字节截断并生成模型可读的提示
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
