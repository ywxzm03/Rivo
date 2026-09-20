package tool

import (
	"strconv"
	"strings"
)

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

// formatSize 把字节数格式化成可读大小
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

// splitLines 按换行切分，结尾换行带来的空串不算一行
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
// 首行单独就超过字节上限时返回空内容并置 FirstLineExceedsLimit
func truncateHead(content string, maxLines, maxBytes int) truncationResult {
	lines := splitLines(content)
	totalLines := len(lines)

	// 1.两个上限都没触到就原样返回，字节数用与下面循环相同的计法
	totalBytes := 0
	for i, line := range lines {
		totalBytes += lineBytes(line, i)
	}
	if totalLines <= maxLines && totalBytes <= maxBytes {
		return truncationResult{Content: content, TotalLines: totalLines, OutputLines: totalLines}
	}

	// 2.能走到这里 content 必非空，所以 lines 非空
	if len(lines[0]) > maxBytes {
		return truncationResult{
			Truncated:             true,
			TruncatedBy:           truncatedByBytes,
			TotalLines:            totalLines,
			FirstLineExceedsLimit: true,
		}
	}

	// 3.逐行收，超过任一上限就停
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
