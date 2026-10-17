package tool

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// FileHistoryTracker 保存内置文件工具修改前的状态。
type FileHistoryTracker interface {
	TrackEdit(context.Context, string, string) error
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
