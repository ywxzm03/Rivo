package codemode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestStoreValueSemanticsAndLimits(t *testing.T) {
	ctx := scriptContext(context.Background(), nil, nil)
	first := runCode(t, ctx, `store("",null);store("x",{value:1});const a=load("x");a.value=2;text(load("x"));store("gone",1);store("gone",undefined);text(load("missing")===undefined);`, scriptHistory{})
	if first.IsError || codeBody(t, first) != "{\"value\":1}\ntrue" {
		t.Fatalf("value semantics: %+v", first)
	}
	details := codeDetails(t, first)
	if string(details.StoreWrites.Set[""]) != "null" || len(details.StoreWrites.Delete) != 1 || details.StoreWrites.Delete[0] != "gone" {
		t.Fatalf("null/delete lost: %+v", details.StoreWrites)
	}
	raw, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	second := runCode(t, ctx, `text([load(""),load("x"),load("gone")===undefined]);`, scriptHistory{entries: []json.RawMessage{raw}})
	if codeBody(t, second) != `[null,{"value":1},true]` {
		t.Fatalf("history load: %+v", second)
	}
	host, ok := llm.CurrentToolContext(ctx)
	if !ok {
		t.Fatal("missing host")
	}
	host.Config.SessionID = "fork-session"
	fork := runCode(t, llm.WithToolContext(ctx, host), `text(load("x")===undefined);`, scriptHistory{entries: []json.RawMessage{raw}})
	if codeBody(t, fork) != "true" {
		t.Fatalf("parent store leaked into fork: %+v", fork)
	}
	failed := runCode(t, ctx, `store("x",2);throw new Error("rollback");`, scriptHistory{entries: []json.RawMessage{raw}})
	if !failed.IsError || codeDetails(t, failed).StoreWrites != nil {
		t.Fatalf("failed store committed: %+v", failed)
	}
	limits := runCode(t, ctx, `const v="x".repeat(262141);for(const key of ["a","b","c","d"])store(key,v);
try{store("e",0);text("overflow accepted");}catch(e){text(e instanceof RangeError);}
store("a",undefined);store("e",0);text(load("e"));
try{store("b","x".repeat(262143));text("large accepted");}catch(e){text(load("b").length);}`, scriptHistory{})
	if limits.IsError || codeBody(t, limits) != "true\n0\n262141" {
		t.Fatalf("store limits: %+v body=%q", limits, codeBody(t, limits))
	}
}

func TestStoreHistoryAndEarlierResults(t *testing.T) {
	ctx := scriptContext(context.Background(), nil, nil)
	older := Details{Codemode: true, StoreWrites: &StoreWrites{SessionID: "script-session", Set: map[string]json.RawMessage{"key": json.RawMessage(`1`)}}}
	raw, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := llm.CurrentToolContext(ctx)
	if !ok {
		t.Fatal("missing host")
	}
	host.Runtime.EarlierResults = []llm.ToolResultMessage{
		{ToolName: "codemode", Details: Details{Codemode: true, StoreWrites: &StoreWrites{SessionID: "script-session", Set: map[string]json.RawMessage{"key": json.RawMessage(`2`)}}}},
		{ToolName: "codemode", IsError: true, Details: Details{Codemode: true, StoreWrites: &StoreWrites{SessionID: "script-session", Set: map[string]json.RawMessage{"key": json.RawMessage(`3`)}}}},
	}
	result := runCode(t, llm.WithToolContext(ctx, host), `text(load("key"));`, scriptHistory{entries: []json.RawMessage{raw}})
	if codeBody(t, result) != "2" {
		t.Fatalf("earlier results precedence: %+v", result)
	}
	for _, history := range []scriptHistory{{err: errors.New("history unavailable")}, {entries: []json.RawMessage{json.RawMessage(`{"codemode":`)}}} {
		result, err := New(history).Execute(ctx, map[string]any{"code": `text("should not run");`})
		if err == nil && !result.IsError {
			t.Fatalf("invalid history accepted: %+v", result)
		}
	}
}

func TestOutputBudgetAndSpill(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx := scriptContext(context.Background(), nil, nil)
	for _, budget := range []int{0, 4} {
		code := fmt.Sprintf("// @options: {\"max_output_tokens\":%d}\ntext(\"你好-alpha\\nbeta\\ngamma-tail\");console.log(\"console-tail\");", budget)
		result := runCode(t, ctx, code, scriptHistory{})
		details := codeDetails(t, result)
		if result.IsError || details.FullOutputPath == "" || details.OutputLines != 6 {
			t.Fatalf("spill metadata: %+v", details)
		}
		full, err := os.ReadFile(details.FullOutputPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(full) != "你好-alpha\nbeta\ngamma-tail\n<console_output>\nconsole-tail\n</console_output>" {
			t.Fatalf("full text = %q", full)
		}
		body := codeBody(t, result)
		if budget == 0 && body != "" {
			t.Fatalf("zero budget exposed output: %q", body)
		}
		if strings.ContainsRune(body, '\uFFFD') {
			t.Fatalf("UTF-8 cut: %q", body)
		}
		if !strings.Contains(llm.TextOf(result.Content), details.FullOutputPath) {
			t.Fatal("model cannot find full output")
		}
	}
	failed := runCode(t, ctx, "// @options: {\"max_output_tokens\":0}\ntext(\"before\");store(\"key\",1);throw new Error(\"boom\");", scriptHistory{})
	if !failed.IsError || codeDetails(t, failed).StoreWrites != nil || !strings.Contains(llm.TextOf(failed.Content), "boom") || codeDetails(t, failed).OutputLines != 1 {
		t.Fatalf("zero-budget failure: %+v", failed)
	}
	last, ok := failed.Content[len(failed.Content)-1].(llm.TextContent)
	if !ok || last.Text != codeDetails(t, failed).Error {
		t.Fatalf("error must be a separate final block: %+v", failed.Content)
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	ioFailure := runCode(t, ctx, "// @options: {\"max_output_tokens\":0}\nstore(\"key\",1);text(\"cannot spill\");", scriptHistory{})
	if !ioFailure.IsError || codeDetails(t, ioFailure).StoreWrites != nil || codeDetails(t, ioFailure).FullOutputPath != "" {
		t.Fatalf("spill I/O failure committed state: %+v", ioFailure)
	}
}

func TestOutputLimitsAreUncatchable(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, source := range []string{
		`store("key",1);text("before");try{text("x".repeat(16*1024*1024));}catch(e){text("caught");}text("after");`,
		`store("key",1);text("before");try{for(let i=0;i<100001;i++)console.log("");}catch(e){text("caught");}text("after");`,
	} {
		result := runCode(t, scriptContext(context.Background(), nil, nil), source, scriptHistory{})
		if !result.IsError || codeDetails(t, result).StoreWrites != nil || strings.Contains(codeBody(t, result), "caught") || strings.Contains(codeBody(t, result), "after") {
			t.Fatalf("output limit was catchable: %+v", codeDetails(t, result))
		}
	}
}

func TestImageOutputUsesProcessedDataAndAPIGate(t *testing.T) {
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewRGBA(image.Rect(0, 0, 2100, 2))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(input.Bytes())
	source := fmt.Sprintf(`image({type:"image",data:%q,mimeType:"image/png"});`, encoded)
	ctx := scriptContext(context.Background(), nil, nil)
	result := runCode(t, ctx, source, scriptHistory{})
	if result.IsError || codeDetails(t, result).OutputLines != 1 {
		t.Fatalf("image output failed: %+v", result)
	}
	var picture llm.ImageContent
	for _, item := range result.Content {
		if image, ok := item.(llm.ImageContent); ok {
			picture = image
		}
	}
	data, err := base64.StdEncoding.DecodeString(picture.Data)
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width > 2000 || config.Height > 2000 || picture.Data == encoded {
		t.Fatalf("image not processed: %dx%d", config.Width, config.Height)
	}
	for _, model := range []llm.Model{{API: llm.API("openai-completions"), SupportsImages: true}, {API: llm.APIOpenAIResponses, SupportsImages: false}} {
		host, ok := llm.CurrentToolContext(ctx)
		if !ok {
			t.Fatal("missing host")
		}
		host.Config.Model = model
		denied := runCode(t, llm.WithToolContext(ctx, host), source, scriptHistory{})
		if !denied.IsError || codeDetails(t, denied).OutputLines != 0 {
			t.Fatalf("unsupported image accepted: %+v", denied)
		}
	}
	for _, bad := range []string{`image("https://example.test/a.png")`, `image("data:image/png;base64,invalid")`, fmt.Sprintf(`image({type:"image",data:%q,mimeType:"image/jpeg"})`, encoded)} {
		denied := runCode(t, ctx, bad, scriptHistory{})
		if !denied.IsError {
			t.Fatalf("bad image accepted: %s", bad)
		}
	}
}

func TestKnownOutputLineBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text  string
		lines int
	}{{"", 0}, {" \n\t", 0}, {"a\nb\n", 2}, {"\na\n\nb\n", 4}, {"a\r\nb\rc", 3}} {
		literal, err := json.Marshal(tc.text)
		if err != nil {
			t.Fatal(err)
		}
		result := runCode(t, scriptContext(context.Background(), nil, nil), "text("+string(literal)+");", scriptHistory{})
		if codeDetails(t, result).OutputLines != tc.lines {
			t.Fatalf("%q lines=%d want%d", tc.text, codeDetails(t, result).OutputLines, tc.lines)
		}
	}
}
