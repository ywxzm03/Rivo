package tool

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func textContents(text string) []llm.Content {
	return []llm.Content{llm.TextContent{Text: text}}
}

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

// resolvePath 把模型给的路径解析成绝对路径，支持 ~ 展开与相对 cwd
func resolvePath(path string) (string, error) {
	return resolvePathFrom(path, "")
}

func resolvePathFrom(path, cwd string) (string, error) {
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
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		path = filepath.Join(cwd, path)
	}
	return path, nil
}

func truncationNote(shown, reason, next string, extra ...string) string {
	note := shown
	if reason != "" {
		note += " (" + reason + ")"
	}
	note += "."
	if next != "" {
		note += " Use " + next + " to continue."
	}
	for _, item := range extra {
		if item == "" {
			continue
		}
		note += " " + item
		if !strings.HasSuffix(item, ".") {
			note += "."
		}
	}
	return "\n\n[" + note + "]"
}

func truncateLine(line string, maxChars int) (string, bool) {
	runes := []rune(line)
	if len(runes) <= maxChars {
		return line, false
	}
	return string(runes[:maxChars]) + "... [truncated]", true
}

const (
	defaultMaxLines = 2000
	defaultMaxBytes = 50 * 1024
)

const (
	truncatedByLines = "lines"
	truncatedByBytes = "bytes"
)

type truncationResult struct {
	Content               string
	Truncated             bool
	TruncatedBy           string
	TotalLines            int
	OutputLines           int
	FirstLineExceedsLimit bool
}

// lineBytes 是第 i 行在内容里占的字节数，除首行外每行多算一个换行符
func lineBytes(line string, i int) int {
	if i == 0 {
		return len(line)
	}
	return len(line) + 1
}

func formatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return strconv.Itoa(bytes) + "B"
	case bytes < 1024*1024:
		return strconv.FormatFloat(float64(bytes)/1024, 'f', 1, 64) + "KB"
	default:
		return strconv.FormatFloat(float64(bytes)/(1024*1024), 'f', 1, 64) + "MB"
	}
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// truncateHead 从头保留内容，行数与字节数两个上限先到先截，永不返回半行
func truncateHead(content string, maxLines, maxBytes int) truncationResult {
	lines := splitLines(content)
	totalLines := len(lines)

	totalBytes := 0
	for i, line := range lines {
		totalBytes += lineBytes(line, i)
	}
	if totalLines <= maxLines && totalBytes <= maxBytes {
		return truncationResult{Content: content, TotalLines: totalLines, OutputLines: totalLines}
	}

	// 1.能走到这里 content 必非空，所以 lines 非空
	if len(lines[0]) > maxBytes {
		return truncationResult{
			Truncated:             true,
			TruncatedBy:           truncatedByBytes,
			TotalLines:            totalLines,
			FirstLineExceedsLimit: true,
		}
	}

	// 2.逐行收，超过任一上限就停
	kept := make([]string, 0, min(totalLines, maxLines))
	used := 0
	truncatedBy := truncatedByLines
	for i, line := range lines {
		if i >= maxLines {
			break
		}
		n := lineBytes(line, i)
		if used+n > maxBytes {
			truncatedBy = truncatedByBytes
			break
		}
		kept = append(kept, line)
		used += n
	}
	// 行数先到上限且字节没超，才算被行数截断
	if len(kept) >= maxLines && used <= maxBytes {
		truncatedBy = truncatedByLines
	}

	return truncationResult{
		Content:     strings.Join(kept, "\n"),
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines,
		OutputLines: len(kept),
	}
}
