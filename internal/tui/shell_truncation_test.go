package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// /stop 受理文案顶格呈现,不带圆点
func TestStopNoticeHasNoBullet(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsStoppedMsg{Stopped: 1})

	message := lastMessageWithRole(m, "summary")
	if message == nil {
		t.Fatal("no stop notice appended")
	}
	rendered := ansi.Strip(m.renderMessage(*message))
	if rendered != "Stopping all background shells." {
		t.Fatalf("stop notice = %q, want it flush left without a bullet", rendered)
	}
}

// 无 shell 可停时同样不带圆点
func TestStopNoticeEmptyHasNoBullet(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsStoppedMsg{Stopped: 0})

	message := lastMessageWithRole(m, "summary")
	if message == nil {
		t.Fatal("no stop notice appended")
	}
	rendered := ansi.Strip(m.renderMessage(*message))
	if strings.Contains(rendered, "•") {
		t.Fatalf("stop notice = %q, want no bullet", rendered)
	}
}
