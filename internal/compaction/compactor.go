package compaction

import (
	"context"
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
)

const (
	DefaultReserveTokens    = 16384
	DefaultKeepRecentTokens = 20000
)

// Config 压缩配置
type Config struct {
	ContextWindow    int
	ReserveTokens    int
	KeepRecentTokens int
}

// DefaultConfig 以模型窗口大小生成默认配置
func DefaultConfig(contextWindow int) Config {
	return Config{
		ContextWindow:    contextWindow,
		ReserveTokens:    DefaultReserveTokens,
		KeepRecentTokens: DefaultKeepRecentTokens,
	}
}

// Compactor 执行上下文压缩：通过 StreamFn 发起独立的摘要调用，产出结构化摘要文本
type Compactor struct {
	StreamFn llm.StreamFn
	Model    llm.Model
	Config   Config
	// Retry 为零值时不重试
	Retry RetryPolicy
}

// Result 一次压缩的产出
type Result struct {
	Summary           string
	FileLists         ctxview.FileLists
	FirstKeptSourceID string
	KeptMessageCount  int
	TokensBefore      int
	Usage             llm.Usage
	IsSplitTurn       bool
}

// Compact 对当前上下文视图执行一次压缩，compaction entry 作为 boundary 不参与新摘要素材
func (c *Compactor) Compact(ctx context.Context, view ctxview.ContextView, instructions string) (Result, error) {
	messages := view.Messages()
	tokensBefore := ContextTokens(messages)
	if view.UsageStale {
		tokensBefore = EstimateContextTokens(messages)
	}
	start, previousSummary, previousFiles := compactionBoundary(view.Entries)
	plan := FindCutPoint(view.Entries, start, c.Config.KeepRecentTokens)
	if plan.CutIndex <= start {
		return Result{}, fmt.Errorf("compaction: no compressible history range")
	}
	result := Result{
		FirstKeptSourceID: view.Entries[plan.CutIndex].SourceID,
		KeptMessageCount:  ctxview.MessageCount(view.Entries[plan.CutIndex:]),
		TokensBefore:      tokensBefore,
		IsSplitTurn:       plan.IsSplitTurn,
	}
	if plan.IsSplitTurn {
		history := ctxview.SummaryMessages(view.Entries[start:plan.TurnStartIndex])
		turnPrefix := ctxview.SummaryMessages(view.Entries[plan.TurnStartIndex:plan.CutIndex])
		if len(history) == 0 && previousSummary == "" && len(turnPrefix) == 0 {
			return Result{}, fmt.Errorf("compaction: no compressible history range")
		}
		var summary string
		var usage llm.Usage
		// 1.先摘历史；历史区为空时以上次摘要为合并基线
		if len(history) > 0 {
			historySummary, historyUsage, err := c.summarizeRegion(ctx, history, previousSummary, instructions, false)
			if err != nil {
				return Result{}, err
			}
			summary, usage = historySummary, historyUsage
		} else if previousSummary != "" {
			summary = previousSummary
		}
		// 2.再摘轮次前缀并合并
		if len(turnPrefix) > 0 {
			prefixSummary, prefixUsage, err := c.summarizeRegion(ctx, turnPrefix, "", "", true)
			if err != nil {
				return Result{}, err
			}
			if summary == "" {
				summary, usage = prefixSummary, prefixUsage
			} else {
				summary = MergeSummaries(summary, prefixSummary)
				usage = combineUsage(usage, prefixUsage)
			}
		}
		result.Summary, result.FileLists = summaryWithFileLists(summary, previousFiles, history, turnPrefix)
		result.Usage = usage
		return result, nil
	}
	conversation := ctxview.SummaryMessages(view.Entries[start:plan.CutIndex])
	if len(conversation) == 0 {
		return Result{}, fmt.Errorf("compaction: no compressible history range")
	}
	summary, usage, err := c.summarizeRegion(ctx, conversation, previousSummary, instructions, false)
	if err != nil {
		return Result{}, err
	}
	result.Summary, result.FileLists = summaryWithFileLists(summary, previousFiles, conversation)
	result.Usage = usage
	return result, nil
}

func combineUsage(first, second llm.Usage) llm.Usage {
	combined := llm.Usage{
		InputTokens:        first.InputTokens + second.InputTokens,
		OutputTokens:       first.OutputTokens + second.OutputTokens,
		CacheReadTokens:    first.CacheReadTokens + second.CacheReadTokens,
		CacheWriteTokens:   first.CacheWriteTokens + second.CacheWriteTokens,
		CacheWrite1HTokens: first.CacheWrite1HTokens + second.CacheWrite1HTokens,
		ReasoningTokens:    first.ReasoningTokens + second.ReasoningTokens,
		TotalTokens:        first.TotalTokens + second.TotalTokens,
	}
	if combined.TotalTokens == 0 {
		combined.TotalTokens = combined.ContextTokens()
	}
	if first.Cost != nil || second.Cost != nil {
		combined.Cost = &llm.Cost{}
		if first.Cost != nil {
			combined.Cost.Input += first.Cost.Input
			combined.Cost.Output += first.Cost.Output
			combined.Cost.CacheRead += first.Cost.CacheRead
			combined.Cost.CacheWrite += first.Cost.CacheWrite
			combined.Cost.Total += first.Cost.Total
		}
		if second.Cost != nil {
			combined.Cost.Input += second.Cost.Input
			combined.Cost.Output += second.Cost.Output
			combined.Cost.CacheRead += second.Cost.CacheRead
			combined.Cost.CacheWrite += second.Cost.CacheWrite
			combined.Cost.Total += second.Cost.Total
		}
	}
	return combined
}

// compactionBoundary 返回最新 compaction 后的 entry 起点和增量摘要基线
func compactionBoundary(entries []ctxview.ContextEntry) (int, string, *ctxview.FileLists) {
	index := -1
	for i, entry := range entries {
		if entry.Kind == ctxview.ContextCompaction {
			index = i
		}
	}
	if index < 0 {
		return 0, "", nil
	}
	return index + 1, entries[index].Summary, entries[index].Details
}

func summaryWithFileLists(summary string, previous *ctxview.FileLists, regions ...[]llm.Message) (string, ctxview.FileLists) {
	ops := fileOperationsFromLists(previous)
	for _, messages := range regions {
		collectFileOperations(messages, &ops)
	}
	lists := computeFileLists(ops)
	return summary + formatFileOperations(lists), lists
}

// summarizeRegion 对一段消息生成摘要：turnPrefix 模式用轮次前缀提示词，
// 有上次摘要时走增量更新
func (c *Compactor) summarizeRegion(ctx context.Context, messages []llm.Message, previousSummary, instructions string, turnPrefix bool) (string, llm.Usage, error) {
	conversation := SerializeMessages(messages)
	var prompt string
	switch {
	case turnPrefix:
		prompt = BuildTurnPrefixPrompt(conversation)
	case previousSummary != "":
		prompt = BuildUpdatePrompt(previousSummary, conversation, instructions)
	default:
		prompt = BuildSummarizePrompt(conversation, instructions)
	}
	return c.summarizeOnce(ctx, prompt)
}

// summarizeOnce 发起一次无工具的独立 LLM 调用并取回完整文本；
// 输出上限取 min(0.8 × ReserveTokens, Model.MaxTokens)；瞬时错误按重试策略退避重试，
// 截断、报错或携带工具调用的摘要绝不返回
func (c *Compactor) summarizeOnce(ctx context.Context, prompt string) (string, llm.Usage, error) {
	modelCtx := llm.Context{
		SystemPrompt: SummarizerSystemPrompt,
		Messages:     []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: prompt}}}},
	}
	opts := llm.Options{MaxTokens: c.summaryMaxTokens()}
	produce := func() (string, llm.Usage, error) {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		events, err := c.StreamFn(streamCtx, c.Model, modelCtx, opts)
		if err != nil {
			return "", llm.Usage{}, fmt.Errorf("compaction: summary call failed: %w", err)
		}
		var final *llm.AssistantMessage
		var usage llm.Usage
		var streamErr error
		for event := range events {
			if streamErr != nil {
				continue
			}
			switch ev := event.(type) {
			case llm.AssistantMessageDoneEvent:
				if ev.Message != nil {
					final, usage = ev.Message, ev.Message.Usage
				}
			case llm.AssistantMessageErrorEvent:
				if ev.Reason == llm.StopReasonAborted {
					// 中止是终态，不得进入重试
					streamErr = fmt.Errorf("compaction: summary call aborted: %w", context.Canceled)
				} else if ev.Err != nil {
					streamErr = fmt.Errorf("compaction: summary call failed: %w", ev.Err)
				} else {
					streamErr = fmt.Errorf("compaction: summary call failed: model stream returned empty error")
				}
				cancel()
			}
		}
		if streamErr != nil {
			return "", usage, streamErr
		}
		if final == nil {
			return "", usage, fmt.Errorf("compaction: summary call failed: stream ended without a final message")
		}
		if err := validateSummary(final); err != nil {
			return "", usage, err
		}
		text := summaryText(final.Content)
		if text == "" {
			return "", usage, fmt.Errorf("compaction: empty summary")
		}
		if usage.Cost == nil {
			usage.Cost = llm.PriceUsage(c.Model, usage)
		}
		return text, usage, nil
	}
	return retrySummarize(ctx, c.Retry, produce)
}

// summaryMaxTokens 计算摘要调用的输出上限：预留量的八成与模型输出上限取小者
func (c *Compactor) summaryMaxTokens() int {
	maxTokens := c.Config.ReserveTokens * 4 / 5
	if modelMax := c.Model.MaxTokens; modelMax > 0 && modelMax < maxTokens {
		maxTokens = modelMax
	}
	return maxTokens
}

func summaryText(contents []llm.Content) string {
	var texts []string
	for _, c := range contents {
		if t, ok := c.(llm.TextContent); ok && t.Text != "" {
			texts = append(texts, t.Text)
		}
	}
	return strings.Join(texts, "\n")
}
