package tool

import (
	"context"
	"os"
	"reflect"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// FileHistoryTracker 保存内置文件工具修改前的状态
type FileHistoryTracker interface {
	TrackEdit(context.Context, string, string) error
}

// FileHistoryContentTracker 可复用工具已读取的内容生成备份
type FileHistoryContentTracker interface {
	TrackEditContent(context.Context, string, string, []byte, os.FileMode) error
}

// trackFileEdit 在写盘前登记备份。
// tracker 常由 FileHistory() 直接返回 typed-nil 指针,接口判空对它无效,
// 因此这里再用 reflect 排除 nil 指针,避免写文件时解引用崩溃
func trackFileEdit(ctx context.Context, tracker FileHistoryTracker, path string) {
	if tracker == nil {
		return
	}
	if value := reflect.ValueOf(tracker); value.Kind() == reflect.Pointer && value.IsNil() {
		return
	}
	messageID := llm.CurrentUserMessageID(ctx)
	if messageID == "" {
		return
	}
	// 备份失败不阻止工具继续写盘
	_ = tracker.TrackEdit(ctx, messageID, path)
}

func trackFileEditContent(ctx context.Context, tracker FileHistoryTracker, path string, content []byte, mode os.FileMode) {
	if tracker == nil {
		return
	}
	if value := reflect.ValueOf(tracker); value.Kind() == reflect.Pointer && value.IsNil() {
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
