package tool

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
	toolargs "github.com/ywxzm03/Rivo/internal/tool/args"
)

const lsDefaultLimit = 500

type Ls struct{ WorkingDir string }

func (l *Ls) Name() string { return "ls" }

func (l *Ls) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (l *Ls) PromptSnippet() string { return "List directory contents" }

func (l *Ls) PromptGuidelines() []string { return nil }

func (l *Ls) Description() string {
	return fmt.Sprintf(
		"List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. "+
			"Includes dotfiles. Output is truncated to %d entries or %dKB (whichever is hit first).",
		lsDefaultLimit, defaultMaxBytes/1024,
	)
}

func (l *Ls) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to list (default: current directory)",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Maximum number of entries to return (default: 500)",
			},
		},
	}
}

// Execute 列出目录内容，目录带斜杠后缀，撞到条目数或字节上限时附上提示
// lsParams 是 ls 工具的参数形状
type lsParams struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

func (l *Ls) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	// 1.取参数，limit 非正数当作没给
	params, err := toolargs.Decode[lsParams](args)
	if err != nil {
		return llm.ToolResult{}, err
	}
	path := params.Path
	if path == "" {
		path = "."
	}
	limit := params.Limit
	if limit <= 0 {
		limit = lsDefaultLimit
	}

	// 2.解析路径并确认是目录
	absPath, err := resolvePathFrom(path, toolWorkingDir(ctx, l.WorkingDir))
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

	// 3.列目录并按名字排序
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return llm.ToolResult{}, fmt.Errorf("cannot read directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.SortStableFunc(names, func(a, b string) int {
		return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
	})

	// 4.逐条判断是否目录并加后缀
	lines := make([]string, 0, len(names))
	limitReached := false
	for _, name := range names {
		if len(lines) >= limit {
			limitReached = true
			break
		}
		entryInfo, err := os.Stat(filepath.Join(absPath, name))
		if err != nil {
			continue
		}
		suffix := ""
		if entryInfo.IsDir() {
			suffix = "/"
		}
		lines = append(lines, name+suffix)
	}
	if len(lines) == 0 {
		return llm.ToolResult{Content: textContents("(empty directory)")}, nil
	}

	// 5.只看字节上限
	truncation := truncateHead(strings.Join(lines, "\n"), math.MaxInt, defaultMaxBytes)
	output := truncation.Content

	byteLimited := truncation.Truncated
	entryLimited := limitReached && !byteLimited
	if byteLimited || entryLimited {
		reason := ""
		next := ""
		if byteLimited {
			reason = formatSize(defaultMaxBytes) + " limit"
		} else {
			next = fmt.Sprintf("limit=%d", limit*2)
		}
		output += truncationNote(
			fmt.Sprintf("Showing %d of %d entries", truncation.OutputLines, len(names)),
			reason, next)
	}
	return llm.ToolResult{Content: textContents(output)}, nil
}
