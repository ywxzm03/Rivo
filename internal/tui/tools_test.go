package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestShortenToolPathRequiresHomeBoundary(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	separator := string(os.PathSeparator)
	if got := shortenToolPath(home); got != "~" {
		t.Fatalf("home path = %q, want ~", got)
	}
	if got := shortenToolPath(home + separator + "project"); got != "~"+separator+"project" {
		t.Fatalf("child path = %q, want home-relative path", got)
	}
	if got := shortenToolPath(home + "-backup/project"); got != home+"-backup/project" {
		t.Fatalf("lookalike path = %q, want unchanged", got)
	}
}

func testToolGroup(count int) *ToolGroupView {
	group := &ToolGroupView{ID: "group"}
	for i := 0; i < count; i++ {
		group.Calls = append(group.Calls, ToolCallView{ID: string(rune('a' + i)), Name: "bash", Arguments: `{"command":"cmd"}`, Result: "result", Status: ToolSuccess, ArgsComplete: true})
	}
	return group
}

// 每条调用都单独成块，不再只留最近三条，也没有组头状态行
func TestToolRowGroupShowsEveryCall(t *testing.T) {
	group := testToolGroup(5)
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "checking the failing assertion"}}
	got := ansi.Strip(renderGroupCollapsed(group, 120, false, time.UnixMilli(1000)))
	if strings.Count(got, "⦁ Bash(cmd)") != 5 {
		t.Fatalf("group rendered %d calls, want 5:\n%s", strings.Count(got, "⦁ Bash(cmd)"), got)
	}
	for _, unwanted := range []string{"tools done", "… +", "thinking:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("group still shows %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "Thinking...") {
		t.Fatalf("thinking line missing:\n%s", got)
	}
}

// 思考是否结束都不改变工具行的外观：只有一行 Thinking...
func TestEmptySealedToolGroupIsNotRendered(t *testing.T) {
	group := &ToolGroupView{ID: "empty"}
	if got := renderGroupCollapsed(group, 80, false, time.UnixMilli(1000)); got != "" {
		t.Fatalf("empty sealed group = %q", got)
	}
}

// 展开后命令输出给全文；思考由 Ctrl+T 单独展开，不随工具行一起打开
func TestExpandedToolGroupShowsFullOutput(t *testing.T) {
	group := testToolGroup(1)
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "full thinking\nsecond line"}}
	group.Calls[0].Result = strings.Repeat("line\n", 20)
	got := ansi.Strip(renderGroupCollapsed(group, 80, true, time.UnixMilli(1000)))
	if strings.Contains(got, "ctrl+o") {
		t.Fatalf("expanded output was folded:\n%s", got)
	}
	if strings.Count(got, "line") < 20 {
		t.Fatalf("expanded group lost output:\n%s", got)
	}
	if !strings.Contains(got, "Thinking...") || strings.Contains(got, "full thinking") {
		t.Fatalf("Ctrl+O also opened the thinking block:\n%s", got)
	}
	expandedThinking := ansi.Strip(renderGroupState(group, 80, toolRenderState{allExpanded: true, showThinking: true}, time.UnixMilli(1000)))
	if !strings.Contains(expandedThinking, "full thinking") || !strings.Contains(expandedThinking, "second line") {
		t.Fatalf("Ctrl+T did not open the thinking block:\n%s", expandedThinking)
	}
}

// Ctrl+O 展开命令输出与思考正文，收起时只有结果句和 Thinking...
func TestModelCtrlOTogglesToolOutputAndThinking(t *testing.T) {
	group := testToolGroup(1)
	group.Calls[0].Result = "first\nsecond\nthird\nfourth\nfifth"
	group.Thinking = []ThinkingBlockView{{Index: 1, Text: "reasoning **detail**"}}
	m := New(Snapshot{Messages: []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}}, Handlers{})
	m.width = 80
	compact := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(compact, "Thinking...") || strings.Contains(compact, "fifth") || strings.Contains(compact, "reasoning") {
		t.Fatalf("compact group = %q", compact)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	expanded := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(expanded, "fifth") {
		t.Fatalf("Ctrl+O did not open the output: %q", expanded)
	}
	if strings.Contains(expanded, "reasoning") {
		t.Fatalf("Ctrl+O also opened thinking: %q", expanded)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	thought := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(thought, "reasoning **detail**") {
		t.Fatalf("Ctrl+T did not open thinking: %q", thought)
	}
}

func TestActiveToolGroupRefreshesWithWorkingSpinner(t *testing.T) {
	group := testToolGroup(1)
	group.Calls[0].Status = ToolPending
	// 已开始执行的 pending 调用才会呼吸
	group.Calls[0].StartedAt = time.UnixMilli(1)
	m := New(Snapshot{Messages: []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}}, Handlers{})
	m.width = 80
	m.busy = true
	now := time.UnixMilli(1000)
	m.clock = func() time.Time { return now }
	m.refreshTranscript()
	first := m.cache["tools"].view
	now = now.Add(100 * time.Millisecond)
	m.Update(spinner.TickMsg{})
	second := m.cache["tools"].view
	if first == second {
		t.Fatal("active tool group did not refresh on spinner tick")
	}
}

func renderGroupState(group *ToolGroupView, width int, state toolRenderState, now time.Time) string {
	view, _ := renderToolRows(group, width, state, now)
	return view
}

// renderGroupCollapsed 以全部收起或全部展开渲染，其余展开状态默认关闭
func renderGroupCollapsed(group *ToolGroupView, width int, expanded bool, now time.Time) string {
	return renderGroupState(group, width, toolRenderState{allExpanded: expanded}, now)
}

// agent 调用行显示大写 Agent,并复用通用截断规则(默认收起可展开)
func TestAgentToolRowLabelAndTruncation(t *testing.T) {
	if got := toolRowLabel("agent"); got != "Agent" {
		t.Fatalf("agent label = %q, want Agent", got)
	}
	longPrompt := strings.Repeat("请仔细检查这个文件的每一处细节。", 40)
	args := map[string]any{
		"description":   "Explore the module",
		"prompt":        longPrompt,
		"subagent_type": "explore",
	}
	collapsed := toolRowArgument("agent", args, false)
	if !strings.Contains(collapsed, "explore") || !strings.Contains(collapsed, "Explore the module") {
		t.Fatalf("collapsed agent row = %q, want type and description", collapsed)
	}
	// 收起时不能把 prompt 正文整段铺出来
	if strings.Contains(collapsed, "请仔细检查") {
		t.Fatalf("collapsed agent row leaked the prompt: %q", collapsed)
	}
	if ansi.StringWidth(collapsed) > rowArgumentChars {
		t.Fatalf("collapsed agent row width = %d, want <= %d", ansi.StringWidth(collapsed), rowArgumentChars)
	}
}

// 通用参数行同样受截断约束
func TestUnknownToolArgumentsAreTruncated(t *testing.T) {
	args := map[string]any{"blob": strings.Repeat("x", 500)}
	if got := toolRowArgument("unknown-tool", args, false); ansi.StringWidth(got) > rowArgumentChars {
		t.Fatalf("generic argument row width = %d, want <= %d", ansi.StringWidth(got), rowArgumentChars)
	}
}

// 没有工具组的消息不能沿用上一条的点击命中区
func TestPlainMessageDoesNotInheritToolHits(t *testing.T) {
	messages := []Message{
		{ID: "a", Role: "assistant", ToolGroup: &ToolGroupView{
			ID:       "g",
			Thinking: []ThinkingBlockView{{Index: 0, Round: 0, Text: "thought"}},
		}},
		{ID: "b", Role: "assistant", Text: "plain text reply"},
	}
	m := New(Snapshot{Cwd: "/repo", Messages: messages}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.refreshTranscript()

	// 只有第一条消息贡献命中区
	if len(m.transcript.hits) != 1 {
		t.Fatalf("hits = %d, want 1 (only the tool group)", len(m.transcript.hits))
	}
	if m.transcript.hits[0].Key != "g/r0" {
		t.Fatalf("hit key = %q, want g/r0", m.transcript.hits[0].Key)
	}
}

// 展开靠后的 thinking 后仍能折叠回来
func TestThinkingTogglesAcrossMessages(t *testing.T) {
	var messages []Message
	for i := 0; i < 3; i++ {
		messages = append(messages, Message{
			ID:   string(rune('a' + i)),
			Role: "assistant",
			ToolGroup: &ToolGroupView{
				ID:       string(rune('g' + i)),
				Thinking: []ThinkingBlockView{{Index: 0, Round: 0, Text: "deep thought"}},
			},
		})
	}
	m := New(Snapshot{Cwd: "/repo", Messages: messages}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.refreshTranscript()

	key := "i/r0"
	m.transcript.expandedThoughts[key] = true
	m.refreshTranscript()
	var target toolHit
	found := false
	for _, hit := range m.transcript.hits {
		if hit.Key == key {
			target, found = hit, true
		}
	}
	if !found {
		t.Fatalf("thinking key %q missing from hits", key)
	}
	if target.End == target.Start {
		t.Fatal("expanded thinking should span more than one line")
	}
	if !m.toggleToolHit(target.Start) {
		t.Fatal("toggle did not hit")
	}
	if m.transcript.expandedThoughts[key] {
		t.Fatal("thinking did not collapse after expanding")
	}
}
