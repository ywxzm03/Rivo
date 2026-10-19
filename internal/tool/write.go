package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type Write struct{ History FileHistoryTracker }

func (w *Write) Name() string { return "write" }

func (w *Write) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (w *Write) PromptSnippet() string { return "Create or overwrite files" }

func (w *Write) PromptGuidelines() []string {
	return []string{"Use write only for new files or complete rewrites."}
}

func (w *Write) Description() string {
	return "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories."
}

func (w *Write) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"path", "content"},
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write (relative or absolute)",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
	}
}

// Execute 创建或覆盖文件，需要时递归创建父目录
func (w *Write) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	// 1.读取必填参数，空内容也是合法写入
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return llm.ToolResult{}, fmt.Errorf("write: missing required parameter: path")
	}
	content, ok := args["content"].(string)
	if !ok {
		return llm.ToolResult{}, fmt.Errorf("write: missing required parameter: content")
	}

	// 2.解析路径并创建父目录
	absPath, err := resolvePath(path)
	if err != nil {
		return llm.ToolResult{}, err
	}

	// 3.串行执行同一文件的目录创建与写入
	_, err = withFileMutationQueue(ctx, absPath, func() (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		trackFileEdit(ctx, w.History, absPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return struct{}{}, err
		}
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		if err := os.WriteFile(absPath, []byte(content), 0o666); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, ctx.Err()
	})
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Content: textContents(fmt.Sprintf("Successfully wrote to %s", path))}, nil
}
