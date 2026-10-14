package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// aborted 消息渲染为 partial 正文 + 红色固定提示行
func TestAbortedMessageRendering(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{ID: "a", Role: "assistant", Text: "partial answer", Aborted: true}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(rendered, "partial answer") {
		t.Fatalf("partial content lost: %q", rendered)
	}
	if !strings.Contains(rendered, "Operation aborted") {
		t.Fatalf("abort hint missing: %q", rendered)
	}
}

// 空 partial 的 aborted 消息只显示提示行，不渲染空正文
func TestAbortedMessageRenderingEmptyPartial(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{ID: "a", Role: "assistant", Aborted: true}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if strings.TrimSpace(rendered) != "Operation aborted" {
		t.Fatalf("empty partial should render only hint: %q", rendered)
	}
}

// 正常 assistant 消息不受 Aborted 字段影响
func TestNormalAssistantRenderingUnchanged(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{ID: "a", Role: "assistant", Text: "done"}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if strings.Contains(rendered, "Operation aborted") {
		t.Fatalf("normal message should not show abort hint: %q", rendered)
	}
}

// 带 ToolGroup 的 aborted 消息：工具组渲染在前，尾部追加红色提示
func TestAbortedMessageRenderingWithToolGroup(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{
		ID: "a", Role: "assistant", Aborted: true,
		ToolGroup: &ToolGroupView{ID: "g1", Calls: []ToolCallView{{ID: "c1", Name: "bash", Status: ToolInterrupted}}, Sealed: true},
	}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(rendered, "Operation aborted") {
		t.Fatalf("abort hint missing: %q", rendered)
	}
	if !strings.Contains(rendered, "bash") {
		t.Fatalf("tool group lost on aborted render: %q", rendered)
	}
}

// 带 partial 正文与 ToolGroup 并存的 aborted 消息：正文不丢
func TestAbortedMessageRenderingTextAndGroup(t *testing.T) {
	m := testModel(80, 24)
	m.state.Messages = []Message{{
		ID: "a", Role: "assistant", Text: "partial answer", Aborted: true,
		ToolGroup: &ToolGroupView{ID: "g1", Calls: []ToolCallView{{ID: "c1", Name: "bash", Status: ToolInterrupted}}, Sealed: true},
	}}
	rendered := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if !strings.Contains(rendered, "partial answer") || !strings.Contains(rendered, "Operation aborted") || !strings.Contains(rendered, "bash") {
		t.Fatalf("aborted render incomplete: %q", rendered)
	}
}
