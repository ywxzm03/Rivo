package compaction

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

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

func contextView(messages []llm.Message) ContextView {
	entries := make([]ContextEntry, 0, len(messages))
	for i, message := range messages {
		entries = append(entries, ContextEntry{SourceID: fmt.Sprintf("entry-%d", i), Kind: ContextMessage, Messages: []llm.Message{message}})
	}
	return ContextView{Entries: entries}
}

func contextViewWithCompaction(summary string, snapshot *llm.SystemMessage, messages []llm.Message) ContextView {
	compactionMessages := make([]llm.Message, 0, 2)
	if snapshot != nil {
		compactionMessages = append(compactionMessages, *snapshot)
	}
	compactionMessages = append(compactionMessages, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: WrappedSummary(summary)}}})
	entries := []ContextEntry{{SourceID: "compaction", Kind: ContextCompaction, Messages: compactionMessages, Summary: summary}}
	for i, message := range messages {
		entries = append(entries, ContextEntry{SourceID: fmt.Sprintf("entry-%d", i), Kind: ContextMessage, Messages: []llm.Message{message}})
	}
	return ContextView{Entries: entries}
}

// singleCallConfig 让预算落在 index 2 的 user 消息上，切割点在轮次边界，走单次摘要路径
func singleCallConfig() Config {
	return Config{KeepRecentTokens: 150}
}

func TestCompactGeneratesSummary(t *testing.T) {
	streamFn, seen := recordingStreamFn("SUMMARY")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	result, err := c.Compact(context.Background(), contextView(compactFixture()), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "SUMMARY" {
		t.Errorf("summary = %q, want SUMMARY", result.Summary)
	}
	if result.KeptMessageCount != 2 {
		t.Errorf("KeptMessageCount = %d, want 2", result.KeptMessageCount)
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

func TestCompactPricesSummaryUsage(t *testing.T) {
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content: []llm.Content{llm.TextContent{Text: "SUMMARY"}},
			Usage:   llm.Usage{InputTokens: 1_000_000},
		}}
		close(ch)
		return ch, nil
	}
	c := &Compactor{Model: llm.Model{Provider: "openai", ID: "gpt-6.1-sol"}, StreamFn: streamFn, Config: singleCallConfig()}
	result, err := c.Compact(context.Background(), contextView(compactFixture()), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.Cost == nil || result.Usage.Cost.Total != 4 {
		t.Fatalf("summary cost = %+v, want $4", result.Usage.Cost)
	}
}

func TestCombineUsageAddsCosts(t *testing.T) {
	got := combineUsage(
		llm.Usage{InputTokens: 2, Cost: &llm.Cost{Input: 0.1, Total: 0.1}},
		llm.Usage{OutputTokens: 3, Cost: &llm.Cost{Output: 0.2, Total: 0.2}},
	)
	if got.Cost == nil {
		t.Fatal("combined cost = nil")
	}
	want := &llm.Cost{Input: 0.1, Output: 0.2, Total: 0.3}
	if math.Abs(got.Cost.Input-want.Input) > 1e-12 || math.Abs(got.Cost.Output-want.Output) > 1e-12 || math.Abs(got.Cost.Total-want.Total) > 1e-12 {
		t.Fatalf("combined cost = %+v, want %+v", *got.Cost, *want)
	}
}

func TestCompactUsesUpdatePromptWithPreviousSummary(t *testing.T) {
	streamFn, seen := recordingStreamFn("MERGED")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	view := contextViewWithCompaction("old summary", nil, compactFixture())
	if _, err := c.Compact(context.Background(), view, "focus on tests"); err != nil {
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

func TestCompactTracksFileListsAcrossSummaryBoundary(t *testing.T) {
	streamFn, _ := recordingStreamFn("SUMMARY")
	view := contextView([]llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "inspect files"}}},
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{Name: "read", Arguments: map[string]any{"path": "z.go"}},
			llm.ToolCall{Name: "write", Arguments: map[string]any{"path": "a.go"}},
		}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "continue"}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}},
	})
	c := &Compactor{StreamFn: streamFn, Config: Config{KeepRecentTokens: 1}}
	result, err := c.Compact(context.Background(), view, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FileLists.ReadFiles) != 1 || result.FileLists.ReadFiles[0] != "z.go" || len(result.FileLists.ModifiedFiles) != 1 || result.FileLists.ModifiedFiles[0] != "a.go" {
		t.Fatalf("file lists = %+v, want read z.go and modified a.go", result.FileLists)
	}
	if !strings.Contains(result.Summary, "<modified-files>\na.go\n</modified-files>") {
		t.Fatalf("summary = %q, want modified file section", result.Summary)
	}
}

func TestCompactCarriesPreviousFileLists(t *testing.T) {
	streamFn, _ := recordingStreamFn("SUMMARY")
	view := contextViewWithCompaction("old summary", nil, compactFixture())
	view.Entries[0].Details = &FileLists{ReadFiles: []string{"old.go"}, ModifiedFiles: []string{"changed.go"}}
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	result, err := c.Compact(context.Background(), view, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.FileLists.ReadFiles, ",") != "old.go" || len(result.FileLists.ModifiedFiles) != 1 || result.FileLists.ModifiedFiles[0] != "changed.go" {
		t.Fatalf("file lists = %+v, want previous lists", result.FileLists)
	}
}

func TestCompactSkipsCompactionSnapshotAndSummary(t *testing.T) {
	streamFn, seen := recordingStreamFn("MERGED")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	snapshot := &llm.SystemMessage{Sections: map[string]string{"instructions": "snapshot"}}
	view := contextViewWithCompaction("old summary", snapshot, compactFixture())
	result, err := c.Compact(context.Background(), view, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.FirstKeptSourceID == "compaction" || result.FirstKeptSourceID == "" {
		t.Fatalf("FirstKeptSourceID = %q, want retained message entry", result.FirstKeptSourceID)
	}
	prompt := (*seen)[0].Messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text
	if strings.Contains(prompt, "The conversation history before this point was compacted") {
		t.Error("wrapped previous summary should not be serialized into incremental conversation")
	}
	if strings.Contains(prompt, "snapshot") {
		t.Error("compaction system snapshot should not be serialized into summary input")
	}
	if strings.Count(prompt, "<previous-summary>\nold summary\n</previous-summary>") != 1 {
		t.Errorf("previous summary block count = %d, want 1", strings.Count(prompt, "<previous-summary>\nold summary\n</previous-summary>"))
	}
}

func TestCompactSplitTurnMergesSummaries(t *testing.T) {
	streamFn, seen := recordingStreamFn("HISTORY", "PREFIX")
	// 预算 99 token：预算边界落在最后一条 assistant 上，保留尾部以 assistant 开头，触发 split turn
	c := &Compactor{StreamFn: streamFn, Config: Config{KeepRecentTokens: 99}}
	result, err := c.Compact(context.Background(), contextView(compactFixture()), "")
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
	if _, err := c.Compact(context.Background(), contextView(compactFixture()), ""); err == nil || !strings.Contains(err.Error(), "empty summary") {
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
	if _, err := c.Compact(context.Background(), contextView(compactFixture()), ""); err == nil || !strings.Contains(err.Error(), "truncated by output length") {
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
	if _, err := c.Compact(context.Background(), contextView(compactFixture()), ""); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCompactDoesNotRetryAbortedSummary(t *testing.T) {
	tests := []struct {
		name  string
		event llm.AssistantMessageEvent
	}{
		{name: "error", event: llm.AssistantMessageErrorEvent{
			Reason: llm.StopReasonAborted,
			Err:    errors.New("openai: stream ended before a terminal event"),
		}},
		{name: "done", event: llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content:    []llm.Content{llm.TextContent{Text: "partial summary"}},
			StopReason: llm.StopReasonAborted,
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, _ llm.Options) (<-chan llm.AssistantMessageEvent, error) {
				calls++
				ch := make(chan llm.AssistantMessageEvent, 1)
				ch <- tt.event
				close(ch)
				return ch, nil
			}
			c := &Compactor{
				StreamFn: streamFn,
				Config:   singleCallConfig(),
				Retry:    RetryPolicy{Enabled: true, MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond},
			}
			result, err := c.Compact(context.Background(), contextView(compactFixture()), "")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if calls != 1 {
				t.Fatalf("summary calls = %d, want 1", calls)
			}
			if result.Summary != "" {
				t.Fatalf("aborted summary = %q, want empty", result.Summary)
			}
		})
	}
}

func TestCompactRejectsEmptyRegion(t *testing.T) {
	streamFn, _ := recordingStreamFn("SUMMARY")
	c := &Compactor{StreamFn: streamFn, Config: singleCallConfig()}
	if _, err := c.Compact(context.Background(), ContextView{}, ""); err == nil {
		t.Fatal("compacting empty messages should fail")
	}
}

func TestCompactSplitTurnEmptyHistoryKeepsPreviousSummary(t *testing.T) {
	streamFn, seen := recordingStreamFn("PREFIX")
	c := &Compactor{StreamFn: streamFn, Config: Config{KeepRecentTokens: 99}}
	// 截断轮次的 user 紧贴序列化起点：历史区为空，上次摘要必须作为合并基线保留
	view := contextViewWithCompaction("old summary", nil, []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: textOf(400)}}},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: textOf(400)}}},
	})
	result, err := c.Compact(context.Background(), view, "")
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

func TestSummaryMaxTokens(t *testing.T) {
	// 预留量 20000 的八成为 16000，低于模型上限时取前者
	c := &Compactor{Config: Config{ReserveTokens: 20000}, Model: llm.Model{MaxTokens: 128000}}
	if got := c.summaryMaxTokens(); got != 16000 {
		t.Errorf("summaryMaxTokens = %d, want 16000", got)
	}
	// 模型上限更小时取模型上限
	c = &Compactor{Config: Config{ReserveTokens: 20000}, Model: llm.Model{MaxTokens: 8192}}
	if got := c.summaryMaxTokens(); got != 8192 {
		t.Errorf("summaryMaxTokens = %d, want 8192", got)
	}
	// 模型上限未知时只按预留量收缩
	c = &Compactor{Config: Config{ReserveTokens: 20000}}
	if got := c.summaryMaxTokens(); got != 16000 {
		t.Errorf("summaryMaxTokens = %d, want 16000", got)
	}
}

func TestSummarizeOncePassesMaxTokensOption(t *testing.T) {
	var got llm.Options
	streamFn := func(_ context.Context, _ llm.Model, _ llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		got = opts
		ch := make(chan llm.AssistantMessageEvent, 1)
		ch <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "SUMMARY"}}}}
		close(ch)
		return ch, nil
	}
	c := &Compactor{StreamFn: streamFn, Config: Config{ReserveTokens: 20000}, Model: llm.Model{MaxTokens: 8192}}
	if _, _, err := c.summarizeOnce(context.Background(), "prompt"); err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", got.MaxTokens)
	}
}
