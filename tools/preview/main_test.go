package main

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/tui"
)

func TestRunKeepsAbortedMarkerInSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &preview{send: func(tea.Msg) {}, scene: "working"}

	result, ok := p.run(ctx, "hello").(tui.OperationDoneMsg)
	if !ok || result.Snapshot == nil {
		t.Fatalf("run() result = %#v, want operation snapshot", result)
	}
	if len(result.Snapshot.Messages) != 2 || !result.Snapshot.Messages[1].Aborted {
		t.Fatalf("snapshot messages = %+v, want aborted assistant message", result.Snapshot.Messages)
	}
}
