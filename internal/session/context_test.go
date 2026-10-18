package session

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestBuildContextPathWalkAndBranch(t *testing.T) {
	m := New("/project", t.TempDir())
	for _, msg := range []llm.Message{userMsg("u1"), assistantMsg("a1"), userMsg("u2"), assistantMsg("a2")} {
		if err := m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	ctx := m.BuildContext()
	if len(ctx.Messages()) != 4 {
		t.Fatalf("messages = %d, want 4", len(ctx.Messages()))
	}
	if messageRole(ctx.Messages()[0]) != "user" || messageRole(ctx.Messages()[3]) != "assistant" {
		t.Error("path order wrong, want root-to-leaf")
	}
	if ctx.ThinkingLevel != "off" {
		t.Errorf("thinkingLevel = %q, want default off", ctx.ThinkingLevel)
	}

	// usage entry 计入会话但不产出 LLM 消息
	if err := m.AppendUsage("cache_warm", "openai", "gpt-5", llm.Usage{InputTokens: 0, OutputTokens: 0}, ""); err != nil {
		t.Fatal(err)
	}
	ctx = m.BuildContext()
	if len(ctx.Messages()) != 4 {
		t.Fatalf("messages after usage entry = %d, want 4 (usage not in context)", len(ctx.Messages()))
	}

	// Branch 到 u1 后追加，路径只含 u1 与新消息
	u1 := m.entries[0].base().ID
	if err := m.Branch(u1); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(userMsg("u3")); err != nil {
		t.Fatal(err)
	}
	ctx = m.BuildContext()
	if len(ctx.Messages()) != 2 {
		t.Fatalf("messages after branch = %d, want 2", len(ctx.Messages()))
	}
	if messageText(t, ctx.Messages()[0]) != "u1" || messageText(t, ctx.Messages()[1]) != "u3" {
		t.Errorf("branch path = [%s, %s], want [u1, u3]", messageText(t, ctx.Messages()[0]), messageText(t, ctx.Messages()[1]))
	}
}

func TestBuildContextModelExtraction(t *testing.T) {
	t.Run("assistant fallback", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("u1")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(assistantMsg("a1")); err != nil {
			t.Fatal(err)
		}
		ctx := m.BuildContext()
		if ctx.Provider != "openai" || ctx.Model != "gpt-5" {
			t.Errorf("model = %s/%s, want openai/gpt-5 from assistant", ctx.Provider, ctx.Model)
		}
	})
	t.Run("model_change wins", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(assistantMsg("a1")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendModelChange("deepseek", "deepseek-chat"); err != nil {
			t.Fatal(err)
		}
		ctx := m.BuildContext()
		if ctx.Provider != "deepseek" || ctx.Model != "deepseek-chat" {
			t.Errorf("model = %s/%s, want deepseek/deepseek-chat", ctx.Provider, ctx.Model)
		}
	})
	t.Run("thinking level", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendThinkingLevelChange("high"); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(userMsg("u1")); err != nil {
			t.Fatal(err)
		}
		if ctx := m.BuildContext(); ctx.ThinkingLevel != "high" {
			t.Errorf("thinkingLevel = %q, want high", ctx.ThinkingLevel)
		}
	})
}

func TestBuildContextCompaction(t *testing.T) {
	m := New("/project", t.TempDir())
	var ids []string
	for _, msg := range []llm.Message{userMsg("u1"), assistantMsg("a1"), userMsg("u2"), assistantMsg("a2")} {
		if err := m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.leafID)
	}
	// 保留 u2 起的历史
	if err := m.AppendCompaction("early chat summarized", ids[2], 5000, nil, &llm.Usage{InputTokens: 100, OutputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(userMsg("u3")); err != nil {
		t.Fatal(err)
	}

	ctx := m.BuildContext()
	if len(ctx.Messages()) != 4 {
		t.Fatalf("messages = %d, want 4 (summary, u2, a2, u3)", len(ctx.Messages()))
	}
	summary := messageText(t, ctx.Messages()[0])
	if !strings.HasPrefix(summary, "The conversation history before this point was compacted") || !strings.Contains(summary, "early chat summarized") || !strings.HasSuffix(summary, "</summary>") {
		t.Errorf("summary message not wrapped: %q", summary)
	}
	if messageText(t, ctx.Messages()[1]) != "u2" || messageText(t, ctx.Messages()[2]) != "a2" || messageText(t, ctx.Messages()[3]) != "u3" {
		t.Errorf("kept range wrong: [%s, %s, %s]", messageText(t, ctx.Messages()[1]), messageText(t, ctx.Messages()[2]), messageText(t, ctx.Messages()[3]))
	}
	if ctx.Model != "gpt-5" {
		t.Errorf("model = %q, want gpt-5 (assistant fallback still works)", ctx.Model)
	}
}

func TestBuildContextCompactionZeroRetain(t *testing.T) {
	t.Run("empty firstKept stores own id", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("u1")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendCompaction("all summarized", "", 1, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(userMsg("u2")); err != nil {
			t.Fatal(err)
		}
		ctx := m.BuildContext()
		if len(ctx.Messages()) != 2 {
			t.Fatalf("messages = %d, want 2 (summary, u2)", len(ctx.Messages()))
		}
		compaction := m.entries[1].(*CompactionEntry)
		if compaction.FirstKeptEntryID != compaction.ID {
			t.Errorf("firstKeptEntryId = %q, want own id %q", compaction.FirstKeptEntryID, compaction.ID)
		}
	})
	t.Run("dangling firstKept is zero retain", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("u1")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendCompaction("all summarized", "deadbeef", 1, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(userMsg("u2")); err != nil {
			t.Fatal(err)
		}
		ctx := m.BuildContext()
		if len(ctx.Messages()) != 2 {
			t.Fatalf("messages = %d, want 2 (summary, u2)", len(ctx.Messages()))
		}
	})
}

func TestBuildContextIgnoresOlderCompaction(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(userMsg("u1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendCompaction("first summary", m.leafID, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(userMsg("u2")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendCompaction("second summary", m.entries[0].base().ID, 2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(userMsg("u3")); err != nil {
		t.Fatal(err)
	}
	ctx := m.BuildContext()
	// 期望：第二个摘要 + u1 + u2 + u3，第一个摘要不产出消息
	if len(ctx.Messages()) != 4 {
		t.Fatalf("messages = %d, want 4", len(ctx.Messages()))
	}
	if got := messageText(t, ctx.Messages()[0]); !strings.Contains(got, "second summary") {
		t.Errorf("first message = %q, want second summary", got)
	}
	for _, msg := range ctx.Messages()[1:] {
		if strings.Contains(messageText(t, msg), "summary") {
			t.Error("older compaction produced a message")
		}
	}
}

func TestBuildContextContextEdit(t *testing.T) {
	t.Run("replace and omit", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("original")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(assistantMsg("a1")); err != nil {
			t.Fatal(err)
		}
		u1 := m.entries[0].base().ID
		a1 := m.entries[1].base().ID

		if err := m.AppendContextEdit(u1, &Replacement{Content: []llm.Content{llm.TextContent{Text: "edited"}}}); err != nil {
			t.Fatal(err)
		}
		ctx := m.BuildContext()
		if messageText(t, ctx.Messages()[0]) != "edited" {
			t.Errorf("edited user = %q, want edited", messageText(t, ctx.Messages()[0]))
		}

		// 省略 assistant
		if err := m.AppendContextEdit(a1, nil); err != nil {
			t.Fatal(err)
		}
		ctx = m.BuildContext()
		if len(ctx.Messages()) != 1 {
			t.Fatalf("messages = %d, want 1 (assistant omitted)", len(ctx.Messages()))
		}
		if messageText(t, ctx.Messages()[0]) != "edited" {
			t.Errorf("kept message = %q, want edited", messageText(t, ctx.Messages()[0]))
		}
	})
	t.Run("latest edit wins", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("original")); err != nil {
			t.Fatal(err)
		}
		u1 := m.leafID
		if err := m.AppendContextEdit(u1, &Replacement{Content: []llm.Content{llm.TextContent{Text: "first"}}}); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendContextEdit(u1, &Replacement{Content: []llm.Content{llm.TextContent{Text: "second"}}}); err != nil {
			t.Fatal(err)
		}
		if ctx := m.BuildContext(); messageText(t, ctx.Messages()[0]) != "second" {
			t.Errorf("edited = %q, want second (latest wins)", messageText(t, ctx.Messages()[0]))
		}
	})
	t.Run("branch relative", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("original")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(assistantMsg("a1")); err != nil {
			t.Fatal(err)
		}
		u1 := m.entries[0].base().ID
		a1 := m.leafID
		if err := m.AppendContextEdit(u1, &Replacement{Content: []llm.Content{llm.TextContent{Text: "edited"}}}); err != nil {
			t.Fatal(err)
		}
		// Branch 回 a1：edit 不在活跃路径上，u1 恢复原内容
		if err := m.Branch(a1); err != nil {
			t.Fatal(err)
		}
		ctx := m.BuildContext()
		if messageText(t, ctx.Messages()[0]) != "original" {
			t.Errorf("u1 after branch away from edit = %q, want original", messageText(t, ctx.Messages()[0]))
		}
	})
}

func TestAppendContextEditValidation(t *testing.T) {
	m := New("/project", t.TempDir())
	if err := m.AppendMessage(userMsg("u1")); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendMessage(assistantMsg("a1")); err != nil {
		t.Fatal(err)
	}
	a1 := m.leafID
	u1 := m.entries[0].base().ID

	if err := m.AppendContextEdit("deadbeef", nil); err == nil {
		t.Error("unknown target: error = nil, want error")
	}
	if err := m.AppendContextEdit(a1, &Replacement{Content: []llm.Content{llm.TextContent{Text: "x"}}}); err != nil {
		t.Fatalf("valid edit error = %v", err)
	}
	// Branch 回 u1 后，a1 不在活跃分支上，不能再编辑
	if err := m.Branch(u1); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendContextEdit(a1, nil); err == nil {
		t.Error("off-branch target: error = nil, want error")
	}
}

// messageText 取消息第一个 text 块，便于断言
func messageText(t *testing.T, msg llm.Message) string {
	t.Helper()
	for _, c := range messageContent(msg) {
		if text, ok := c.(llm.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func TestBuildContextContextEntries(t *testing.T) {
	m := New("/project", t.TempDir())
	var ids []string
	for _, msg := range []llm.Message{userMsg("u1"), assistantMsg("a1"), userMsg("u2"), assistantMsg("a2")} {
		if err := m.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.leafID)
	}
	if err := m.AppendCompaction("early chat summarized", ids[2], 5000, nil, nil); err != nil {
		t.Fatal(err)
	}
	compactionID := m.leafID
	if err := m.AppendMessage(userMsg("u3")); err != nil {
		t.Fatal(err)
	}

	ctx := m.BuildContext()
	// 结构化条目保持来源：[摘要(compaction), u2, a2, u3]
	if len(ctx.Entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(ctx.Entries))
	}
	want := []string{compactionID, ids[2], ids[3], m.leafID}
	for i, id := range want {
		if ctx.Entries[i].SourceID != id {
			t.Errorf("Entries[%d].SourceID = %q, want %q", i, ctx.Entries[i].SourceID, id)
		}
	}
	if ctx.Entries[0].Summary != "early chat summarized" {
		t.Errorf("compaction summary = %q", ctx.Entries[0].Summary)
	}
}

func TestBuildContextStaleUsage(t *testing.T) {
	t.Run("assistant before compaction is stale", func(t *testing.T) {
		m := New("/project", t.TempDir())
		var ids []string
		for _, msg := range []llm.Message{userMsg("u1"), assistantMsg("a1")} {
			if err := m.AppendMessage(msg); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, m.leafID)
		}
		if err := m.AppendCompaction("summarized", ids[1], 5000, nil, nil); err != nil {
			t.Fatal(err)
		}
		// 压缩后只有 user 消息，最后一条 assistant 仍带着压缩前的 usage
		if err := m.AppendMessage(userMsg("u2")); err != nil {
			t.Fatal(err)
		}
		if !m.BuildContext().UsageStale {
			t.Error("UsageStale = false, want true (assistant predates compaction)")
		}
	})
	t.Run("assistant after compaction is fresh", func(t *testing.T) {
		m := New("/project", t.TempDir())
		var ids []string
		for _, msg := range []llm.Message{userMsg("u1"), assistantMsg("a1")} {
			if err := m.AppendMessage(msg); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, m.leafID)
		}
		if err := m.AppendCompaction("summarized", ids[1], 5000, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(assistantMsg("a2")); err != nil {
			t.Fatal(err)
		}
		if m.BuildContext().UsageStale {
			t.Error("UsageStale = true, want false (fresh assistant after compaction)")
		}
	})
	t.Run("no compaction never stale", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("u1")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendMessage(assistantMsg("a1")); err != nil {
			t.Fatal(err)
		}
		if m.BuildContext().UsageStale {
			t.Error("UsageStale = true, want false without compaction")
		}
	})
	t.Run("context edit invalidates earlier usage", func(t *testing.T) {
		m := New("/project", t.TempDir())
		if err := m.AppendMessage(userMsg("u1")); err != nil {
			t.Fatal(err)
		}
		userID := m.LastEntryID()
		if err := m.AppendMessage(assistantMsg("a1")); err != nil {
			t.Fatal(err)
		}
		if err := m.AppendContextEdit(userID, &Replacement{Content: []llm.Content{llm.TextContent{Text: "edited"}}}); err != nil {
			t.Fatal(err)
		}
		if !m.BuildContext().UsageStale {
			t.Error("UsageStale = false, want true after context edit")
		}
	})
}
