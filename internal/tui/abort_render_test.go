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
