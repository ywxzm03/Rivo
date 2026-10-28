package llm

import "strings"

const (
	TaskNotificationOpenTag  = "<task-notification>"
	TaskNotificationCloseTag = "</task-notification>"
)

func LooksLikeTaskNotification(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, TaskNotificationOpenTag) || !strings.HasSuffix(trimmed, TaskNotificationCloseTag) {
		return false
	}
	inner := trimmed[len(TaskNotificationOpenTag) : len(trimmed)-len(TaskNotificationCloseTag)]
	// 渲染结果至少含 task-id/status 一段,必然换行;单行同形文本按用户正文处理
	return strings.Contains(inner, "\n")
}
