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
	for i := range cloned.Calls {
		if source := g.Calls[i].Codemode; source != nil {
			view := *source
			view.Calls = append([]CodemodeCallView(nil), source.Calls...)
			view.Summary.Groups = append([]CodemodeToolCountView(nil), source.Summary.Groups...)
			for j := range view.Calls {
				if duration := source.Calls[j].DurationMS; duration != nil {
					view.Calls[j].DurationMS = new(*duration)
				}
			}
			if duration := source.Output.DurationMS; duration != nil {
				view.Output.DurationMS = new(*duration)
			}
			cloned.Calls[i].Codemode = &view
		}
	}
	cloned.Thinking = append([]ThinkingBlockView(nil), g.Thinking...)
	return &cloned
}

// toolRenderState 汇总当前的展开状态
func (m *Model) toolRenderState() toolRenderState {
	state := toolRenderState{allExpanded: m.transcript.showAllTools, showThinking: m.transcript.showThinking, hideTools: m.transcript.hideTools, groups: m.transcript.expandedGroups, calls: m.transcript.expandedCalls, thoughts: m.transcript.expandedThoughts}
	return state
}

// renderToolGroup 渲染一条消息里的工具组，并把点击命中记到 m.transcript.hits
func (m *Model) renderToolGroup(group *ToolGroupView, width int) string {
	if group == nil {
		return ""
	}
	now := time.Now()
	if m.clock != nil {
		now = m.clock()
	}
	view, hits := renderToolRows(group, width, m.toolRenderState(), now)
	m.transcript.hits = hits
	return view
}

// expandSignature 是单击展开状态的稳定指纹，供渲染缓存判断是否需要重画
func (m *Model) expandSignature() string {
	var builder strings.Builder
	keys := make([]string, 0, len(m.transcript.expandedGroups))
	for key := range m.transcript.expandedGroups {
		keys = append(keys, "g"+key)
	}
	for key := range m.transcript.expandedCalls {
		keys = append(keys, "c"+key)
	}
	for key := range m.transcript.expandedThoughts {
		keys = append(keys, "t"+key)
	}
	sort.Strings(keys)
	builder.WriteString(strings.Join(keys, "\x00"))
	return builder.String()
}

// toggleToolHit 切换屏幕上某一行对应的展开状态，返回是否命中
func (m *Model) toggleToolHit(line int) bool {
	for _, hit := range m.transcript.hits {
		if line < hit.Start || line > hit.End {
			continue
		}
		switch hit.Kind {
		case "group":
			m.transcript.expandedGroups[hit.Key] = !m.transcript.expandedGroups[hit.Key]
		case "call":
			m.transcript.expandedCalls[hit.Key] = !m.transcript.expandedCalls[hit.Key]
		case "thinking":
			m.transcript.expandedThoughts[hit.Key] = !m.transcript.expandedThoughts[hit.Key]
		default:
			return false
		}
		m.refreshTranscript()
		return true
	}
	return false
}
