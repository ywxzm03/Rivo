package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestRunCreatesFileHistoryCheckpointBeforeModelCall(t *testing.T) {
	callStarted := make(chan struct{})
	a := operationApp(t, func(ctx context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		channel := make(chan llm.AssistantMessageEvent, 1)
		channel <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "reply"}}}}
		close(channel)
		close(callStarted)
		return channel, nil
	}, nil)
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err != nil {
		t.Fatal(result.Err)
	}
	<-callStarted
	entries := a.Tree().Entries
	var userID string
	for _, entry := range entries {
		if entry.Role == "user" {
			userID = entry.ID
			break
		}
	}
	if userID == "" {
		t.Fatalf("tree entries = %+v, want user entry", entries)
	}
	snapshots := a.session.FileHistory().Snapshots()
	if len(snapshots) != 1 || snapshots[0].MessageID != userID {
		t.Fatalf("snapshots = %+v, want checkpoint for %q", snapshots, userID)
	}
}

func TestTreeCodeRestoreKeepsConversationLeaf(t *testing.T) {
	a := operationApp(t, func(ctx context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		channel := make(chan llm.AssistantMessageEvent)
		close(channel)
		return channel, nil
	}, nil)
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "edit"}}}); err != nil {
		t.Fatal(err)
	}
	userID := a.session.LeafID()
	if err := a.session.MakeFileHistorySnapshot(userID); err != nil {
		t.Fatal(err)
	}
	if err := a.session.FileHistory().TrackEdit(context.Background(), userID, file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationTree, Value: userID, RestoreMode: filehistory.RestoreCode})
	if err != nil {
		t.Fatal(err)
	}
	result := op.Wait()
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.ContextRewound {
		t.Fatal("code-only restore moved conversation context")
	}
	if got := a.session.LeafID(); got != userID {
		t.Fatalf("leaf = %q, want %q", got, userID)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before" {
		t.Fatalf("content = %q, want before", content)
	}
	if len(result.FileRestore.FilesChanged) != 1 {
		t.Fatalf("file restore = %+v, want one changed file", result.FileRestore)
	}
}

func TestTreeCodeAndContextRestoreDoesNotMoveContextOnFileFailure(t *testing.T) {
	a := operationApp(t, func(ctx context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		channel := make(chan llm.AssistantMessageEvent)
		close(channel)
		return channel, nil
	}, nil)
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.session.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "edit"}}}); err != nil {
		t.Fatal(err)
	}
	userID := a.session.LeafID()
	if err := a.session.MakeFileHistorySnapshot(userID); err != nil {
		t.Fatal(err)
	}
	if err := a.session.FileHistory().TrackEdit(context.Background(), userID, file); err != nil {
		t.Fatal(err)
	}
	backup := a.session.FileHistory().Snapshots()[0].TrackedFileBackups[file].BackupFileName
	if backup == nil {
		t.Fatal("snapshot did not contain a backup")
	}
	backupPath := filepath.Join(os.Getenv("RIVO_AGENT_DIR"), "file-history", a.SessionID(), *backup)
	if err := os.Remove(backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationTree, Value: userID, RestoreMode: filehistory.RestoreCodeAndContext})
	if err != nil {
		t.Fatal(err)
	}
	result := op.Wait()
	if result.Err == nil {
		t.Fatal("combined restore succeeded with missing backup")
	}
	if result.ContextRewound || a.session.LeafID() != userID {
		t.Fatalf("context moved after code failure: result=%+v leaf=%q", result, a.session.LeafID())
	}
}
