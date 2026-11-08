package codemode

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/testutil"
)

func TestBudgetedOutputPreservesImagePosition(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf("// @options: {\"max_output_tokens\":4}\ntext(\"prefixprefix\");image(\"data:image/png;base64,%s\");text(\"suffixsuffix\");", base64.StdEncoding.EncodeToString(data.Bytes()))
	result := runCode(t, scriptContext(context.Background(), nil, nil), source, scriptHistory{})
	if result.IsError {
		t.Fatalf("image execution failed: %+v", result)
	}
	picture, prefix, suffix := -1, -1, -1
	for index, item := range result.Content {
		switch item := item.(type) {
		case llm.ImageContent:
			picture = index
		case llm.TextContent:
			if strings.Contains(item.Text, "prefix") {
				prefix = index
			}
			if strings.Contains(item.Text, "suffix") {
				suffix = index
			}
		}
	}
	if prefix < 0 || prefix >= picture || picture >= suffix {
		t.Fatalf("budget reordered text/image: prefix=%d image=%d suffix=%d content=%+v", prefix, picture, suffix, result.Content)
	}
}

func TestExitAfterToolAwaitRetainsSuccessfulWrites(t *testing.T) {
	var stopped atomic.Bool
	tools := []llm.Tool{
		&testutil.Tool{ToolName: "wait", Run: func(ctx context.Context, _ map[string]any) (llm.ToolResult, error) {
			<-ctx.Done()
			stopped.Store(true)
			return llm.ToolResult{}, ctx.Err()
		}},
		&testutil.Tool{ToolName: "ready", Run: func(context.Context, map[string]any) (llm.ToolResult, error) { return llm.ToolResult{}, nil }},
	}
	result := runCode(t, scriptContext(context.Background(), tools, nil), `tools.wait();await tools.ready();store("success",true);text("before");try{exit();}catch(e){text("caught");}text("after");`, scriptHistory{})
	if result.IsError || !stopped.Load() || codeBody(t, result) != "before" || codeDetails(t, result).StoreWrites == nil {
		t.Fatalf("async exit did not settle successfully: %+v stopped=%v", result, stopped.Load())
	}
}

func TestReturnedCircularValuePreservesPreviousOutput(t *testing.T) {
	result := runCode(t, scriptContext(context.Background(), nil, nil), `text("before");const value={};value.self=value;return value;`, scriptHistory{})
	if !result.IsError || codeBody(t, result) != "before" || codeDetails(t, result).StoreWrites != nil {
		t.Fatalf("return serialization failure = %+v", result)
	}
}
