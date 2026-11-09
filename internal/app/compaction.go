package app

import (
	"context"
	"errors"
	"sync"

	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/ctxview"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

// compactionController 负责压缩编排与溢出恢复。
// 它持有压缩相关的全部可变状态，让 Coordinator 只保留运行与落盘边界
type compactionController struct {
	m    *session.SessionManager
	emit func(llm.AgentEvent) error
	// compactor 取当前压缩器，nil 表示禁用压缩
	compactor func() *compaction.Compactor

	// mu 保护 compacting；入队边界与结束事件共用同一临界区
	mu         sync.Mutex
	compacting bool

	// pendingRecovery 记录正在收场的运行是否以可恢复的溢出结束
	pendingRecovery bool
	// recovered 限制每次 Run 最多做一次溢出恢复
	recovered bool
}

func newCompactionController(m *session.SessionManager, emit func(llm.AgentEvent) error, compactor func() *compaction.Compactor) *compactionController {
	return &compactionController{m: m, emit: emit, compactor: compactor}
}

// resetRecovery 清空恢复状态
func (c *compactionController) resetRecovery() {
	c.pendingRecovery, c.recovered = false, false
}

// isCompacting 供入队边界判断是否处于压缩中
func (c *compactionController) isCompacting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.compacting
}

// setCompacting 更新压缩状态，供入队边界与结束事件共享同一临界区
func (c *compactionController) setCompacting(value bool) {
	c.mu.Lock()
	c.compacting = value
	c.mu.Unlock()
}

// needsRecovery 报告当前运行是否以可恢复的溢出结束
func (c *compactionController) needsRecovery() bool {
	return c.pendingRecovery && !c.recovered && c.compactor() != nil
}

// compact 先计算候选摘要，落盘成功后才发布新投影；失败时会话与内存保持一致
func (c *compactionController) compact(ctx context.Context, reason, instructions string) error {
	if c.compactor() == nil {
		return errors.New("session: context compaction not enabled")
	}
	c.setCompacting(true)
	defer c.setCompacting(false)
	proj := c.m.BuildContext()
	tokensBefore := contextTokens(proj)
	if err := c.emit(llm.CompactionStartEvent{Reason: reason, TokensBefore: tokensBefore}); err != nil {
		return err
	}
	result, err := c.compactor().Compact(ctx, proj, instructions)
	if err != nil {
		_ = c.emit(llm.CompactionEndEvent{Reason: reason, TokensBefore: tokensBefore, Error: err.Error()})
		return err
	}
	var usage *llm.Usage
	if result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 0 {
		usage = &result.Usage
	}
	if err := c.m.AppendCompactionWithDetails(result.Summary, result.FirstKeptSourceID, tokensBefore, llm.CurrentSystemMessage(proj.Messages()), usage, &result.FileLists); err != nil {
		_ = c.emit(llm.CompactionEndEvent{Reason: reason, TokensBefore: tokensBefore, Error: err.Error()})
		return err
	}
	// 落盘成功后重建投影并提交内存；此后通知失败不影响状态
	rebuilt := c.m.BuildContext()
	// 结束事件表示 compaction 边界已关闭：先解除 compacting，TUI 才能按原序转发暂存输入
	c.setCompacting(false)
	_ = c.emit(llm.CompactionEndEvent{
		Reason:           reason,
		Summary:          result.Summary,
		KeptMessageCount: result.KeptMessageCount,
		TokensBefore:     tokensBefore,
		TokensAfter:      compaction.EstimateContextTokens(rebuilt.Messages()),
		Usage:            result.Usage,
	})
	return nil
}

// omitLastAttempt 为最后落盘的失败尝试追加 context_edit 省略
func (c *compactionController) omitLastAttempt() error {
	target := c.m.LastEntryID()
	if target == "" {
		return errors.New("session: no failed attempt to omit")
	}
	return c.m.AppendContextEdit(target, nil)
}

// shouldCompact 在会话投影上评估静默溢出与阈值
func (c *compactionController) shouldCompact(proj ctxview.ContextView) (reason string, needed bool) {
	if c.compactor() == nil {
		return "", false
	}
	window := c.compactor().Config.ContextWindow
	if window <= 0 {
		return "", false
	}
	// 静默溢出优先：响应成功但最后一条 assistant 的 usage 已超窗
	if !proj.UsageStale {
		if msg := lastAssistant(proj.Messages()); msg != nil && msg.Usage.InputTokens > window {
			return CompactionReasonOverflow, true
		}
	}
	if compaction.ShouldCompact(contextTokens(proj), window, c.compactor().Config.ReserveTokens) {
		return CompactionReasonAuto, true
	}
	return "", false
}

// contextTokens 按陈旧标记选择 token 估算方式：陈旧时尾部 usage 反映压缩前的大上下文，
// 改用纯消息体估算，防止压缩刚结束就重复触发
func contextTokens(proj ctxview.ContextView) int {
	if proj.UsageStale {
		return compaction.EstimateContextTokens(proj.Messages())
	}
	return compaction.ContextTokens(proj.Messages())
}

func lastAssistant(messages []llm.Message) *llm.AssistantMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		if msg, ok := messages[i].(llm.AssistantMessage); ok {
			return &msg
		}
	}
	return nil
}
