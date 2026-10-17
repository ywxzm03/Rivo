package tool

import (
	"context"
	"os"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// FileHistoryTracker 保存内置文件工具修改前的状态。
type FileHistoryTracker interface {
	TrackEdit(context.Context, string, string) error
}

// FileHistoryContentTracker 可复用工具已经读取的原始文件内容生成备份。
type FileHistoryContentTracker interface {
	TrackEditContent(context.Context, string, string, []byte, os.FileMode) error
}

func trackFileEdit(ctx context.Context, tracker FileHistoryTracker, path string) {
	if tracker == nil {
		return
	}
	messageID := llm.CurrentUserMessageID(ctx)
	if messageID == "" {
		return
	}
	// 与 Claude Code 一致：备份失败不阻止工具继续写盘。
	_ = tracker.TrackEdit(ctx, messageID, path)
}

func trackFileEditContent(ctx context.Context, tracker FileHistoryTracker, path string, content []byte, mode os.FileMode) {
	if tracker == nil {
		return
	}
	messageID := llm.CurrentUserMessageID(ctx)
	if messageID == "" {
		return
	}
	if contentTracker, ok := tracker.(FileHistoryContentTracker); ok {
		_ = contentTracker.TrackEditContent(ctx, messageID, path, content, mode)
		return
	}
	trackFileEdit(ctx, tracker, path)
}
