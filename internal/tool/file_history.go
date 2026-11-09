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

// trackFileEdit 在写盘前登记备份，返回备份失败原因供调用方提示用户
func trackFileEdit(ctx context.Context, tracker FileHistoryTracker, path string) error {
	if tracker == nil {
		return nil
	}
	messageID := llm.CurrentUserMessageID(ctx)
	if messageID == "" {
		return nil
	}
	return tracker.TrackEdit(ctx, messageID, path)
}

// trackFileEditContent 优先用内容快照登记备份，tracker 不支持时退回按路径备份
func trackFileEditContent(ctx context.Context, tracker FileHistoryTracker, path string, content []byte, mode os.FileMode) error {
	if tracker == nil {
		return nil
	}
	messageID := llm.CurrentUserMessageID(ctx)
	if messageID == "" {
		return nil
	}
	if contentTracker, ok := tracker.(FileHistoryContentTracker); ok {
		return contentTracker.TrackEditContent(ctx, messageID, path, content, mode)
	}
	return tracker.TrackEdit(ctx, messageID, path)
}

// Tracker 把可能为 nil 的具体指针归一成真正的 nil 接口，
// 避免 typed-nil 让接口判空失效
func Tracker[T FileHistoryTracker](tracker T) FileHistoryTracker {
	value := reflect.ValueOf(tracker)
	if !value.IsValid() || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return nil
	}
	return tracker
}
