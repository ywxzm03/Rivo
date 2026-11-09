package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBenchSession 写入一个包含 header 与若干消息的会话文件
func writeBenchSession(b *testing.B, path, id string, messages int, textSize int) {
	b.Helper()
	var builder strings.Builder
	header, err := json.Marshal(Header{Type: TypeSession, Version: CurrentVersion, ID: id, Timestamp: "2026-01-01T00:00:00Z", Cwd: "/tmp/bench"})
	if err != nil {
		b.Fatal(err)
	}
	builder.Write(header)
	builder.WriteByte('\n')
	parent := "null"
	text := strings.Repeat("x", textSize)
	for i := range messages {
		entryID := fmt.Sprintf("%s-%d", id, i)
		line := fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, entryID, parent, text)
		builder.WriteString(line)
		builder.WriteByte('\n')
		parent = fmt.Sprintf("%q", entryID)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		b.Fatal(err)
	}
}

// benchSessionDir 创建 count 个约 size 字节的会话文件
func benchSessionDir(b *testing.B, count, messages, textSize int) string {
	b.Helper()
	dir := b.TempDir()
	for i := range count {
		path := filepath.Join(dir, fmt.Sprintf("2026-01-01T00-00-%02dZ_bench%02d.jsonl", i, i))
		writeBenchSession(b, path, fmt.Sprintf("bench%02d", i), messages, textSize)
	}
	return dir
}

func BenchmarkListSessions(b *testing.B) {
	dir := benchSessionDir(b, 20, 200, 900)
	b.ResetTimer()
	for b.Loop() {
		if _, err := listSessionsInDir(dir); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpenLargeSession(b *testing.B) {
	dir := benchSessionDir(b, 1, 4000, 900)
	path := filepath.Join(dir, "2026-01-01T00-00-00Z_bench00.jsonl")
	b.ResetTimer()
	for b.Loop() {
		if _, err := Open(path, "/tmp/bench"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLatestSessionPath(b *testing.B) {
	dir := benchSessionDir(b, 20, 200, 900)
	b.ResetTimer()
	for b.Loop() {
		if _, err := latestSessionPathInDir(dir); err != nil {
			b.Fatal(err)
		}
	}
}
