package adapter

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestRepeatedTextKeepsSourceMetadataAfterProjectionChanges(t *testing.T) {
	r := &Runtime{namespace: "session"}
	view := testContext([]llm.Message{
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "same"}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "same"}}},
	})
	r.replaceMessagesLocked(view)
	r.messages[0].Divider = &tui.TurnDividerView{Elapsed: 2 * time.Second}
	r.messages[1].Divider = &tui.TurnDividerView{Elapsed: 5 * time.Second}
	id := r.messages[1].ID
	view.Entries = view.Entries[1:]
	r.replaceMessagesLocked(view)
	if len(r.messages) != 1 || r.messages[0].ID != id || r.messages[0].Divider.Elapsed != 5*time.Second {
		t.Fatalf("source metadata drifted: %+v", r.messages)
	}
}

func TestMessageEndBindsLiveIdentityToCommittedSource(t *testing.T) {
	var sent []tea.Msg
	r := &Runtime{namespace: "session", userID: "session/live/1/user", send: func(msg tea.Msg) { sent = append(sent, msg) }}
	message := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "same"}}}
	_ = r.handleEvent(llm.MessageStartEvent{Message: message})
	_ = r.handleEvent(llm.MessageEndEvent{Message: message, SourceID: "entry-a"})
	if len(r.messages) != 1 || r.messages[0].ID != "session/entry/entry-a/message" {
		t.Fatalf("live source binding = %+v", r.messages)
	}
	if msg, ok := sent[len(sent)-1].(tui.MessageCommittedMsg); !ok || msg.PreviousID != "session/live/1/user" {
		t.Fatalf("identity notification = %+v", sent)
	}
}

func TestSteeringUserMessagesGetDistinctLiveIdentities(t *testing.T) {
	r := &Runtime{namespace: "session", sequence: 3, userID: "session/live/3/user"}
	first := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}}
	second := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "second"}}}
	r.handleEvent(llm.MessageStartEvent{Message: first})
	r.handleEvent(llm.MessageEndEvent{Message: first, SourceID: "entry-1"})
	r.handleEvent(llm.MessageStartEvent{Message: second})
	r.handleEvent(llm.MessageEndEvent{Message: second, SourceID: "entry-2"})
	if len(r.messages) != 2 || r.messages[0].ID == r.messages[1].ID {
		t.Fatalf("steering user identities = %+v", r.messages)
	}
}

func TestCompactionVisibilityUsesContextKindAndSessionNamespace(t *testing.T) {
	text := "The conversation history before this point was compacted into a summary."
	view := testContext([]llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}}})
	one, two := &Runtime{namespace: "one"}, &Runtime{namespace: "two"}
	first, second := one.projectContext(view), two.projectContext(view)
	if len(first) != 1 || first[0].Role != "user" || first[0].Text != text || first[0].ID == second[0].ID {
		t.Fatal("ordinary text or session namespace was lost")
	}
	view.Entries[0].Kind = ctxview.ContextCompaction
	if len(one.projectContext(view)) != 0 || len(two.projectContext(view)) != 0 {
		t.Fatal("compaction entry appeared in the transcript")
	}
}

func TestAgentEndSendsOnlyChangedBlocksWithLongHistory(t *testing.T) {
	var sent []tea.Msg
	r := &Runtime{turnStarted: time.Now().Add(-2 * time.Second), send: func(msg tea.Msg) { sent = append(sent, msg) }}
	for i := 0; i < 10000; i++ {
		r.upsertLocked(tui.Message{ID: fmt.Sprint(i), Role: "assistant", ToolGroup: &tui.ToolGroupView{}})
	}
	r.upsertLocked(tui.Message{ID: "final", Role: "assistant", Text: "answer"})
	_ = r.handleEvent(llm.AgentEndEvent{})
	if len(sent) != 1 {
		t.Fatalf("completion sent %d history blocks, want one changed divider", len(sent))
	}
}

func TestSnapshotIncludesAllConfiguredModels(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{app: a, cfg: cfg, cwd: t.TempDir()}
	snapshot := r.snapshotLocked(false)
	if len(snapshot.Models) != len(cfg.AvailableModels()) {
		t.Fatalf("snapshot models = %d, want %d", len(snapshot.Models), len(cfg.AvailableModels()))
	}
	got := make(map[string]bool, len(snapshot.Models))
	for _, model := range snapshot.Models {
		got[model.Key()] = true
	}
	for _, model := range cfg.AvailableModels() {
		if !got[model.Provider+"/"+model.ID] {
			t.Fatalf("snapshot omitted configured model %s/%s", model.Provider, model.ID)
		}
	}
}
