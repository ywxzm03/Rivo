package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// aborted 消息渲染为 partial 正文 + 弱化色固定提示行
func TestAbortedMessageRendering(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{ID: "a", Role: "assistant", Text: "partial answer", Aborted: true}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(rendered, "partial answer") {
		t.Fatalf("partial content lost: %q", rendered)
	}
	if !strings.Contains(rendered, "Conversation interrupted") {
		t.Fatalf("abort hint missing: %q", rendered)
	}
}

// 空 partial 的 aborted 消息只显示提示行，不渲染空正文
func TestAbortedMessageRenderingEmptyPartial(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{ID: "a", Role: "assistant", Aborted: true}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if strings.TrimSpace(rendered) != "■ Conversation interrupted" {
		t.Fatalf("empty partial should render only hint: %q", rendered)
	}
}

// 正常 assistant 消息不受 Aborted 字段影响
func TestNormalAssistantRenderingUnchanged(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{ID: "a", Role: "assistant", Text: "done"}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if strings.Contains(rendered, "Conversation interrupted") {
		t.Fatalf("normal message should not show abort hint: %q", rendered)
	}
}

// 带 ToolGroup 的 aborted 消息：工具组渲染在前，尾部追加中断提示
func TestAbortedMessageRenderingWithToolGroup(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{
		ID: "a", Role: "assistant", Aborted: true,
		ToolGroup: &ToolGroupView{ID: "g1", Calls: []ToolCallView{{ID: "c1", Name: "bash", Status: ToolInterrupted, ArgsComplete: true}}},
	}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(rendered, "Conversation interrupted") {
		t.Fatalf("abort hint missing: %q", rendered)
	}
	if !strings.Contains(rendered, "Bash") {
		t.Fatalf("tool group lost on aborted render: %q", rendered)
	}
}

// 带 partial 正文与 ToolGroup 并存的 aborted 消息：正文不丢
func TestAbortedMessageRenderingTextAndGroup(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{
		ID: "a", Role: "assistant", Text: "partial answer", Aborted: true,
		ToolGroup: &ToolGroupView{ID: "g1", Calls: []ToolCallView{{ID: "c1", Name: "bash", Status: ToolInterrupted, ArgsComplete: true}}},
	}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(rendered, "partial answer") || !strings.Contains(rendered, "Conversation interrupted") || !strings.Contains(rendered, "Bash") {
		t.Fatalf("aborted render incomplete: %q", rendered)
	}
}

// 中断提示的方块缩进一格，正文从第三列开始，与 assistant 正文缩进对齐
func TestAbortNoticeGeometry(t *testing.T) {
	rendered := ansi.Strip(abortNotice())
	if rendered != " ■ Conversation interrupted" {
		t.Fatalf("abort notice = %q", rendered)
	}
}

// 提示行的圆点与中断方块同列，正文从第三列开始
func TestNoticeRenderingUsesBulletPrefix(t *testing.T) {
	m := testModel(80, 24)
	rendered := ansi.Strip(m.renderMessage(Message{ID: "n", Role: "notice", Text: "Model changed to m"}))
	if rendered != " • Model changed to m" {
		t.Fatalf("notice = %q", rendered)
	}
}

// 压缩摘要不属于命令提示，保持无前缀
func TestSummaryRenderingHasNoBullet(t *testing.T) {
	m := testModel(80, 24)
	rendered := ansi.Strip(m.renderMessage(Message{ID: "s", Role: "summary", Text: "Context compacted"}))
	if rendered != "Context compacted" {
		t.Fatalf("summary = %q", rendered)
	}
}
