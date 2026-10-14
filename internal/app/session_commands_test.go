package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

func TestRenameOperationRetriesWithoutDuplicatingMetadata(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendSessionInfo("Original"); err != nil {
		t.Fatal(err)
	}
	restore := blockSessionWrites(t, a)
	req := OperationRequest{Kind: OperationRename, Value: "Updated Thread"}
	op, err := a.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if op.Wait().Err == nil || a.SessionName() != "Original" {
		t.Fatal("failed rename changed the committed name")
	}
	restore()
	op, err = a.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err != nil || !result.Replayed || a.SessionName() != "Updated Thread" {
		t.Fatalf("rename retry = %+v, name = %q", result, a.SessionName())
	}
	loaded, err := session.Open(a.session.Path(), a.cwd)
	if err != nil || loaded.Name() != "Updated Thread" || loaded.LastEntryID() != a.session.LastEntryID() {
		t.Fatal("rename retry diverged from persisted session", err)
	}
}

func TestNewSessionInheritsSettingsWithoutOldContext(t *testing.T) {
	a := operationApp(t, nil, nil)
	for _, model := range a.AvailableModels() {
		if model.ID != a.CurrentModel().ID {
			if err := a.SetModel(model.Provider, model.ID); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if err := a.SetThinkingLevel(llm.ThinkingLevelHigh); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendMessage(textMsg("old conversation")); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendSessionInfo("Old Thread"); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(a.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	// 新实例必须重新读盘，而不是仅清空旧 Agent 的消息
	writePromptFile(t, filepath.Join(os.Getenv("RIVO_AGENT_DIR"), "AGENTS.md"), "New session instructions")
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationNew})
	if err != nil {
		t.Fatal(err)
	}
	result := op.Wait()
	next := result.Next
	if result.Err != nil || next == nil {
		t.Fatalf("new session = %+v", result)
	}
	if next.SessionID() == a.SessionID() || next.SessionName() != "" || len(next.ContextView().Messages()) != 0 {
		t.Fatal("new session retained old identity, name or context")
	}
	if next.CurrentModel().ID != a.CurrentModel().ID || next.CurrentThinkingLevel() != llm.ThinkingLevelHigh || filepath.Dir(next.session.Path()) != filepath.Dir(a.session.Path()) {
		t.Fatal("new session lost its settings or storage directory")
	}
	if files := next.loader.ContextFiles(); len(files) == 0 || files[0].Content != "New session instructions" {
		t.Fatal("new session did not reload context files")
	}
	if _, err := os.Stat(next.session.Path()); !os.IsNotExist(err) {
		t.Fatal("new session was published before naming or conversation")
	}
	if err := next.session.AppendSessionInfo("New Thread"); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Open(next.session.Path(), a.cwd)
	if err != nil || loaded.BuildContext().Model != next.CurrentModel().ID || loaded.BuildContext().ThinkingLevel != "high" {
		t.Fatal("inherited settings were not persisted", err)
	}
	after, err := os.ReadFile(a.session.Path())
	if err != nil || !reflect.DeepEqual(old, after) {
		t.Fatal("new session modified the old session", err)
	}
}

func TestNewSessionFailureKeepsOldApp(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendSessionInfo("Old Thread"); err != nil {
		t.Fatal(err)
	}
	id := a.SessionID()
	spec := a.cfg.registry[a.cfg.ProviderID]
	spec.new = func(string, string) ([]llm.Model, llm.StreamFn) { return nil, nil }
	a.cfg.registry[a.cfg.ProviderID] = spec
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationNew})
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err == nil || result.Next != nil || a.SessionID() != id || a.SessionName() != "Old Thread" {
		t.Fatalf("failed switch = %+v", result)
	}
}

func TestNewSessionStopsWhenPendingCommitFails(t *testing.T) {
	a := operationApp(t, nil, nil)
	if err := a.session.AppendSessionInfo("Old Thread"); err != nil {
		t.Fatal(err)
	}
	restore := blockSessionWrites(t, a)
	t.Cleanup(restore)
	if err := a.session.AppendMessage(textMsg("pending")); err == nil {
		t.Fatal("want pending write failure")
	}
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationNew})
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err == nil || result.Next != nil || !a.session.HasPending() {
		t.Fatalf("switch with pending failure = %+v", result)
	}
}

func TestReloadOperationUpdatesBaselineWithoutWritingContext(t *testing.T) {
	a := operationApp(t, nil, nil)
	before := a.ContextView()
	writePromptFile(t, filepath.Join(os.Getenv("RIVO_AGENT_DIR"), "AGENTS.md"), "Reloaded instructions")
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationReload})
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err != nil || !reflect.DeepEqual(before, a.ContextView()) {
		t.Fatalf("reload changed persisted context: %+v", result)
	}
	if files := a.loader.ContextFiles(); len(files) == 0 || files[0].Content != "Reloaded instructions" {
		t.Fatal("reload did not update loaded files")
	}
}
