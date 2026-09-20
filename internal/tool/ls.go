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
)

const lsDefaultLimit = 500

type Ls struct{}

func (l *Ls) Name() string { return "ls" }

func (l *Ls) Description() string {
	return fmt.Sprintf(
		"List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. "+
			"Includes dotfiles. Output is truncated to %d entries or %dKB (whichever is hit first).",
		lsDefaultLimit, defaultMaxBytes/1024,
	)
}

// Parameters 每次新建，避免共享的 map 被下游改写
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

// Execute 列出目录内容，一条一行、目录带斜杠后缀，撞到条目数或字节上限时附上提示
func (l *Ls) Execute(ctx context.Context, args map[string]any) ([]llm.Content, error) {
	// 1.取参数：path 缺省列当前目录，limit 非正数当作没给
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	limit, _ := argInt(args, "limit")
	if limit <= 0 {
		limit = lsDefaultLimit
	}

	// 2.解析路径并确认是目录
	absPath, err := resolvePath(path)
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

	// 3.列目录并按名字排序，大小写不敏感
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("Cannot read directory: %w", err)
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
		return textContents("(empty directory)"), nil
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
	return textContents(output), nil
}
