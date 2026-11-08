package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/testutil"
	"github.com/ywxzm03/Rivo/internal/tool/codemode"
)

type emptyCodemodeHistory struct{}

func (emptyCodemodeHistory) BranchToolDetails(string) ([]json.RawMessage, error) { return nil, nil }

func TestCodemodeUsesCurrentToolContext(t *testing.T) {
	cwd := t.TempDir()
	var calls atomic.Int32
	var a *Agent
	probe := &testutil.Tool{ToolName: "probe", Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
		host, ok := llm.CurrentToolContext(ctx)
		if !ok || host.Config.SessionID != "current-session" || host.AgentID != "child-task" || !host.ForkChild || llm.CurrentWorkingDir(ctx) != cwd || llm.CurrentUserMessageID(ctx) != "user-entry" {
			return llm.ToolResult{}, fmt.Errorf("lost current tool context: %+v", host)
		}
		if !strings.HasPrefix(host.Runtime.CallID, "parent/") || host.Runtime.ExecuteNested != nil || host.Runtime.Update != nil || host.Runtime.EarlierResults != nil {
			return llm.ToolResult{}, fmt.Errorf("nested tool inherited parent capabilities: %+v", host.Runtime)
		}
		if calls.Add(1) == 1 {
			a.SetTools(nil)
		}
		return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "verified"}}}, nil
	}}
	var starts, ends int
	a = New(Options{Config: llm.AgentConfig{SessionID: "current-session"}, Context: llm.Context{Tools: []llm.Tool{codemode.New(emptyCodemodeHistory{}), probe}}, WorkingDir: cwd, AgentID: "child-task", ForkChild: true, Events: func(event llm.AgentEvent) error {
		switch event := event.(type) {
		case llm.ToolExecutionStartEvent:
			starts++
			if event.ToolCallID != "parent" {
				t.Errorf("nested start escaped: %+v", event)
			}
		case llm.ToolExecutionEndEvent:
			ends++
			if event.ToolCallID != "parent" {
				t.Errorf("nested end escaped: %+v", event)
			}
		}
		return nil
	}})
	ctx := llm.WithCurrentUserMessageID(context.Background(), "user-entry")
	results, err := a.executeToolBatch(ctx, []llm.ToolCall{{ID: "parent", Name: "codemode", Arguments: map[string]any{"code": `text((await tools.probe()).text);text((await tools.probe()).text);text(typeof tools.codemode);`}}})
	if err != nil || len(results) != 1 || results[0].IsError || calls.Load() != 2 || starts != 1 || ends != 1 {
		t.Fatalf("results=%+v err=%v calls=%d starts=%d ends=%d", results, err, calls.Load(), starts, ends)
	}
	if !strings.Contains(llm.TextOf(results[0].Content), "verified\nverified\nundefined") {
		t.Fatalf("nested output = %s", llm.TextOf(results[0].Content))
	}
}

func TestCodemodeStoreVisibleWithinBatch(t *testing.T) {
	for _, failed := range []bool{false, true} {
		a := New(Options{Config: llm.AgentConfig{SessionID: "same-session"}, Context: llm.Context{Tools: []llm.Tool{codemode.New(emptyCodemodeHistory{})}}})
		first := `store("key",{ok:true});`
		want := `{"ok":true}`
		if failed {
			first += `throw new Error("rollback");`
			want = "true"
		}
		second := `text(load("key"));`
		if failed {
			second = `text(load("key")===undefined);`
		}
		results, err := a.executeToolBatch(context.Background(), []llm.ToolCall{
			{ID: "one", Name: "codemode", Arguments: map[string]any{"code": first}},
			{ID: "two", Name: "codemode", Arguments: map[string]any{"code": second}},
		})
		if err != nil || len(results) != 2 || results[0].IsError != failed || results[1].IsError || !strings.HasSuffix(llm.TextOf(results[1].Content), want) {
			t.Fatalf("failed=%v results=%+v err=%v", failed, results, err)
		}
	}
}

func TestCodemodeUpdateSinkFailure(t *testing.T) {
	sentinel := errors.New("update consumer failed")
	var notifications int
	var nextOuter atomic.Bool
	probe := &testutil.Tool{ToolName: "probe", Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
		host, ok := llm.CurrentToolContext(ctx)
		if !ok {
			return llm.ToolResult{}, errors.New("missing context")
		}
		if host.Runtime.CallID == "after" {
			nextOuter.Store(true)
		}
		return llm.ToolResult{}, nil
	}}
	a := New(Options{Config: llm.AgentConfig{SessionID: "sink-session"}, Context: llm.Context{Tools: []llm.Tool{codemode.New(emptyCodemodeHistory{}), probe}}, Events: func(event llm.AgentEvent) error {
		switch event.(type) {
		case llm.ToolExecutionUpdateEvent:
			return sentinel
		case llm.MessageStartEvent, llm.MessageEndEvent:
			notifications++
		}
		return nil
	}})
	results, err := a.executeToolBatch(context.Background(), []llm.ToolCall{
		{ID: "parent", Name: "codemode", Arguments: map[string]any{"code": `text("before");store("key",1);try{await tools.probe();}catch(e){text("caught");}text("after");`}},
		{ID: "after", Name: "probe", Arguments: map[string]any{}},
	})
	if !errors.Is(err, sentinel) || len(results) != 2 || !results[0].IsError || !results[1].IsError || nextOuter.Load() || notifications != 0 {
		t.Fatalf("event failure lost: results=%+v err=%v next=%v notifications=%d", results, err, nextOuter.Load(), notifications)
	}
	details, ok := results[0].Details.(codemode.Details)
	if !ok || details.StoreWrites != nil || !strings.Contains(llm.TextOf(results[0].Content), "before") || strings.Contains(llm.TextOf(results[0].Content), "caught") {
		t.Fatalf("partial failure overwritten or state committed: %+v", results[0])
	}
}

func TestCodemodeRuntimeParallelSinkFailure(t *testing.T) {
	sentinel := errors.New("parallel update failed")
	entered := make(chan struct{})
	var exited atomic.Bool
	updater := &testutil.Tool{ToolName: "updater", Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
		<-entered
		host, ok := llm.CurrentToolContext(ctx)
		if !ok {
			return llm.ToolResult{}, errors.New("missing context")
		}
		err := host.Runtime.Update(llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "partial"}}})
		if !errors.Is(err, sentinel) {
			return llm.ToolResult{}, fmt.Errorf("wrong Update error: %v", err)
		}
		return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "partial"}}}, nil
	}}
	waiter := &testutil.Tool{ToolName: "waiter", Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
		close(entered)
		<-ctx.Done()
		exited.Store(true)
		return llm.ToolResult{}, ctx.Err()
	}}
	a := New(Options{Context: llm.Context{Tools: []llm.Tool{updater, waiter}}, Events: func(event llm.AgentEvent) error {
		if _, ok := event.(llm.ToolExecutionUpdateEvent); ok {
			return sentinel
		}
		return nil
	}})
	results, err := a.executeToolBatch(context.Background(), []llm.ToolCall{{ID: "updater", Name: "updater"}, {ID: "waiter", Name: "waiter"}})
	if !errors.Is(err, sentinel) || !exited.Load() || len(results) != 2 || !results[0].IsError || !results[1].IsError || llm.TextOf(results[0].Content) != "partial" {
		t.Fatalf("parallel event failure lost: results=%+v err=%v exited=%v", results, err, exited.Load())
	}
}

func TestCanceledNestedToolDoesNotProduceEffects(t *testing.T) {
	var effect atomic.Bool
	probe := &testutil.Tool{ToolName: "mutate", Run: func(context.Context, map[string]any) (llm.ToolResult, error) {
		effect.Store(true)
		return llm.ToolResult{}, nil
	}}
	executor := toolExecutor{context: llm.ToolContext{Context: llm.Context{Tools: []llm.Tool{probe}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := executor.nested(ctx, llm.ToolCall{ID: "canceled", Name: "mutate"})
	if !result.IsError || effect.Load() {
		t.Fatalf("canceled native call produced an effect: result=%+v effect=%v", result, effect.Load())
	}
}
