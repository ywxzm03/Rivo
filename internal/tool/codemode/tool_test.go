package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/testutil"
)

type scriptHistory struct {
	entries []json.RawMessage
	err     error
}

func (h scriptHistory) BranchToolDetails(string) ([]json.RawMessage, error) {
	return h.entries, h.err
}

// scriptContext 提供真实脚本执行所需的工具边界，不启动模型流
func scriptContext(ctx context.Context, tools []llm.Tool, update func(llm.ToolResult) error) context.Context {
	if update == nil {
		update = func(llm.ToolResult) error { return nil }
	}
	host := llm.ToolContext{
		Config:  llm.AgentConfig{SessionID: "script-session", Model: llm.Model{API: llm.APIOpenAIResponses, SupportsImages: true}},
		Context: llm.Context{Tools: tools},
		Runtime: llm.ToolRuntime{
			CallID: "script",
			Update: update,
			ExecuteNested: func(ctx context.Context, call llm.ToolCall) llm.ToolResult {
				for _, tool := range tools {
					if tool.Name() != call.Name {
						continue
					}
					result, err := tool.Execute(ctx, call.Arguments)
					if err != nil {
						result.IsError = true
						result.Content = []llm.Content{llm.TextContent{Text: err.Error()}}
					}
					return result
				}
				return llm.ToolResult{IsError: true, Content: []llm.Content{llm.TextContent{Text: "unavailable tool"}}}
			},
		},
	}
	return llm.WithToolContext(ctx, host)
}

func runCode(t *testing.T, ctx context.Context, code string, history scriptHistory) llm.ToolResult {
	t.Helper()
	result, err := New(history).Execute(ctx, map[string]any{"code": code})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

func codeDetails(t *testing.T, result llm.ToolResult) Details {
	t.Helper()
	details, ok := result.Details.(Details)
	if !ok {
		t.Fatalf("details = %T, want codemode.Details", result.Details)
	}
	return details
}

func codeBody(t *testing.T, result llm.ToolResult) string {
	t.Helper()
	details := codeDetails(t, result)
	end := len(result.Content)
	if details.Error != "" {
		end--
	}
	if end < 1 {
		t.Fatalf("missing status block: %+v", result)
	}
	return llm.TextOf(result.Content[1:end])
}

func TestAwaitAndAllSettled(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	makeTool := func(name string, failed bool) llm.Tool {
		return &testutil.Tool{ToolName: name, Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
			entered <- name
			select {
			case <-release:
			case <-ctx.Done():
				return llm.ToolResult{}, ctx.Err()
			}
			return llm.ToolResult{IsError: failed, Content: []llm.Content{llm.TextContent{Text: name + "-private"}}}, nil
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func(runCtx context.Context) {
		for range 2 {
			select {
			case <-entered:
			case <-runCtx.Done():
				return
			}
		}
		close(release)
	}(ctx)
	ctx = scriptContext(ctx, []llm.Tool{makeTool("first", false), makeTool("second", true)}, nil)
	result := runCode(t, ctx, `const r = await Promise.allSettled([tools.first({}), tools.second({})]);
text(r.map(x => x.status));
text(r[1].reason instanceof Error);
text(r[1].reason.result.text);`, scriptHistory{})
	if result.IsError {
		t.Fatalf("allSettled failed: %s", llm.TextOf(result.Content))
	}
	if body := codeBody(t, result); body != "[\"fulfilled\",\"rejected\"]\ntrue\nsecond-private" {
		t.Fatalf("selected output = %q", body)
	}
	details := codeDetails(t, result)
	if len(details.Calls) != 2 || details.Calls[0].Status != CallOK || details.Calls[1].Status != CallError {
		t.Fatalf("calls = %+v", details.Calls)
	}
}

func TestSequentialBarrier(t *testing.T) {
	entered := make(chan string, 4)
	releaseParallel := make(chan struct{})
	releaseExclusive := make(chan struct{})
	var bad atomic.Bool
	update := func(result llm.ToolResult) error {
		details, ok := result.Details.(Details)
		if !ok {
			return errors.New("missing progress details")
		}
		states := make(map[string]CallStatus)
		for _, call := range details.Calls {
			states[call.Name] = call.Status
		}
		if states["c"] == CallRunning && (states["a"] != CallOK || states["b"] != CallOK) {
			bad.Store(true)
		}
		if states["d"] == CallRunning && states["c"] != CallOK {
			bad.Store(true)
		}
		return nil
	}
	makeTool := func(name string, mode llm.ToolExecutionMode, release <-chan struct{}) llm.Tool {
		return &testutil.Tool{ToolName: name, Mode: mode, Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
			entered <- name
			if release != nil {
				select {
				case <-release:
				case <-ctx.Done():
					return llm.ToolResult{}, ctx.Err()
				}
			}
			return llm.ToolResult{}, nil
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = scriptContext(ctx, []llm.Tool{
		makeTool("a", llm.ToolExecutionParallel, releaseParallel),
		makeTool("b", llm.ToolExecutionParallel, releaseParallel),
		makeTool("c", llm.ToolExecutionSequential, releaseExclusive),
		makeTool("d", llm.ToolExecutionParallel, nil),
	}, update)
	go func() {
		for range 2 {
			select {
			case name := <-entered:
				if name != "a" && name != "b" {
					bad.Store(true)
				}
			case <-ctx.Done():
				return
			}
		}
		close(releaseParallel)
		select {
		case name := <-entered:
			if name != "c" {
				bad.Store(true)
			}
		case <-ctx.Done():
			return
		}
		close(releaseExclusive)
	}()
	result := runCode(t, ctx, `await Promise.all([tools.a(),tools.b(),tools.c(),tools.d()]); text("ordered");`, scriptHistory{})
	if result.IsError || bad.Load() || codeBody(t, result) != "ordered" {
		t.Fatalf("barrier violated: bad=%v result=%+v", bad.Load(), result)
	}
}

func TestPartialOutputAndExit(t *testing.T) {
	for _, tc := range []struct {
		name, source, body string
		failed             bool
		writes             bool
	}{
		{"throw", `text("before"); console.warn("warning", 2); store("k", 3); throw new Error("boom");`, "before\n<console_output>\nwarning 2\n</console_output>", true, false},
		{"exit", `store("k", 3); text("before"); try { exit(); } catch(e) { text("caught"); } text("after");`, "before", false, true},
		{"return", `text("before"); return {answer:42};`, "before\n{\"answer\":42}", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runCode(t, scriptContext(context.Background(), nil, nil), tc.source, scriptHistory{})
			details := codeDetails(t, result)
			if result.IsError != tc.failed || codeBody(t, result) != tc.body || (details.StoreWrites != nil) != tc.writes {
				t.Fatalf("result = %+v, details=%+v body=%q", result, details, codeBody(t, result))
			}
			if tc.failed && !strings.Contains(details.Error, "boom") {
				t.Fatalf("missing error: %+v", details)
			}
		})
	}
}

func TestCancelAndTimeout(t *testing.T) {
	for _, source := range []string{`while(true){}`, `while(true){await Promise.resolve();}`, `await tools.wait();`} {
		t.Run(source, func(t *testing.T) {
			var exited atomic.Bool
			tool := &testutil.Tool{ToolName: "wait", Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
				<-ctx.Done()
				exited.Store(true)
				return llm.ToolResult{}, ctx.Err()
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := runCode(t, scriptContext(ctx, []llm.Tool{tool}, nil), "// @options: {\"timeout_ms\": 20}\ntext(\"before\"); store(\"k\",1);\n"+source, scriptHistory{})
			if !result.IsError || codeBody(t, result) != "before" || codeDetails(t, result).StoreWrites != nil {
				t.Fatalf("timeout result = %+v", result)
			}
			if source == `await tools.wait();` && !exited.Load() {
				t.Fatal("tool did not exit before Execute returned")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = scriptContext(ctx, nil, func(llm.ToolResult) error { return nil })
	cancel()
	result := runCode(t, ctx, `text("must not run");`, scriptHistory{})
	if !result.IsError || codeDetails(t, result).StoreWrites != nil {
		t.Fatalf("canceled result = %+v", result)
	}
}

func TestUnawaitedCallsCleanup(t *testing.T) {
	var started, exited atomic.Int32
	var late atomic.Bool
	var once sync.Once
	entered := make(chan struct{})
	tools := []llm.Tool{
		&testutil.Tool{ToolName: "wait", Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
			started.Add(1)
			once.Do(func() { close(entered) })
			<-ctx.Done()
			exited.Add(1)
			return llm.ToolResult{IsError: true, Usage: &llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
		}},
		&testutil.Tool{ToolName: "exclusive", Mode: llm.ToolExecutionSequential, Run: func(context.Context, map[string]any) (llm.ToolResult, error) {
			late.Store(true)
			return llm.ToolResult{}, nil
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = scriptContext(ctx, tools, func(result llm.ToolResult) error {
		details, ok := result.Details.(Details)
		if ok && len(details.Calls) > 0 && details.Calls[0].Status == CallRunning {
			select {
			case <-entered:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	result := runCode(t, ctx, `tools.wait(); tools.exclusive(); return "done";`, scriptHistory{})
	details := codeDetails(t, result)
	if result.IsError || started.Load() != 1 || exited.Load() != 1 || late.Load() || result.Usage == nil || result.Usage.TotalTokens != 5 {
		t.Fatalf("cleanup failed: started=%d exited=%d late=%v result=%+v", started.Load(), exited.Load(), late.Load(), result)
	}
	for _, call := range details.Calls {
		if call.Status == CallQueued || call.Status == CallRunning {
			t.Fatalf("unsettled call: %+v", call)
		}
	}
}

func TestStalledPromise(t *testing.T) {
	result := runCode(t, scriptContext(context.Background(), nil, nil), `await new Promise(()=>{});`, scriptHistory{})
	if !result.IsError || !strings.Contains(strings.ToLower(codeDetails(t, result).Error), "stalled") {
		t.Fatalf("result = %+v", result)
	}
}

func TestSourceOptionsAndSerialization(t *testing.T) {
	for _, source := range []string{"", "```js\ntext(1)\n```", "text(1);\n// @options: {}", "// @options: {\"extra\":1}\ntext(1)", "// @options: {\"max_output_tokens\":-1}\ntext(1)", "// @options: {\"timeout_ms\":0}\ntext(1)", "// @options: {\"timeout_ms\":9223372036854775807}\ntext(1)", "// @options: {\"timeout_ms\":1.5}\ntext(1)"} {
		t.Run(source, func(t *testing.T) {
			result, err := New(scriptHistory{}).Execute(scriptContext(context.Background(), nil, nil), map[string]any{"code": source})
			if err == nil && !result.IsError {
				t.Fatalf("invalid source accepted: %q", source)
			}
		})
	}
	for _, source := range []string{`const a={};a.self=a;text(a);`, `const a={};a.self=a;store("a",a);`, `await tools.probe(1);`, `await tools.probe([]);`} {
		t.Run(source, func(t *testing.T) {
			var called atomic.Bool
			probe := &testutil.Tool{ToolName: "probe", Run: func(context.Context, map[string]any) (llm.ToolResult, error) {
				called.Store(true)
				return llm.ToolResult{}, nil
			}}
			result := runCode(t, scriptContext(context.Background(), []llm.Tool{probe}, nil), source, scriptHistory{})
			if !result.IsError || called.Load() {
				t.Fatalf("serialization boundary failed: %+v called=%v", result, called.Load())
			}
		})
	}
	result := runCode(t, scriptContext(context.Background(), nil, nil), "text(\"before\");\ntext(2);\nthrow new Error(\"located\");", scriptHistory{})
	if !strings.Contains(codeDetails(t, result).Error, "codemode.js:3:") {
		t.Fatalf("wrong source position: %s", codeDetails(t, result).Error)
	}
}

func TestResultEnvelopeAndJSONIsolation(t *testing.T) {
	args := map[string]any{"nested": map[string]any{"value": "original"}}
	detail := &envelopeDetails{Values: []string{"original"}}
	tools := []llm.Tool{&testutil.Tool{ToolName: "probe", Run: func(_ context.Context, in map[string]any) (llm.ToolResult, error) {
		if reflect.DeepEqual(in, args) == false {
			return llm.ToolResult{}, errors.New("arguments changed")
		}
		return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "private"}}, Details: detail}, nil
	}}}
	result := runCode(t, scriptContext(context.Background(), tools, nil), `JSON.parse = ()=>{throw new Error("replaced parser")};
const a={nested:{value:"original"}};const r=await tools.probe(a);r.details.values[0]="changed";a.nested.value="changed";
text([r.text,r.images,r.details.values[0],Object.getPrototypeOf(r)===Object.prototype]);`, scriptHistory{})
	if result.IsError || codeBody(t, result) != `["private",[],"changed",true]` || detail.Values[0] != "original" {
		t.Fatalf("isolation failed: %+v details=%+v", result, detail)
	}
	missing := runCode(t, scriptContext(context.Background(), []llm.Tool{&testutil.Tool{ToolName: "empty", Run: func(context.Context, map[string]any) (llm.ToolResult, error) { return llm.ToolResult{}, nil }}}, nil), `text(await tools.empty());`, scriptHistory{})
	if codeBody(t, missing) != `{"text":"","images":[],"details":null}` {
		t.Fatalf("empty envelope = %q", codeBody(t, missing))
	}
}

type envelopeDetails struct {
	Values []string `json:"values"`
}

func (*envelopeDetails) ToolDetails() {}

func TestToolDiscoveryAndRuntimeIsolation(t *testing.T) {
	tools := []llm.Tool{
		&testutil.Tool{ToolName: "raw-name", DescriptionText: "chosen", Schema: map[string]any{"type": "object"}, Run: func(context.Context, map[string]any) (llm.ToolResult, error) {
			return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "ran"}}}, nil
		}},
		New(scriptHistory{}),
	}
	ctx := scriptContext(context.Background(), tools, nil)
	result := runCode(t, ctx, `globalThis.previous=42;text(ALL_TOOLS.map(x=>x.name));text(describeTool("raw-name").description);text((await tools["raw-name"]()).text);text(typeof tools.codemode);text(Object.getPrototypeOf(tools)===null);`, scriptHistory{})
	if result.IsError || codeBody(t, result) != "[\"raw-name\"]\nchosen\nran\nundefined\ntrue" {
		t.Fatalf("discovery = %+v", result)
	}
	fresh := runCode(t, ctx, `text(typeof globalThis.previous);`, scriptHistory{})
	if codeBody(t, fresh) != "undefined" {
		t.Fatalf("VM reused: %+v", fresh)
	}
}
