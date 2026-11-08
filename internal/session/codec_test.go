package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool"
)

func TestMessageRoundtrip(t *testing.T) {
	user := llm.UserMessage{Content: []llm.Content{
		llm.TextContent{Text: "hello"},
		llm.ImageContent{Data: "base64data", MIMEType: "image/png"},
	}}
	assistant := llm.AssistantMessage{
		API:        llm.APIOpenAIResponses,
		Provider:   "openai",
		Model:      "gpt-5",
		ResponseID: "resp_123",
		Content: []llm.Content{
			llm.ThinkingContent{Thinking: "thinking content", ThinkingSignature: "sig-1"},
			llm.TextContent{Text: "answer"},
			llm.ToolCall{ID: "call_1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
		},
		StopReason: llm.StopReasonToolUse,
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, CacheWriteTokens: 3, ReasoningTokens: 4, TotalTokens: 20, Cost: &llm.Cost{Input: 0.1, Output: 0.2, CacheRead: 0.03, CacheWrite: 0.04, Total: 0.37}},
	}
	toolResult := llm.ToolResultMessage{
		ToolCallID: "call_1",
		ToolName:   "edit",
		Content:    []llm.Content{llm.TextContent{Text: "file content"}},
		Details:    tool.EditDetails{Patch: "1-10"},
		IsError:    true,
		Usage:      &llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3, Cost: &llm.Cost{Total: 0.05}},
	}

	for name, msg := range map[string]llm.Message{"user": user, "assistant": assistant, "toolResult": toolResult} {
		t.Run(name, func(t *testing.T) {
			raw, err := encodeMessage(msg)
			if err != nil {
				t.Fatalf("encodeMessage() error = %v", err)
			}
			got, err := decodeMessage(raw)
			if err != nil {
				t.Fatalf("decodeMessage() error = %v", err)
			}
			// details 以原始 JSON 透传，比较前先再编码一次保证稳定
			raw2, err := encodeMessage(got)
			if err != nil {
				t.Fatalf("re-encode error = %v", err)
			}
			raw3, err := encodeMessage(msg)
			if err != nil {
				t.Fatalf("re-encode error = %v", err)
			}
			if string(raw2) != string(raw3) {
				t.Errorf("roundtrip unstable:\n got  %s\n want %s", raw2, raw3)
			}
			if !messagesEqual(got, msg) {
				t.Errorf("roundtrip mismatch:\n got  %#v\n want %#v", got, msg)
			}
		})
	}
}

// messagesEqual 比较 content 块与关键字段，跳过 details 的原始 JSON 形态差异
func messagesEqual(a, b llm.Message) bool {
	switch a := a.(type) {
	case llm.UserMessage:
		b, ok := b.(llm.UserMessage)
		return ok && contentEqual(a.Content, b.Content)
	case llm.AssistantMessage:
		b, ok := b.(llm.AssistantMessage)
		return ok && a.API == b.API && a.Provider == b.Provider && a.Model == b.Model &&
			a.ResponseID == b.ResponseID && a.StopReason == b.StopReason && reflect.DeepEqual(a.Usage, b.Usage) &&
			contentEqual(a.Content, b.Content)
	case llm.ToolResultMessage:
		b, ok := b.(llm.ToolResultMessage)
		if !ok || a.ToolCallID != b.ToolCallID || a.ToolName != b.ToolName || a.IsError != b.IsError || !contentEqual(a.Content, b.Content) {
			return false
		}
		if a.Usage == nil || b.Usage == nil {
			return a.Usage == nil && b.Usage == nil
		}
		return reflect.DeepEqual(*a.Usage, *b.Usage)
	}
	return false
}

func contentEqual(a, b []llm.Content) bool {
	if len(a) != len(b) {
		return false
	}
	// ToolCall 含 map 字段不可用 == 比较，统一用 DeepEqual
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestToolResultDetailsRawJSONRoundtrip(t *testing.T) {
	firstLine := 3
	msg := llm.ToolResultMessage{
		ToolCallID: "call_1",
		ToolName:   "edit",
		Content:    []llm.Content{llm.TextContent{Text: "x"}},
		Details:    tool.EditDetails{Diff: "-a\n+b", Patch: "1-10", FirstChangedLine: &firstLine},
	}
	raw, err := encodeMessage(msg)
	if err != nil {
		t.Fatalf("encodeMessage() error = %v", err)
	}
	got, err := decodeMessage(raw)
	if err != nil {
		t.Fatalf("decodeMessage() error = %v", err)
	}
	result := got.(llm.ToolResultMessage)
	back, ok := result.Details.(tool.EditDetails)
	if !ok {
		t.Fatalf("Details type = %T, want tool.EditDetails", result.Details)
	}
	if back.Patch != "1-10" || back.Diff != "-a\n+b" || back.FirstChangedLine == nil || *back.FirstChangedLine != 3 {
		t.Errorf("details roundtrip mismatch: %+v", back)
	}
}

func TestDecodeContentsDropsUnknownBlocks(t *testing.T) {
	raw := json.RawMessage(`[{"type":"text","text":"a"},{"type":"futureBlock","x":1}]`)
	got, err := decodeContents(raw)
	if err != nil {
		t.Fatalf("decodeContents() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("decoded content blocks = %d, want 1 (unknown dropped)", len(got))
	}
	if got[0] != llm.Content(llm.TextContent{Text: "a"}) {
		t.Errorf("decoded block = %#v, want text block", got[0])
	}
}

func TestParseFileTolerance(t *testing.T) {
	header := `{"type":"session","version":1,"id":"s1","timestamp":"2026-10-02T12:00:00Z","cwd":"/p"}`
	userLine := `{"type":"message","id":"aaaaaaaa","parentId":null,"timestamp":"2026-10-02T12:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`

	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"missing header", userLine + "\n", "missing session header"},
		{"wrong version", strings.Replace(header, `"version":1`, `"version":9`, 1) + "\n" + userLine + "\n", "unsupported session version"},
		{"invalid header json", "{\"type\":\"session\",\"version\":1}\n" + userLine + "\n", "session header missing id"},
		{"header must be first", "{\"type\":\"unknown\",\"id\":\"x\"}\n" + header + "\n", "missing session header"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseFile([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parseFile() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}

	t.Run("skips bad lines and unknown types", func(t *testing.T) {
		data := strings.Join([]string{
			header,
			"",
			"not-json",
			`{"type":"future_entry","id":"bbbbbbbb","parentId":"aaaaaaaa","timestamp":"t"}`,
			`{"type":"session","version":1,"id":"s2"}`,
			userLine,
			`{"type":"message","id":"cccccccc","parentId":` + `"bbbbbbbb"` + `,"timestamp":"t","message":{"role":"unknownRole","content":[]}}`,
		}, "\n") + "\n"
		header, entries, err := parseFile([]byte(data))
		if err != nil {
			t.Fatalf("parseFile() error = %v", err)
		}
		if header.ID != "s1" {
			t.Errorf("header id = %q, want s1", header.ID)
		}
		if len(entries) != 3 {
			t.Fatalf("entries = %d, want 3 (unknown entry kept, later session line ignored, unknown role downgraded)", len(entries))
		}
		if _, ok := entries[0].(*UnknownEntry); !ok {
			t.Errorf("entries[0] type = %T, want *UnknownEntry", entries[0])
		}
		if _, ok := entries[1].(*MessageEntry); !ok {
			t.Errorf("entries[1] type = %T, want *MessageEntry", entries[1])
		}
		if _, ok := entries[2].(*UnknownEntry); !ok {
			t.Errorf("entries[2] type = %T, want *UnknownEntry (unknown role downgraded)", entries[2])
		}
	})
}
