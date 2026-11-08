package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 增量读取必须只消费已写完整的行,并给出正确的下一偏移
func TestReadMessagesReturnsCompleteLinesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sm, err := NewAt(dir, path, "read-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sm.AppendMessages([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "one"}}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "two"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// 追加一行没写完的内容,模拟写入中断
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"message","message":{"role":"user"`); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	messages, offset, err := ReadMessages(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 complete lines", len(messages))
	}
	// 再读一次不应重复返回已消费的行
	again, next, err := ReadMessages(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 || next != offset {
		t.Fatalf("second read = %d messages at %d, want 0 at %d", len(again), next, offset)
	}
}

// 真实 IO 错误必须上报,不能当成"最后一行没写完"静默吞掉
func TestReadMessagesPropagatesIOError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sm, err := NewAt(dir, path, "read-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sm.AppendMessages([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "one"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// 以只写方式打开后读取会得到非 EOF 错误
	if _, _, err := ReadMessages(path, 1<<20); err != nil {
		// 越过文件末尾只应产生 EOF,这里确认不会误报
		t.Fatalf("seek past EOF must not fail: %v", err)
	}
	if _, _, err := ReadMessages(filepath.Join(dir, "missing.jsonl"), 0); !os.IsNotExist(err) {
		t.Fatalf("missing file error = %v, want not-exist", err)
	}
}

// 超过单次预算的大消息不能被跳过:它必须独占一轮被完整读出
func TestReadMessagesDeliversOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sm, err := NewAt(dir, path, "read-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sm.AppendMessages([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "before"}}},
	}); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("y", readChunkBytes+4096)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"` + huge + `"}]}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	var texts []string
	offset := int64(0)
	for range 10 {
		messages, next, err := ReadMessages(path, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if user, ok := message.(llm.UserMessage); ok {
				for _, content := range user.Content {
					if text, ok := content.(llm.TextContent); ok {
						texts = append(texts, text.Text)
					}
				}
			}
		}
		if next == offset {
			break
		}
		offset = next
	}
	if len(texts) != 2 {
		t.Fatalf("delivered %d messages, want 2 (the oversized one must not be dropped)", len(texts))
	}
	if texts[1] != huge {
		t.Fatalf("oversized message was truncated: got %d bytes, want %d", len(texts[1]), len(huge))
	}
}
