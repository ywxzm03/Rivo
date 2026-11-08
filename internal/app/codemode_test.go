package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/provider"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tool/codemode"
)

type codemodeModel struct {
	code           string
	awaitingResult bool
	calls          int
}

func (model *codemodeModel) stream(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
	model.calls++
	message := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, StopReason: llm.StopReasonStop}
	if !model.awaitingResult {
		message = &llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "script", Name: "codemode", Arguments: map[string]any{"code": model.code}}}, StopReason: llm.StopReasonToolUse}
	}
	model.awaitingResult = !model.awaitingResult
	events := make(chan llm.AssistantMessageEvent, 1)
	events <- llm.AssistantMessageDoneEvent{Message: message}
	close(events)
	return events, nil
}

func newCodemodeApp(t *testing.T, model *codemodeModel, sink llm.AgentEventSink) (*App, provider.Config) {
	t.Helper()
	setupPromptEnv(t)
	cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
	cfg = cfg.WithStreamForTest(deepseek.ProviderID, cfg.AvailableModels(), model.stream)
	a, err := NewWithSessionDir(cfg, t.TempDir(), sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, cfg
}

func runCodemodeApp(t *testing.T, a *App, model *codemodeModel, code string) llm.ToolResultMessage {
	t.Helper()
	model.code = code
	if _, err := a.Run(context.Background(), "run script"); err != nil {
		t.Fatal(err)
	}
	messages := a.session.BuildContext().Messages()
	for i := len(messages) - 1; i >= 0; i-- {
		if result, ok := messages[i].(llm.ToolResultMessage); ok && result.ToolName == "codemode" {
			return result
		}
	}
	t.Fatal("missing persisted codemode result")
	return llm.ToolResultMessage{}
}

func assertCodemodeAppValue(t *testing.T, a *App, model *codemodeModel, want string) {
	t.Helper()
	result := runCodemodeApp(t, a, model, `text(load("value") === undefined ? "missing" : load("value"));`)
	if result.IsError || len(result.Content) < 2 {
		t.Fatalf("load failed: %+v", result)
	}
	if got := llm.TextOf(result.Content[1:]); got != want {
		t.Fatalf("store value = %q, want %q", got, want)
	}
}

func TestCodemodeAppAssemblyResolvesEnabled(t *testing.T) {
	for _, enabled := range []*bool{nil, new(true), new(false)} {
		setupPromptEnv(t)
		cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
		cfg.Codemode.Enabled = enabled
		a, err := NewWithSessionDir(cfg, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Close)
		counts := make(map[string]int)
		for _, candidate := range a.agent.Context().Tools {
			counts[candidate.Name()]++
		}
		want := 1
		if enabled != nil && !*enabled {
			want = 0
		}
		if counts["codemode"] != want || counts["read"] != 1 || counts["write"] != 1 || counts["edit"] != 1 {
			t.Fatalf("enabled=%v: assembled tools = %v", enabled, counts)
		}
	}
}

func TestCodemodeStoreBranchAndCompaction(t *testing.T) {
	model := &codemodeModel{}
	a, cfg := newCodemodeApp(t, model, nil)
	runCodemodeApp(t, a, model, `store("value", "ancestor");`)
	var ancestor string
	for _, entry := range a.session.Tree().Entries {
		if entry.Role == "toolResult" {
			ancestor = entry.ID
		}
	}
	assertCodemodeAppValue(t, a, model, "ancestor")
	if err := a.session.AppendCompaction("compressed history", "", 100, nil, nil); err != nil {
		t.Fatal(err)
	}
	a.agent.ReplaceMessages(a.session.BuildContext().Messages())
	assertCodemodeAppValue(t, a, model, "ancestor")
	result := runCodemodeApp(t, a, model, `store("value", "main");`)
	if result.IsError {
		t.Fatalf("store failed: %+v", result)
	}
	mainLeaf := a.session.LeafID()
	var updateUser string
	for _, entry := range a.session.Tree().Entries {
		if entry.Role == "user" {
			updateUser = entry.ID
		}
	}
	if _, err := a.session.RewindUser(updateUser); err != nil {
		t.Fatal(err)
	}
	a.agent.ReplaceMessages(a.session.BuildContext().Messages())
	assertCodemodeAppValue(t, a, model, "ancestor")
	runCodemodeApp(t, a, model, `store("value", "side");`)
	assertCodemodeAppValue(t, a, model, "side")
	if err := a.session.AppendContextEdit(ancestor, nil); err != nil {
		t.Fatal(err)
	}
	a.agent.ReplaceMessages(a.session.BuildContext().Messages())
	assertCodemodeAppValue(t, a, model, "side")
	if err := a.session.Branch(mainLeaf); err != nil {
		t.Fatal(err)
	}
	a.agent.ReplaceMessages(a.session.BuildContext().Messages())
	assertCodemodeAppValue(t, a, model, "main")
	reopened, err := Open(a.session.Path(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	assertCodemodeAppValue(t, reopened, model, "main")
	op, err := reopened.Start(context.Background(), OperationRequest{Kind: OperationClear})
	if err != nil {
		t.Fatal(err)
	}
	if result := op.Wait(); result.Err != nil {
		t.Fatal(result.Err)
	}
	assertCodemodeAppValue(t, reopened, model, "missing")
	runCodemodeApp(t, reopened, model, `store("value", "fresh");`)
	fresh, err := Open(reopened.session.Path(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.Close)
	assertCodemodeAppValue(t, fresh, model, "fresh")
}

func TestCodemodeFailedScriptDoesNotPersistWrites(t *testing.T) {
	model := &codemodeModel{}
	a, cfg := newCodemodeApp(t, model, nil)
	runCodemodeApp(t, a, model, `store("value", "committed");`)
	result := runCodemodeApp(t, a, model, `store("value", "discarded"); text("before failure"); throw new Error("intentional");`)
	details, ok := result.Details.(codemode.Details)
	if !ok || !result.IsError || details.StoreWrites != nil || details.Error == "" {
		t.Fatalf("failure persisted proposed writes: %+v", result)
	}
	if len(result.Content) < 3 || llm.TextOf(result.Content[1:len(result.Content)-1]) != "before failure" {
		t.Fatalf("failure lost earlier script output: %+v", result.Content)
	}
	reopened, err := Open(a.session.Path(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	assertCodemodeAppValue(t, reopened, model, "committed")
}

func TestCodemodeCommitFailureDoesNotAdvanceStore(t *testing.T) {
	model := &codemodeModel{}
	var a *App
	var restore func()
	blockCommit := false
	sink := func(event llm.AgentEvent) error {
		if end, ok := event.(llm.ToolExecutionEndEvent); ok && end.ToolName == "codemode" && blockCommit {
			blockCommit = false
			restore = blockSessionWrites(t, a)
		}
		return nil
	}
	a, _ = newCodemodeApp(t, model, sink)
	runCodemodeApp(t, a, model, `store("value", "before");`)
	path := filepath.Join(t.TempDir(), "effect.txt")
	encodedPath, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	model.code = `await tools.write({path:` + string(encodedPath) + `,content:"executed"}); store("value", "after");`
	blockCommit = true
	_, err = a.Run(context.Background(), "run script")
	var commitErr *session.CommitError
	if !errors.As(err, &commitErr) || !a.session.HasPending() || restore == nil {
		t.Fatalf("result commit did not fail with retained pending data: %v", err)
	}
	defer func() {
		if restore != nil {
			restore()
		}
	}()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "executed" {
		t.Fatalf("completed real write was lost: %q, %v", data, err)
	}
	raw, err := a.session.BranchToolDetails("codemode")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("uncommitted state entered branch history: %s", raw)
	}
	var committed codemode.Details
	if err := json.Unmarshal(raw[0], &committed); err != nil {
		t.Fatal(err)
	}
	if committed.StoreWrites == nil || string(committed.StoreWrites.Set["value"]) != `"before"` {
		t.Fatalf("store advanced before result commit: %+v", committed)
	}
	pending := a.session.PendingMessages()
	if len(pending) != 1 {
		t.Fatalf("pending = %v, want only completed parent result", pending)
	}
	pendingResult, ok := pending[0].(llm.ToolResultMessage)
	if !ok || pendingResult.ToolName != "codemode" || pendingResult.IsError {
		t.Fatalf("pending result = %+v", pending[0])
	}
	// 修改真实副作用作为重跑探针；补交不能再次执行 write
	if err := os.WriteFile(path, []byte("changed after failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	callsBeforeRetry := model.calls
	restore()
	restore = nil
	if _, err := a.Run(context.Background(), "run script"); err != nil {
		t.Fatal(err)
	}
	if a.session.HasPending() || model.calls != callsBeforeRetry+1 {
		t.Fatalf("retry re-executed script or did not finish: pending=%v model calls=%d", a.session.HasPending(), model.calls)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "changed after failure" {
		t.Fatalf("retry re-ran real write: %q, %v", data, err)
	}
	assertCodemodeAppValue(t, a, model, "after")
	reopened, err := Open(a.session.Path(), a.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	assertCodemodeAppValue(t, reopened, model, "after")
}
