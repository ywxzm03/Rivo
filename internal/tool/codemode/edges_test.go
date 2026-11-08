package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/testutil"
)

func TestCallSummaryKeepsExactPreviewBoundary(t *testing.T) {
	probe := &testutil.Tool{ToolName: "probe", Run: func(context.Context, map[string]any) (llm.ToolResult, error) { return llm.ToolResult{}, nil }}
	for _, length := range []int{187, 188, 189} {
		result := runCode(t, scriptContext(context.Background(), []llm.Tool{probe}, nil), fmt.Sprintf(`await tools.probe({value:"x".repeat(%d)});`, length), scriptHistory{})
		calls := codeDetails(t, result).Calls
		if result.IsError || len(calls) != 1 {
			t.Fatalf("call failed: %+v", result)
		}
		if length <= 188 {
			if !json.Valid([]byte(calls[0].Args)) || strings.HasSuffix(calls[0].Args, "…") {
				t.Fatalf("exact-boundary preview was truncated: %q", calls[0].Args)
			}
		} else if utf8.RuneCountInString(calls[0].Args) != 200 || !strings.HasSuffix(calls[0].Args, "…") {
			t.Fatalf("long preview = %q", calls[0].Args)
		}
	}
}

func TestUnicodeWhitespaceDoesNotCreateOutputLines(t *testing.T) {
	result := runCode(t, scriptContext(context.Background(), nil, nil), `text("\u3000\u00a0\n");`, scriptHistory{})
	if codeDetails(t, result).OutputLines != 0 {
		t.Fatalf("whitespace-only output counted as content: %+v", codeDetails(t, result))
	}
}

type invalidResultDetails struct {
	Value chan int `json:"value"`
}

func (invalidResultDetails) ToolDetails() {}

func TestToolResultSerializationFailureIsCatchable(t *testing.T) {
	probe := &testutil.Tool{ToolName: "probe", Run: func(context.Context, map[string]any) (llm.ToolResult, error) {
		return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "partial native output"}}, Details: invalidResultDetails{Value: make(chan int)}}, nil
	}}
	result := runCode(t, scriptContext(context.Background(), []llm.Tool{probe}, nil), `try{await tools.probe();text("not rejected");}catch(e){text(e instanceof Error);text(e.result.text);text(e.result.details===null);}`, scriptHistory{})
	if result.IsError || codeBody(t, result) != "true\npartial native output\ntrue" || codeDetails(t, result).Calls[0].Status != CallError {
		t.Fatalf("tool serialization failure escaped catch or lost partial result: %+v", result)
	}
}

func TestCompletedFailureIsNotReclassifiedByLaterCancellation(t *testing.T) {
	e := newExecution(context.Background(), llm.ToolContext{Config: llm.AgentConfig{SessionID: "test"}})
	defer e.release()
	e.calls.records = []Call{{ID: "done", Name: "probe", Status: CallRunning}}
	e.calls.indices = map[string]int{"done": 0}
	e.calls.active = 1
	e.calls.stopping = true
	e.cancelTools()
	e.accept(completion{id: "done", result: llm.ToolResult{IsError: true, Content: []llm.Content{llm.TextContent{Text: "permission denied"}}}})
	result := e.finish(SourceOptions{})
	if codeDetails(t, result).Calls[0].Status != CallError {
		t.Fatalf("completed error became a canceled call: %+v", codeDetails(t, result).Calls)
	}
}

func TestSourceSyntaxAndUnserializableReturnAreVisible(t *testing.T) {
	for _, source := range []string{"text(1);\ntext(2);\nconst = ;", `return ()=>{};`, `text(1n);`, `await tools.codemode({code:"text(1)"});`} {
		result, err := New(scriptHistory{}).Execute(scriptContext(context.Background(), nil, nil), map[string]any{"code": source})
		if err == nil && !result.IsError {
			t.Fatalf("invalid or unserializable script accepted: %q", source)
		}
	}
}
