package tui

import (
	"sort"
	"strings"
	"time"
)

// Clone 深拷贝调用与思考切片，投影侧改副本时不影响已发出的消息
func (g *ToolGroupView) Clone() *ToolGroupView {
	if g == nil {
		return nil
	}
	cloned := *g
	cloned.Calls = append([]ToolCallView(nil), g.Calls...)
	cloned.Thinking = append([]ThinkingBlockView(nil), g.Thinking...)
	return &cloned
}

// toolRenderState 汇总当前的展开状态
func (m *Model) toolRenderState() toolRenderState {
	state := toolRenderState{allExpanded: m.showAllTools, showThinking: m.showThinking, hideTools: m.hideTools, groups: m.expandedGroups, calls: m.expandedCalls, thoughts: m.expandedThoughts}
	return state
}

// renderToolGroup 渲染一条消息里的工具组，并把点击命中记到 m.lastToolHits
func (m *Model) renderToolGroup(group *ToolGroupView, width int) string {
	if group == nil {
		return ""
	}
	now := time.Now()
	if m.clock != nil {
		now = m.clock()
	}
	view, hits := renderToolRows(group, width, m.toolRenderState(), now)
	m.lastToolHits = hits
	return view
}

// expandSignature 是单击展开状态的稳定指纹，供渲染缓存判断是否需要重画
func (m *Model) expandSignature() string {
	var builder strings.Builder
	keys := make([]string, 0, len(m.expandedGroups))
	for key := range m.expandedGroups {
		keys = append(keys, "g"+key)
	}
	for key := range m.expandedCalls {
		keys = append(keys, "c"+key)
	}
	for key := range m.expandedThoughts {
		keys = append(keys, "t"+key)
	}
	sort.Strings(keys)
	builder.WriteString(strings.Join(keys, "\x00"))
	return builder.String()
}

// toggleToolHit 切换屏幕上某一行对应的展开状态，返回是否命中
func (m *Model) toggleToolHit(line int) bool {
	for _, hit := range m.lastToolHits {
		if line < hit.Start || line > hit.End {
			continue
		}
		switch hit.Kind {
		case "group":
			m.expandedGroups[hit.Key] = !m.expandedGroups[hit.Key]
		case "call":
			m.expandedCalls[hit.Key] = !m.expandedCalls[hit.Key]
		case "thinking":
			m.expandedThoughts[hit.Key] = !m.expandedThoughts[hit.Key]
		default:
			return false
		}
		m.refreshTranscript()
		return true
	}
	return false
}
