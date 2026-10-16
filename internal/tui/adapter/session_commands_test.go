package adapter

import (
	"os"
	"testing"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestSessionCommandsPublishNameAndReplaceRuntime(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := session.DefaultDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	sm := session.New(cwd, dir)
	if err := sm.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "seed"}}}); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(sm.Path(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "")
	r.Attach(a)
	done := r.request(tui.Request{Kind: "rename", Value: "Named Thread"})().(tui.OperationDoneMsg)
	if done.Err != nil || done.Notice != "Session renamed: Named Thread" || done.Snapshot.SessionName != "Named Thread" {
		t.Fatalf("rename completion = %+v", done)
	}
	if len(done.Snapshot.Sessions) != 1 || done.Snapshot.Sessions[0].Summary != "Named Thread" {
		t.Fatal("session selector did not use the persisted name")
	}
	old := r.namespace
	done = r.request(tui.Request{Kind: "new"})().(tui.OperationDoneMsg)
	if done.Err != nil || !done.ResetSessionUI || done.RetainMessages || r.app == a || r.namespace == old || done.Snapshot.SessionName != "" || len(done.Snapshot.Messages) != 0 {
		t.Fatalf("new completion = %+v", done)
	}
}

func TestClearAndDeleteSessionCommands(t *testing.T) {
	setupAdapterEnv(t)
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "")
	r.Attach(a)
	clear := r.request(tui.Request{ID: 1, Kind: "clear"})().(tui.OperationDoneMsg)
	if clear.Err != nil || clear.Snapshot == nil || len(clear.Snapshot.Messages) != 0 {
		t.Fatalf("clear completion = %+v", clear)
	}
	delete := r.request(tui.Request{ID: 2, Kind: "delete"})().(tui.OperationDoneMsg)
	if delete.Err != nil || !delete.Quit {
		t.Fatalf("delete completion = %+v", delete)
	}
}
