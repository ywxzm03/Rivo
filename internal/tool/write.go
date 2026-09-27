package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type Write struct{}

func (w *Write) Name() string { return "write" }

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

// Execute 创建或覆盖文件，并在需要时递归创建父目录
func (w *Write) Execute(ctx context.Context, args map[string]any) ([]llm.Content, error) {
	// 1.读取必填参数，空内容也是合法写入
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("write: missing required parameter: path")
	}
	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("write: missing required parameter: content")
	}

	// 2.解析路径并创建父目录
	absPath, err := resolvePath(path)
	if err != nil {
		return nil, err
	}

	// 3.串行执行同一文件的目录创建与写入
	err = withFileMutationQueue(ctx, absPath, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.WriteFile(absPath, []byte(content), 0o666); err != nil {
			return err
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return textContents(fmt.Sprintf("Successfully wrote to %s", path)), nil
}
