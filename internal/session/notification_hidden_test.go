package session

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func hiddenUserMessage(text string) llm.UserMessage {
	return llm.UserMessage{Hidden: true, Content: []llm.Content{llm.TextContent{Text: text}}}
}

// 隐藏标记必须能落盘再读回,否则重开会话后通知又会渲染出来
func TestHiddenUserMessageRoundtrip(t *testing.T) {
	message := hiddenUserMessage("<task-notification>\n<status>completed</status>\n</task-notification>")
	raw, err := encodeMessage(message)
	if err != nil {
		t.Fatalf("encodeMessage() error = %v", err)
	}
	decoded, err := decodeMessage(raw)
	if err != nil {
		t.Fatalf("decodeMessage() error = %v", err)
	}
	user, ok := decoded.(llm.UserMessage)
	if !ok || !user.Hidden {
		t.Fatalf("decoded = %#v, want a hidden user message", decoded)
	}
	if got := llm.TextOf(user.Content); got != llm.TextOf(message.Content) {
		t.Fatalf("content = %q, want the notification payload preserved", got)
	}
}

// 普通用户消息不写隐藏字段,旧会话文件格式保持不变
func TestVisibleUserMessageOmitsHiddenField(t *testing.T) {
	raw, err := encodeMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hi"}}})
	if err != nil {
		t.Fatalf("encodeMessage() error = %v", err)
	}
	if strings.Contains(string(raw), "hidden") {
		t.Fatalf("encoded = %s, want no hidden field for a visible message", raw)
	}
}

// 会话树要给显示层提供隐藏标记,由它决定是否成节点
func TestTreeMarksHiddenUserEntries(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(hiddenUserMessage("<task-notification>")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "real prompt"}}}); err != nil {
		t.Fatal(err)
	}

	entries := m.Tree().Entries
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want two", entries)
	}
	if !entries[0].Hidden || entries[1].Hidden {
		t.Fatalf("hidden flags = %v/%v, want only the notification marked", entries[0].Hidden, entries[1].Hidden)
	}
}

// 会话列表的摘要是首条用户消息,通知不能顶替真实提问
func TestListSessionsSkipsHiddenNotificationSummary(t *testing.T) {
	dir := t.TempDir()
	m := New("/tmp/proj", dir)
	if err := m.AppendMessage(hiddenUserMessage("<task-notification>\n<status>completed</status>")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "real prompt"}}}); err != nil {
		t.Fatal(err)
	}

	infos, err := listSessionsInDir(dir)
	if err != nil {
		t.Fatalf("listSessionsInDir() error = %v", err)
	}
	if len(infos) != 1 || infos[0].Summary != "real prompt" {
		t.Fatalf("infos = %+v, want the summary from the visible prompt", infos)
	}
}

// 本次改动之前落盘的通知没有 hidden 字段,解码时按渲染形态补上标记,
// 否则老会话重开后仍会把通知正文渲染出来
func TestLegacyNotificationDecodedAsHidden(t *testing.T) {
	raw := []byte(`{"role":"user","content":[{"type":"text","text":"<task-notification>\n<task-id>bash-1</task-id>\n<status>completed</status>\n</task-notification>"}]}`)
	decoded, err := decodeMessage(raw)
	if err != nil {
		t.Fatalf("decodeMessage() error = %v", err)
	}
	if !llm.IsHiddenMessage(decoded) {
		t.Fatalf("decoded = %#v, want legacy notification marked hidden", decoded)
	}
}

// 用户自己写下同形文本时不能被误判:缺少成对标签或正文不含分段都不算通知
func TestUserTextIsNotMistakenForNotification(t *testing.T) {
	for _, text := range []string{
		"<task-notification>单行同形文本</task-notification>",
		"看看 <task-notification> 这个标签怎么写",
		"<task-notification>\n没有闭合标签",
	} {
		raw, err := encodeMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeMessage(raw)
		if err != nil {
			t.Fatal(err)
		}
		if llm.IsHiddenMessage(decoded) {
			t.Fatalf("text %q was mistaken for a notification", text)
		}
	}
}
