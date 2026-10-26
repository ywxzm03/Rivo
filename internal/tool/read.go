package tool

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ywxzm03/Rivo/internal/imageutil"
	"github.com/ywxzm03/Rivo/internal/llm"
)

const noVisionNote = "[Current model does not support images. The image will be omitted from this request.]"

type Read struct {
	SupportsImages bool
	WorkingDir     string
}

func (r *Read) Name() string { return "read" }

func (r *Read) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (r *Read) PromptSnippet() string { return "Read file contents" }

func (r *Read) PromptGuidelines() []string {
	return []string{"Use read to examine files instead of cat or sed."}
}

func (r *Read) Description() string {
	return fmt.Sprintf(
		"Read the contents of a file. Supports text files and images (jpg, png, webp). Images are sent as attachments. "+
			"For text files, output is truncated to %d lines or %dKB (whichever is hit first). "+
			"Use offset/limit for large files. When you need the full file, continue with offset until complete.",
		defaultMaxLines, defaultMaxBytes/1024,
	)
}

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

// Execute 读取文件：图片返回文字说明加图片附件，文本按行截断并附上续读提示
func (r *Read) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return llm.ToolResult{}, fmt.Errorf("read: missing required parameter: path")
	}
	offset, _ := argInt(args, "offset")
	limit, hasLimit := argInt(args, "limit")
	if limit <= 0 {
		hasLimit = false
	}

	// 1.解析路径后整读
	absPath, err := resolvePathFrom(path, r.WorkingDir)
	if err != nil {
		return llm.ToolResult{}, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return llm.ToolResult{}, err
	}
	if len(data) == 0 {
		return llm.ToolResult{Content: textContents("")}, nil
	}

	// 2.是图片就走图片分支
	if mimeType := imageutil.Sniff(data); mimeType != "" {
		return llm.ToolResult{Content: r.readImage(mimeType, data)}, nil
	}

	// 3.切行，结尾换行切出的空串不算一行
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
		return llm.ToolResult{}, fmt.Errorf("Offset %d is beyond end of file (%d lines total)", offset, totalFileLines)
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
		return llm.ToolResult{Content: textContents(fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use offset=%d to continue.]",
			startLineDisplay, formatSize(len(allLines[startLine])), formatSize(defaultMaxBytes),
			startLineDisplay+1))}, nil
	case truncation.Truncated || (hasLimit && startLine+userLimitedLines < totalFileLines):
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		reason := ""
		if truncation.TruncatedBy == truncatedByBytes {
			reason = formatSize(defaultMaxBytes) + " limit"
		}
		return llm.ToolResult{Content: textContents(truncation.Content + truncationNote(
			fmt.Sprintf("Showing lines %d-%d of %d", startLineDisplay, endLineDisplay, totalFileLines),
			reason,
			fmt.Sprintf("offset=%d", endLineDisplay+1),
		))}, nil
	default:
		return llm.ToolResult{Content: textContents(truncation.Content)}, nil
	}
}

func (r *Read) readImage(mimeType string, data []byte) []llm.Content {
	note := "Read image file [" + mimeType + "]"

	if !r.SupportsImages {
		return textContents(note + "\n" + noVisionNote)
	}

	result, err := imageutil.Process(data, mimeType)
	if err != nil {
		return textContents(note + "\n" + err.Error())
	}
	if result.Width != result.OriginalWidth || result.Height != result.OriginalHeight {
		note += "\n" + dimensionNote(result)
	}
	return []llm.Content{
		llm.TextContent{Text: note},
		llm.ImageContent{Data: result.Data, MIMEType: result.MIMEType},
	}
}

// dimensionNote 告诉模型缩放前后的尺寸，方便把坐标映射回原图
func dimensionNote(result imageutil.Data) string {
	scale := float64(result.OriginalWidth) / float64(result.Width)
	return fmt.Sprintf("[Image: original %dx%d, displayed at %dx%d. Multiply coordinates by %.2f to map to original image.]",
		result.OriginalWidth, result.OriginalHeight, result.Width, result.Height, scale)
}
