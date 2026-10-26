// Package testutil 提供跨包复用的测试辅助：
// 脚本化流函数、可推进时钟、隔离环境与 provider 契约断言。
package testutil

import (
	"context"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// TextReplies 返回按调用顺序吐出文本的流函数，用完后重复最后一条
func TextReplies(replies ...string) llm.StreamFn {
	index := 0
	return func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		text := "ok"
		if len(replies) > 0 {
			if index < len(replies) {
				text = replies[index]
			} else {
				text = replies[len(replies)-1]
			}
		}
		index++
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content: []llm.Content{llm.TextContent{Text: text}},
		}}
		close(events)
		return events, nil
	}
}

// Messages 返回按调用顺序吐出完整回复的流函数，用完后重复最后一条
func Messages(replies ...llm.AssistantMessage) llm.StreamFn {
	index := 0
	return func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		message := llm.AssistantMessage{}
		if len(replies) > 0 {
			if index < len(replies) {
				message = replies[index]
			} else {
				message = replies[len(replies)-1]
			}
		}
		index++
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &message}
		close(events)
		return events, nil
	}
}

// Counting 包装流函数并统计调用次数
func Counting(stream llm.StreamFn) (llm.StreamFn, *int) {
	calls := 0
	return func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		calls++
		return stream(ctx, model, c, opts)
	}, &calls
}

// Blocked 返回一个在 release 关闭前不产出事件的流函数，用于覆盖取消与并发路径
func Blocked(started chan struct{}, release chan struct{}) llm.StreamFn {
	return func(ctx context.Context, model llm.Model, c llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		events := make(chan llm.AssistantMessageEvent)
		go func() {
			defer close(events)
			if started != nil {
				select {
				case started <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
				Content: []llm.Content{llm.TextContent{Text: "done"}},
			}}
		}()
		return events, nil
	}
}
