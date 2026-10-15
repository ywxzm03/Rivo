package adapter

import (
	"os"
	"testing"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestSessionCommandsPublishNameAndReplaceRuntime(t *testing.T) {
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
