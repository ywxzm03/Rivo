package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func userMsg(text string) llm.Message {
	return llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}}
}

func assistantMsg(text string) llm.Message {
	return llm.AssistantMessage{
		Provider:   "openai",
		Model:      "gpt-5",
		Content:    []llm.Content{llm.TextContent{Text: text}},
		StopReason: llm.StopReasonStop,
	}
}

// readLines 读会话文件并按行拆分，行内不应有换行
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func TestFlushTimingAndBatchOrder(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)

	// 1.纯设置类 entry 不触发落盘
	if err := m.AppendModelChange("openai", "gpt-5"); err != nil {
		t.Fatalf("AppendModelChange() error = %v", err)
	}
	if _, err := os.Stat(m.sessionFile); !os.IsNotExist(err) {
		t.Fatal("session file created before first conversation message")
	}
	if err := m.AppendThinkingLevelChange("high"); err != nil {
		t.Fatalf("AppendThinkingLevelChange() error = %v", err)
	}

	// 2.首条 user 消息触发 flush，批量为 header + 暂存 entry + 当前消息
	if err := m.AppendMessage(userMsg("hi")); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	lines := readLines(t, m.sessionFile)
	if len(lines) != 4 {
		t.Fatalf("flushed lines = %d, want 4 (header, model_change, thinking_level_change, user)", len(lines))
	}
	var header Header
	if err := jsonUnmarshal(lines[0], &header); err != nil {
		t.Fatalf("header line invalid: %v", err)
	}
	if header.Version != CurrentVersion || header.Cwd != "/project" || header.ID == "" {
		t.Errorf("header = %+v, want version 1 with cwd and id", header)
	}

	// 3.后续每条消息追加一行
	if err := m.AppendMessage(assistantMsg("hello")); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	if got := len(readLines(t, m.sessionFile)); got != 5 {
		t.Fatalf("lines after assistant append = %d, want 5", got)
	}
}

// jsonUnmarshal 包装 json.Unmarshal，测试内使用
func jsonUnmarshal(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}

func TestErrorAssistantTriggersFlush(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	errMsg := llm.AssistantMessage{
		Content:    []llm.Content{llm.TextContent{Text: "boom"}},
		StopReason: llm.StopReasonError,
	}
	if err := m.AppendMessage(errMsg); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	if got := len(readLines(t, m.sessionFile)); got != 2 {
		t.Fatalf("lines = %d, want 2 (error assistant alone triggers flush)", got)
	}
}

func TestFlushOpenFailureRetriesAfterBlockerRemoved(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "f")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New("/project", filepath.Join(dir, "f", "sub"))

	// 1.目录创建失败，文件未创建，错误返回
	if err := m.AppendMessage(userMsg("hi")); err == nil {
		t.Fatal("AppendMessage() error = nil, want mkdir failure")
	}
	// 2.解除阻塞后下一条消息自然重走 flush
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(assistantMsg("hello")); err != nil {
		t.Fatalf("AppendMessage() after blocker removed error = %v", err)
	}
	if got := len(readLines(t, m.sessionFile)); got != 3 {
		t.Fatalf("lines = %d, want 3 (header, user, assistant)", got)
	}
}

func TestFlushMidWriteWedgeOnExistingFile(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	// 模拟上次半写残留的文件
	if err := os.WriteFile(m.sessionFile, []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(userMsg("hi")); err == nil {
		t.Fatal("AppendMessage() error = nil, want exclusive-create failure")
	}
	// 同进程内持续报错，内存状态继续推进
	if err := m.AppendMessage(assistantMsg("hello")); err == nil {
		t.Fatal("second AppendMessage() error = nil, want wedge")
	}
	if m.flushed {
		t.Error("flushed = true, want false after wedge")
	}
	if len(m.entries) != 0 || len(m.pending) != 1 {
		t.Errorf("failed writes changed committed records: entries=%d pending=%d", len(m.entries), len(m.pending))
	}
}

func TestAppendLineFailureRetriesSameEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires the Unix file permission model")
	}
	dir := t.TempDir()
	m := New("/project", dir)
	if err := m.AppendMessage(userMsg("hi")); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	if err := os.Chmod(m.sessionFile, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(assistantMsg("hello")); err == nil {
		t.Fatal("AppendMessage() error = nil, want append failure")
	}
	if err := os.Chmod(m.sessionFile, 0o644); err != nil {
		t.Fatal(err)
	}
	// 显式补写失败的 assistant，成功后才能追加 toolResult
	if err := m.RetryPending(); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(llm.ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []llm.Content{llm.TextContent{Text: "r"}}}); err != nil {
		t.Fatalf("AppendMessage() after chmod back error = %v", err)
	}
	lines := readLines(t, m.sessionFile)
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (header, user, assistant, toolResult)", len(lines))
	}
	if !strings.Contains(lines[2], `"role":"assistant"`) {
		t.Errorf("line 2 = %q, want retried assistant entry", lines[2])
	}
}

func TestOpenReloadAndContinue(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	if err := m.AppendMessage(userMsg("hi")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(assistantMsg("hello")); err != nil {
		t.Fatal(err)
	}

	m2, err := Open(m.sessionFile, "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !m2.flushed {
		t.Error("loaded session flushed = false, want true")
	}
	// 重载后的追加直接落盘，不再走延迟逻辑
	if err := m2.AppendMessage(llm.ToolResultMessage{ToolCallID: "c1", ToolName: "read"}); err != nil {
		t.Fatalf("AppendMessage() on loaded session error = %v", err)
	}
	if got := len(readLines(t, m2.sessionFile)); got != 4 {
		t.Fatalf("lines = %d, want 4", got)
	}

	ctx := m2.BuildContext()
	if len(ctx.Messages()) != 3 {
		t.Fatalf("messages = %d, want 3", len(ctx.Messages()))
	}
	if messageRole(ctx.Messages()[0]) != "user" || messageRole(ctx.Messages()[1]) != "assistant" || messageRole(ctx.Messages()[2]) != "toolResult" {
		t.Errorf("message roles = %s/%s/%s, want user/assistant/toolResult",
			messageRole(ctx.Messages()[0]), messageRole(ctx.Messages()[1]), messageRole(ctx.Messages()[2]))
	}
	if ctx.Provider != "openai" || ctx.Model != "gpt-5" {
		t.Errorf("model = %s/%s, want openai/gpt-5", ctx.Provider, ctx.Model)
	}
}

func TestOpenEmptyFileKeepsExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "explicit.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if m.sessionFile != path {
		t.Errorf("sessionFile = %q, want explicit path %q", m.sessionFile, path)
	}
	if err := m.AppendMessage(userMsg("hi")); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (header written to explicit path on flush)", len(lines))
	}
}

func TestOpenRejectsInvalidSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(path, []byte("{\"no\":\"header\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "/project"); err == nil {
		t.Fatal("Open() error = nil, want invalid session error")
	}
	// 无效文件不被修改
	data, _ := os.ReadFile(path)
	if string(data) != "{\"no\":\"header\"}\n" {
		t.Errorf("invalid file modified: %q", data)
	}
}

func TestOpenDuplicateIDAndOrphan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	data := strings.Join([]string{
		`{"type":"session","version":1,"id":"s1","timestamp":"t","cwd":"/p"}`,
		`{"type":"message","id":"dddddddd","parentId":null,"timestamp":"t","message":{"role":"user","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"model_change","id":"dddddddd","parentId":null,"timestamp":"t","provider":"openai","modelId":"gpt-5"}`,
		`{"type":"message","id":"eeeeeeee","parentId":"dddddddd","timestamp":"t","message":{"role":"user","content":[{"type":"text","text":"child"}]}}`,
		`{"type":"message","id":"ffffffff","parentId":"missing00","timestamp":"t","message":{"role":"user","content":[{"type":"text","text":"orphan"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// 重复 ID 索引指向后出现者
	if _, ok := m.byID["dddddddd"].(*ModelChangeEntry); !ok {
		t.Errorf("byID[dddddddd] type = %T, want *ModelChangeEntry (later wins)", m.byID["dddddddd"])
	}
	if m.leafID != "ffffffff" {
		t.Errorf("leafID = %q, want last entry ffffffff", m.leafID)
	}
	// 孤儿 entry 的路径在断点停止，只包含自身
	ctx := m.BuildContext()
	if len(ctx.Messages()) != 1 {
		t.Fatalf("messages from orphan leaf = %d, want 1", len(ctx.Messages()))
	}
}

func TestOpenUnknownEntryKeepsTreeChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	data := strings.Join([]string{
		`{"type":"session","version":1,"id":"s1","timestamp":"t","cwd":"/p"}`,
		`{"type":"message","id":"aaaaaaaa","parentId":null,"timestamp":"t","message":{"role":"user","content":[{"type":"text","text":"u1"}]}}`,
		`{"type":"label","id":"bbbbbbbb","parentId":"aaaaaaaa","timestamp":"t","targetId":"aaaaaaaa","label":"bookmark"}`,
		`{"type":"message","id":"cccccccc","parentId":"bbbbbbbb","timestamp":"t","message":{"role":"user","content":[{"type":"text","text":"u2"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	// label 是未实现的类型，但作为未知 entry 仍参与树路径
	m, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	ctx := m.BuildContext()
	if len(ctx.Messages()) != 2 {
		t.Fatalf("messages = %d, want 2 (unknown entry contributes no message but keeps chain)", len(ctx.Messages()))
	}
	// leaf 在未知 entry 之后，新追加成为其后代
	if err := m.AppendMessage(userMsg("u3")); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	if !strings.Contains(lines[len(lines)-1], `"parentId":"cccccccc"`) {
		t.Errorf("new entry parent = %q, want cccccccc", lines[len(lines)-1])
	}
}

func TestOpenDefersNewlineRepairToAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	data := `{"type":"session","version":1,"id":"s1","timestamp":"t","cwd":"/p"}` + "\n" +
		`{"type":"message","id":"aaaaaaaa","parentId":null,"timestamp":"t","message":{"role":"user","content":[{"type":"text","text":"u1"}]}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// 加载期不修改文件，补换行推迟到首次追加
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(data)) {
		t.Fatalf("file size after Open = %d, want %d (no write during load)", info.Size(), len(data))
	}
	// 首次追加先补换行再写新行，两行各自完整
	if err := m.AppendMessage(userMsg("u2")); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, path)); got != 3 {
		t.Fatalf("lines after append = %d, want 3", got)
	}
	m2, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("re-Open() error = %v", err)
	}
	if len(m2.entries) != 2 {
		t.Errorf("entries after re-open = %d, want 2", len(m2.entries))
	}
	if m2.needsNewline {
		t.Error("needsNewline = true after append, want false")
	}
}

func TestOpenTornLastLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	data := `{"type":"session","version":1,"id":"s1","timestamp":"t","cwd":"/p"}` + "\n" +
		`{"type":"message","id":"aaaaaaaa","parentId":null,"timestamp":"t","message":{"role":"user","con`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(m.entries) != 0 {
		t.Errorf("entries = %d, want 0 (torn line skipped)", len(m.entries))
	}
	// 残行之后的追加先补换行再写新行，重新加载时残行被跳过、新行完整解析
	if err := m.AppendMessage(userMsg("u2")); err != nil {
		t.Fatal(err)
	}
	m2, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("re-Open() error = %v", err)
	}
	if len(m2.entries) != 1 {
		t.Errorf("entries after re-open = %d, want 1 (torn line skipped, new entry parsed)", len(m2.entries))
	}
}

func TestBranchMovesLeafOnly(t *testing.T) {
	dir := t.TempDir()
	m := New("/project", dir)
	for _, msg := range []llm.Message{userMsg("u1"), assistantMsg("a1"), userMsg("u2")} {
		if err := m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	before := len(readLines(t, m.sessionFile))
	if err := m.Branch(m.entries[0].base().ID); err != nil {
		t.Fatalf("Branch() error = %v", err)
	}
	if err := m.AppendMessage(userMsg("u3")); err != nil {
		t.Fatal(err)
	}
	// 分支只追加一行，不复制历史
	if got := len(readLines(t, m.sessionFile)); got != before+1 {
		t.Errorf("lines after branch append = %d, want %d", got, before+1)
	}
	if err := m.Branch("nonexistent"); err == nil {
		t.Error("Branch(nonexistent) error = nil, want error")
	}
}

func TestEncodingFailureDoesNotLeavePartialFileOrPublishEntries(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(llm.ToolResultMessage{Details: make(chan int)}); err == nil {
		t.Fatal("want encoding failure")
	}
	if len(m.entries) != 0 || m.leafID != "" {
		t.Fatal("failed encoding published state")
	}
	if _, err := os.Stat(m.sessionFile); !os.IsNotExist(err) {
		t.Fatal("failed encoding created session file")
	}
}

func TestDirNameEncoding(t *testing.T) {
	tests := []struct {
		cwd  string
		want string
	}{
		{"/Users/wm/Rivo", "--Users-wm-Rivo--"},
		{`C:\Users\wm\Rivo`, "--C--Users-wm-Rivo--"},
		{"/", "----"},
		{"/a:b/c", "--a-b-c--"},
	}
	for _, tt := range tests {
		if got := dirName(tt.cwd); got != tt.want {
			t.Errorf("dirName(%q) = %q, want %q", tt.cwd, got, tt.want)
		}
	}
}
