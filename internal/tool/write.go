package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ywxzm03/Rivo/internal/llm"
	toolargs "github.com/ywxzm03/Rivo/internal/tool/args"
)

type Write struct {
	History    FileHistoryTracker
	WorkingDir string
}

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
// writeParams 是 write 工具的参数形状
type writeParams struct {
	Path string `json:"path"`
	// Content 用指针区分「未提供」与「显式写空串」
	Content *string `json:"content"`
}

func (w *Write) Execute(ctx context.Context, values map[string]any) (llm.ToolResult, error) {
	// 1.读取必填参数，空内容也是合法写入
	params, err := toolargs.Decode[writeParams](values)
	if err != nil {
		return llm.ToolResult{}, err
	}
	path := params.Path
	if path == "" {
		return llm.ToolResult{}, fmt.Errorf("write: missing required parameter: path")
	}
	// content 必须提供但允许为空串，用指针区分「未提供」
	if params.Content == nil {
		return llm.ToolResult{}, fmt.Errorf("write: missing required parameter: content")
	}
	content := *params.Content

	// 2.解析路径并创建父目录
	absPath, err := resolvePathFrom(path, w.WorkingDir)
	if err != nil {
		return llm.ToolResult{}, err
	}

	// 3.串行执行同一文件的目录创建与写入
	var historyErr error
	_, err = withFileMutationQueue(ctx, absPath, func() (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		// 备份失败不阻断写入，但要让用户知道 undo 历史不完整
		historyErr = trackFileEdit(ctx, w.History, absPath)
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
	message := fmt.Sprintf("Successfully wrote to %s", path)
	if historyErr != nil {
		message += fmt.Sprintf(" (file history unavailable: %v)", historyErr)
	}
	return llm.ToolResult{Content: textContents(message)}, nil
}
