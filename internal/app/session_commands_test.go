package app

import (
	"context"
	"testing"

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
