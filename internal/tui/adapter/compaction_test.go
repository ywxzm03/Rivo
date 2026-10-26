package adapter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// TestManualCompactionHidesPromptAfterCompletionAndResume 验证压缩显示与模型上下文各自的边界
func TestManualCompactionHidesPromptAfterCompletionAndResume(t *testing.T) {
	setupAdapterEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"summary","status":"completed","output":[{"type":"message","id":"summary-text","role":"assistant","status":"completed","content":[{"type":"output_text","text":"internal compaction summary","annotations":[]}]}]}}`+"\n\n")
	}))
	defer server.Close()
	agentDir := os.Getenv("RIVO_AGENT_DIR")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := fmt.Sprintf(`{"providers":{"fixture":{"baseUrl":%q,"apiKey":"test-key","models":[{"id":"fixture","contextWindow":1000000,"maxTokens":1024}]}}}`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"defaultProvider":"fixture","defaultModel":"fixture"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}

	// 1.尾部超过默认保留预算，确保真实压缩调用存在可摘要的历史区间
	cwd := t.TempDir()
	sm := session.New(cwd, t.TempDir())
	kept := strings.Repeat("kept ", compaction.DefaultKeepRecentTokens)
	ids, err := sm.AppendMessages([]llm.Message{
		llm.SystemMessage{Sections: map[string]string{"preamble": "system snapshot"}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "old question"}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "old answer"}}, StopReason: llm.StopReasonStop},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: kept}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "kept answer"}}, StopReason: llm.StopReasonStop},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := New(cfg, cwd, "main")
	a, err := app.Open(sm.Path(), cfg, runtime.HandleEvent)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Attach(a)
	model := tui.New(runtime.Snapshot(), tui.Handlers{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	result := runtime.Request(tui.Request{Kind: "compact"})().(tui.OperationDoneMsg)
	if result.Err != nil || result.Snapshot == nil || result.RetainMessages {
		t.Fatalf("compact completion = %+v", result)
	}
	model.Update(result)

	// 2.完成与恢复使用同一显示投影，保留消息的来源身份不能改变
	for _, snapshot := range []tui.Snapshot{*result.Snapshot, runtime.Snapshot()} {
		if len(snapshot.Messages) != 2 || snapshot.Messages[0].Role != "user" || snapshot.Messages[0].Text != kept ||
			snapshot.Messages[0].ID != runtime.sourceID(ids[3], "message") || snapshot.Messages[1].Text != "kept answer" {
			t.Fatalf("compacted transcript has %d messages; want the retained user and assistant with source IDs", len(snapshot.Messages))
		}
	}
	view := ansi.Strip(model.View())
	if strings.Contains(view, "<summary>") || strings.Contains(view, "internal compaction summary") || !strings.Contains(view, "kept answer") {
		t.Fatalf("compacted frame = %q", view)
	}
	if !strings.Contains(view, "Context compacted (manual)") {
		t.Fatalf("compaction completion was not rendered: %q", view)
	}
	resumed := New(cfg, cwd, "main")
	reopened, err := app.Open(sm.Path(), cfg, resumed.HandleEvent)
	if err != nil {
		t.Fatal(err)
	}
	resumed.Attach(reopened)
	if snapshot := resumed.Snapshot(); len(snapshot.Messages) != 2 || snapshot.Messages[0].Text != kept || snapshot.Messages[1].Text != "kept answer" {
		t.Fatalf("resumed transcript has %d messages; want the retained user and assistant", len(snapshot.Messages))
	}

	// 3.摘要与 system 快照仍经磁盘恢复进入模型上下文
	for _, view := range []ctxview.ContextView{a.ContextView(), reopened.ContextView()} {
		if len(view.Entries) != 3 || view.Entries[0].Kind != ctxview.ContextCompaction || view.Entries[0].Summary != "internal compaction summary" {
			t.Fatalf("model context lost compaction entry: %d entries", len(view.Entries))
		}
		messages := view.Messages()
		if len(messages) != 4 || messages[0].Role() != llm.RoleSystem || textContent(messages[1].(llm.UserMessage).Content) != ctxview.WrappedSummary("internal compaction summary") {
			t.Fatalf("model context lost summary or snapshot: %d messages", len(messages))
		}
	}
}
