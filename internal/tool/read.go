package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Read 读取文本文件，可按行号区间读取并按行数、字节数上限截断
type Read struct{}

func (r *Read) Name() string { return "read" }

func (r *Read) Description() string {
	return fmt.Sprintf(
		"Read the contents of a file. For text files, output is truncated to %d lines or %dKB (whichever is hit first). "+
			"Use offset/limit for large files. When you need the full file, continue with offset until complete.",
		defaultMaxLines, defaultMaxBytes/1024,
	)
}

// Parameters 每次新建，避免共享的 map 被下游改写
func (r *Read) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read (relative or absolute)",
			},
			"offset": map[string]any{
				"type":        "number",
				"description": "Line number to start reading from (1-indexed)",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Maximum number of lines to read",
			},
		},
	}
}

// Execute 读取文件，按行数、字节数上限截断并附上续读提示
func (r *Read) Execute(_ context.Context, args map[string]any) (string, error) {
	// 1.取参数，JSON 解出来的数字都是 float64
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("read: missing required parameter: path")
	}
	offset, _ := argInt(args, "offset")
	limit, hasLimit := argInt(args, "limit")

	// limit 得是正数才有意义，0 与负数当作没给
	if limit <= 0 {
		hasLimit = false
	}

	// 2.解析路径后整读，读不到就把错误原样透传给模型
	absPath, err := resolvePath(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}

	// 3.切行；结尾换行切出的空串不算一行，但切片仍要走原数组才能保住结尾换行
	allLines := strings.Split(string(data), "\n")
	totalFileLines := len(allLines)
	if allLines[totalFileLines-1] == "" {
		totalFileLines--
	}

	startLine := 0
	if offset > 0 {
		startLine = offset - 1
	}
	startLineDisplay := startLine + 1
	if startLine >= totalFileLines {
		return "", fmt.Errorf("Offset %d is beyond end of file (%d lines total)", offset, totalFileLines)
	}

	// 4.limit 只切出候选区间，真正的截断交给 truncateHead
	var selected string
	userLimitedLines := 0
	if hasLimit {
		userLimitedLines = min(limit, totalFileLines-startLine)
		selected = strings.Join(allLines[startLine:startLine+userLimitedLines], "\n")
	} else {
		selected = strings.Join(allLines[startLine:], "\n")
	}

	// 5.按截断结果拼续读提示，判定顺序不能换
	truncation := truncateHead(selected, defaultMaxLines, defaultMaxBytes)
	switch {
	case truncation.FirstLineExceedsLimit:
		return fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use offset=%d to continue.]",
			startLineDisplay, formatSize(len(allLines[startLine])), formatSize(defaultMaxBytes),
			startLineDisplay+1), nil
	case truncation.Truncated || (hasLimit && startLine+userLimitedLines < totalFileLines):
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		limitNote := ""
		if truncation.TruncatedBy == truncatedByBytes {
			limitNote = fmt.Sprintf(" (%s limit)", formatSize(defaultMaxBytes))
		}
		return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d%s. Use offset=%d to continue.]",
			truncation.Content, startLineDisplay, endLineDisplay, totalFileLines, limitNote, endLineDisplay+1), nil
	default:
		return truncation.Content, nil
	}
}

// argInt 读取可选的整数参数，JSON 里的数字解出来是 float64
func argInt(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// resolvePath 把模型给的路径解析成绝对路径，支持 ~ 展开与相对当前工作目录
func resolvePath(path string) (string, error) {
	// 1.~ 与 ~/ 展开成用户主目录
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	// 2.相对路径按当前工作目录解析
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	return path, nil
}
