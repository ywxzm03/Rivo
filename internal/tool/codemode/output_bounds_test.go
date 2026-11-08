package codemode

import (
	"context"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestBudgetedImageKeepsItsTypeLabel(t *testing.T) {
	items := []llm.Content{
		llm.TextContent{Text: "AAAA"},
		llm.TextContent{Text: "[image: image/png]"},
		llm.ImageContent{Data: "image-payload", MIMEType: "image/png"},
		llm.TextContent{Text: "BBBB"},
	}
	for _, characters := range []int{0, 4} {
		out := trimOutput(items, llm.TextOf(items), characters)
		imageIndex := -1
		for index, item := range out {
			if _, ok := item.(llm.ImageContent); ok {
				imageIndex = index
			}
		}
		if imageIndex < 1 {
			t.Fatalf("budget %d lost image or its label: %+v", characters, out)
		}
		label, ok := out[imageIndex-1].(llm.TextContent)
		if !ok || label.Text != "[image: image/png]" {
			t.Fatalf("budget %d split image metadata: %+v", characters, out)
		}
	}
}

func TestTextBoundaryTruncationIsVisible(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	result := runCode(t, scriptContext(context.Background(), nil, nil), "// @options: {\"max_output_tokens\":1}\ntext(\"ab\");text(\"cd\");", scriptHistory{})
	body := codeBody(t, result)
	if result.IsError || !strings.HasPrefix(body, "ab") || !strings.HasSuffix(body, "cd") || !strings.Contains(body, "output truncated") {
		t.Fatalf("boundary clipping hid truncation: %+v body=%q", result, body)
	}
}
