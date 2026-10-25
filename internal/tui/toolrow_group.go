package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// toolRenderState 是一次渲染的展开状态：
// allExpanded 对应 Ctrl+O，groups/calls 记录单击打开的分组与单条调用，thoughts 记录展开的思考块
type toolRenderState struct {
	allExpanded bool
	// showThinking 是 Ctrl+T 的全局开关，展开每个思考块
	showThinking bool
	// hideTools 是 Ctrl+Q 的遮罩，整段只剩一行隐藏提示
	hideTools bool
	groups    map[string]bool
	calls     map[string]bool
	thoughts  map[string]bool
}

func (s toolRenderState) callExpanded(group *ToolGroupView, call ToolCallView) bool {
	return s.allExpanded || s.calls[group.ID+"/"+call.ID]
}

func (s toolRenderState) groupExpanded(group *ToolGroupView) bool {
	return s.allExpanded || s.groups[group.ID]
}

// thinkingExpanded 只认显式展开：Ctrl+O 只开工具行，思考归 Ctrl+T 与单击
func (s toolRenderState) thinkingExpanded(key string) bool {
	return s.showThinking || s.thoughts[key]
}

// toolHit 是一段可点击区域：屏幕上 [Start, End] 行对应哪个分组、调用或思考块
type toolHit struct {
	Kind  string // group / call / thinking
	Key   string
	Start int
	End   int
}

// thinkingKey 让每一轮的思考块各自可展开
func thinkingKey(group *ToolGroupView, round int) string {
	return fmt.Sprintf("%s/r%d", group.ID, round)
}

// thinkingForRound 取某一轮的思考块，按 content index 排序
func thinkingForRound(group *ToolGroupView, round int) []ThinkingBlockView {
	blocks := make([]ThinkingBlockView, 0, len(group.Thinking))
	for _, block := range group.Thinking {
		if block.Round == round && strings.TrimSpace(block.Text) != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// renderToolRows 渲染整个工具组，并返回点击命中区间
func renderToolRows(group *ToolGroupView, width int, state toolRenderState, now time.Time) (string, []toolHit) {
	if group == nil {
		return "", nil
	}
	width = max(1, width)
	if len(group.Calls) == 0 && len(thinkingBlocks(group)) == 0 {
		return "", nil
	}
	// 隐藏状态只留一行提示，调用与思考都不上屏，也不产生点击命中
	if state.hideTools {
		return hiddenToolRowsLine(group, width), nil
	}
	lines, hits := renderToolRowCalls(group, width, state, now)
	return strings.Join(lines, "\n"), hits
}

// hiddenToolRowsLine 汇总被隐藏的调用数与思考块数，并提示恢复键
func hiddenToolRowsLine(group *ToolGroupView, width int) string {
	parts := make([]string, 0, 2)
	if count := len(group.Calls); count > 0 {
		parts = append(parts, countNoun(count, "tool call", "tool calls"))
	}
	if count := len(thinkingBlocks(group)); count > 0 {
		parts = append(parts, countNoun(count, "thinking block", "thinking blocks"))
	}
	// 前导空格与调用行的 ` ⦁ ` 对齐
	label := " • " + strings.Join(parts, " and ") + " hidden (ctrl+q)"
	return colored(faintColor, ansi.Truncate(label, width, "…"))
}

// thinkingBlocks 返回组里所有非空思考块
func thinkingBlocks(group *ToolGroupView) []ThinkingBlockView {
	blocks := make([]ThinkingBlockView, 0, len(group.Thinking))
	for _, block := range group.Thinking {
		if strings.TrimSpace(block.Text) != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// renderToolRowCalls 按轮次依次画：每轮的思考排在该轮调用之前，
// 同一轮里连续的只读调用折成一行摘要，其余调用各自成块
func renderToolRowCalls(group *ToolGroupView, width int, state toolRenderState, now time.Time) ([]string, []toolHit) {
	var lines []string
	var hits []toolHit
	emitted := make(map[int]bool, len(group.Thinking))
	// emitThinking 把某一轮的思考画一次；没有调用的轮次在末尾补画
	emitThinking := func(round int) {
		if emitted[round] {
			return
		}
		blocks := thinkingForRound(group, round)
		if len(blocks) == 0 {
			return
		}
		emitted[round] = true
		key := thinkingKey(group, round)
		start := len(lines)
		lines = append(lines, renderToolRowThinking(blocks, key, width, state)...)
		hits = append(hits, toolHit{Kind: "thinking", Key: key, Start: start, End: len(lines) - 1})
	}

	for start := 0; start < len(group.Calls); {
		round := group.Calls[start].Round
		emitThinking(round)
		// 1.同一轮里连续的只读调用合成一组，轮次变化时断开以便插入下一轮思考
		end := start
		for end < len(group.Calls) && group.Calls[end].Round == round && visibleCall(group.Calls[end]) &&
			lookKindOf(group.Calls[end]) != lookNone {
			end++
		}
		if end == start {
			call := group.Calls[start]
			start++
			// 名字还没到的调用先不画，避免出现只有状态点的空行
			if !visibleCall(call) {
				continue
			}
			begin := len(lines)
			lines = append(lines, renderToolRow(call, width, state.callExpanded(group, call), now)...)
			hits = append(hits, toolHit{Kind: "call", Key: call.ID, Start: begin, End: len(lines) - 1})
			continue
		}
		calls := group.Calls[start:end]
		begin := len(lines)
		lines = append(lines, renderToolRowExploration(group, calls, width, state, now)...)
		hits = append(hits, toolHit{Kind: "group", Key: group.ID, Start: begin, End: len(lines) - 1})
		start = end
	}
	// 2.最后一轮只有思考没有调用时，思考仍然留在末尾
	for _, block := range group.Thinking {
		emitThinking(block.Round)
	}
	return lines, hits
}

// visibleCall 判断调用行是否该出现。
// 参数还在流式写入时先不渲染:此时只有一个工具名,没有可读内容,
// 会闪出一行"有工具无参数"的空壳
func visibleCall(call ToolCallView) bool {
	return call.Name != "" && call.ArgsComplete
}

// renderToolRowExploration 画一组只读调用：收起时一行摘要，展开时是各自的调用行
func renderToolRowExploration(group *ToolGroupView, calls []ToolCallView, width int, state toolRenderState, now time.Time) []string {
	expanded := state.groupExpanded(group)
	summary := summarizeToolRowGroup(calls)
	dot := toolRowGroupDot(calls, now)
	title := " " + dot + " " + boldText(summary)
	if !expanded {
		title += " " + colored(dimColor, rowExpandHint)
	}
	lines := hardWrap(title, width)
	if hint := groupHint(calls); hint != "" {
		lines = append(lines, toolRowSentence(hint, width)...)
	}
	if !expanded {
		for _, failure := range groupFailures(calls) {
			lines = append(lines, toolRowSentence(failure, width)...)
		}
		return lines
	}
	for _, call := range calls {
		lines = append(lines, renderToolRow(call, width, state.callExpanded(group, call), now)...)
	}
	return lines
}

// toolRowGroupDot 是摘要行的状态点：有调用在跑就呼吸，有失败就红
func toolRowGroupDot(calls []ToolCallView, now time.Time) string {
	switch {
	case groupRunning(calls):
		// 摘要行只关心“还在动”，用一个运行中的调用取点即可
		return toolRowDot(ToolCallView{Status: ToolPending}, now)
	case len(groupFailures(calls)) > 0:
		return colored(redColor, "⦁")
	default:
		return colored(greenColor, "⦁")
	}
}

// renderToolRowThinking 画一轮的思考：默认只有一行 Thinking...，展开后是完整正文
func renderToolRowThinking(blocks []ThinkingBlockView, key string, width int, state toolRenderState) []string {
	// 与其他行一样整体缩进一格，状态点所在列留给工具行
	lines := []string{" " + boldItalicText("Thinking...")}
	if !state.thinkingExpanded(key) {
		return lines
	}
	available := max(1, width-ansi.StringWidth(rowIndent))
	for _, block := range blocks {
		for _, row := range strings.Split(strings.TrimRight(block.Text, "\n"), "\n") {
			// 每一行都带缩进，换行后的续行也要保持同一列
			for _, wrapped := range strings.Split(ansi.Hardwrap(row, available, true), "\n") {
				lines = append(lines, rowIndent+colored(thinkingColor, wrapped))
			}
		}
	}
	return lines
}
