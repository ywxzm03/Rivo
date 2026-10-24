package tui

import (
	"strings"
	"time"
)

// statusSpinner 是状态栏的工作指示；工具行改用 ⦁ 的呼吸，不共用它
var statusSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func cloneToolGroup(group *ToolGroupView) *ToolGroupView {
	if group == nil {
		return nil
	}
	cloned := *group
	cloned.Calls = append([]ToolCallView(nil), group.Calls...)
	cloned.Thinking = append([]ThinkingBlockView(nil), group.Thinking...)
	return &cloned
}

// renderToolGroup 用“全部收起/全部展开”两种状态渲染，供测试与预览直接调用
func renderToolGroup(group *ToolGroupView, width int, expanded bool, now time.Time) string {
	return renderToolGroupWithState(group, width, toolRenderState{allExpanded: expanded}, now)
}

// renderToolGroupWithState 渲染工具组并丢掉点击命中
func renderToolGroupWithState(group *ToolGroupView, width int, state toolRenderState, now time.Time) string {
	view, _ := renderToolRows(group, width, state, now)
	return view
}

// toolRenderState 汇总当前的展开状态
func (m *Model) toolRenderState() toolRenderState {
	state := toolRenderState{allExpanded: m.showAllTools, showThinking: m.showThinking, groups: m.expandedGroups, calls: m.expandedCalls, thoughts: m.expandedThoughts}
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
	sortStrings(keys)
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
