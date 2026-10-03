package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// recordingStreamFn 记录每次调用收到的上下文，并按序返回预设的摘要回复
func recordingStreamFn(replies ...string) (llm.StreamFn, *[]llm.Context) {
	var seen []llm.Context
	return func(_ context.Context, _ llm.Model, c llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		seen = append(seen, c)
		ch := make(chan llm.AssistantMessageEvent, 1)
		reply := replies[min(len(seen)-1, len(replies)-1)]
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content: []llm.Content{llm.TextContent{Text: reply}},
			Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5},
		}}
		close(ch)
		return ch, nil
	}, &seen
}

// compactFixture 造四条约 100 token 的消息，最后一条 assistant 带真实 usage
func compactFixture() []llm.Message {
	text := textOf(400)
	return []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: text}}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: text}}, Usage: llm.Usage{InputTokens: 500, OutputTokens: 20}},
	}
}

// singleCallConfig 让预算落在 index 2 的 user 消息上，切割点在轮次边界，走单次摘要路径
func singleCallConfig() Config {
	return Config{KeepRecentTokens: 150}
}

func TestCompactGeneratesSummary(t *testing.T) {
	streamFn, seen := recordingStreamFn("SUMMARY")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	result, err := c.Compact(context.Background(), compactFixture(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "SUMMARY" {
		t.Errorf("summary = %q, want SUMMARY", result.Summary)
	}
	if result.KeptCount != 2 {
		t.Errorf("KeptCount = %d, want 2", result.KeptCount)
	}
	if result.TokensBefore == 0 {
		t.Error("TokensBefore should be estimated")
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", result.Usage)
	}
	if len(*seen) != 1 {
		t.Fatalf("summarize calls = %d, want 1", len(*seen))
	}
	call := (*seen)[0]
	if call.SystemPrompt != SummarizerSystemPrompt {
		t.Errorf("system prompt = %q", call.SystemPrompt)
	}
	if len(call.Tools) != 0 {
		t.Errorf("summarize call should carry no tools, got %d", len(call.Tools))
	}
	if !strings.Contains(call.Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text, "## Goal") {
		t.Error("summarize prompt should contain format sections")
	}
}

func TestCompactUsesUpdatePromptWithPreviousSummary(t *testing.T) {
	streamFn, seen := recordingStreamFn("MERGED")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	// messages[0] 是上次回插的摘要消息，序列化必须跳过它
	messages := append([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "wrapped previous"}}},
	}, compactFixture()...)
	if _, err := c.Compact(context.Background(), messages, "old summary", "focus on tests"); err != nil {
		t.Fatal(err)
	}
	prompt := (*seen)[0].Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text
	if !strings.Contains(prompt, "<previous-summary>\nold summary\n</previous-summary>") {
		t.Errorf("prompt = %q, want previous-summary", prompt)
	}
	if !strings.Contains(prompt, "Additional focus: focus on tests") {
		t.Errorf("prompt = %q, want additional focus", prompt)
	}
	if strings.Contains(prompt, "wrapped previous") {
		t.Error("synthetic summary message should not be serialized again")
	}
}

func TestCompactSplitTurnMergesSummaries(t *testing.T) {
	streamFn, seen := recordingStreamFn("HISTORY", "PREFIX")
	// 预算 99 token：预算边界落在最后一条 assistant 上，保留尾部以 assistant 开头，触发 split turn
	c := &Compactor{StreamFn: streamFn, Config: Config{KeepRecentTokens: 99}}
	result, err := c.Compact(context.Background(), compactFixture(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsSplitTurn {
		t.Fatalf("expected split turn, got %+v", result)
	}
	if result.Summary != "HISTORY\n\n---\n\n**Turn Context (split turn):**\n\nPREFIX" {
		t.Errorf("summary = %q, want merged", result.Summary)
	}
	if len(*seen) != 2 {
		t.Fatalf("summarize calls = %d, want 2", len(*seen))
	}
	first := (*seen)[0].Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text
	second := (*seen)[1].Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text
	if strings.Contains(first, "beginning of the current conversation turn") {
		t.Error("first call should summarize history, not the turn prefix")
	}
	if !strings.Contains(second, "beginning of the current conversation turn") {
		t.Error("second call should use the turn prefix prompt")
	}
}

func TestCompactRejectsEmptySummary(t *testing.T) {
	streamFn, _ := recordingStreamFn("")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	if _, err := c.Compact(context.Background(), compactFixture(), "", ""); err == nil || !strings.Contains(err.Error(), "摘要为空") {
		t.Fatalf("err = %v, want empty summary error", err)
	}
}

func TestCompactRejectsTruncatedSummary(t *testing.T) {
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content:    []llm.Content{llm.TextContent{Text: "partial"}},
			StopReason: llm.StopReasonLength,
		}}
		close(ch)
		return ch, nil
	}
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	if _, err := c.Compact(context.Background(), compactFixture(), "", ""); err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("err = %v, want truncated error", err)
	}
}

func TestCompactPropagatesStreamError(t *testing.T) {
	sentinel := errors.New("boom")
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageErrorEvent{Err: sentinel}
		close(ch)
		return ch, nil
	}
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	if _, err := c.Compact(context.Background(), compactFixture(), "", ""); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCompactRejectsEmptyRegion(t *testing.T) {
	streamFn, _ := recordingStreamFn("SUMMARY")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	if _, err := c.Compact(context.Background(), nil, "", ""); err == nil {
		t.Fatal("compacting empty messages should fail")
	}
}

func TestCompactSplitTurnEmptyHistoryKeepsPreviousSummary(t *testing.T) {
	streamFn, seen := recordingStreamFn("PREFIX")
	c := &Compactor{StreamFn: streamFn, Config: Config{KeepRecentTokens: 99}}
	// messages[0] 是上次回插的摘要消息，被截断轮次的 user 紧贴序列化起点：历史区为空，
	// 上次摘要必须作为合并基线保留，而不是被轮次前缀摘要取代
	messages := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "wrapped previous"}}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: textOf(400)}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: textOf(400)}}},
	}
	result, err := c.Compact(context.Background(), messages, "old summary", "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsSplitTurn {
		t.Fatalf("expected split turn, got %+v", result)
	}
	if len(*seen) != 1 {
		t.Fatalf("summarize calls = %d, want 1 (history region empty)", len(*seen))
	}
	if result.Summary != "old summary\n\n---\n\n**Turn Context (split turn):**\n\nPREFIX" {
		t.Errorf("summary = %q, want previous summary merged with prefix", result.Summary)
	}
}
