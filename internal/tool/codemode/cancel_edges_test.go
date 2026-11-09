package codemode

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestExitDuringOutputSerializationIsSuccessful(t *testing.T) {
	result := runCode(t, scriptContext(context.Background(), nil, nil), `
store("saved", true);
text("before");
try { text({toJSON() { exit(); }}); } catch (_) { text("caught"); }
text("after");`, scriptHistory{})
	if result.IsError || codeBody(t, result) != "before" {
		t.Fatalf("serialization exit failed or resumed: %+v", result)
	}
	writes := codeDetails(t, result).StoreWrites
	if writes == nil || string(writes.Set["saved"]) != "true" {
		t.Fatalf("successful exit lost store writes: %+v", writes)
	}
}

func TestCanceledQueuedCallsDoNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var effect atomic.Bool
	e := newExecution(ctx, llm.ToolContext{Runtime: llm.ToolRuntime{ExecuteNested: func(context.Context, llm.ToolCall) llm.ToolResult {
		effect.Store(true)
		return llm.ToolResult{}
	}}})
	defer e.release()
	e.calls.queue = []request{{call: llm.ToolCall{ID: "queued", Name: "mutate"}}}
	e.calls.records = []Call{{ID: "queued", Name: "mutate", Status: CallQueued}}
	e.calls.indices["queued"] = 0
	cancel()
	e.dispatch()
	e.stop()
	if effect.Load() || e.calls.records[0].Status != CallCancelled {
		t.Fatalf("canceled queue produced a side effect: effect=%v calls=%+v", effect.Load(), e.calls.records)
	}
}
