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

// 输出行最多 3 行,与 codex 的 MAX_RECENT_CHUNKS 一致
func TestShellPanelCapsOutputLines(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsMsg{Shells: []ShellView{{
		Command: "sleep 1", Status: "running",
		OutputTail: []string{"a", "b", "c", "d", "e"},
	}}})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	plain := ansi.Strip(m.renderMessage(*message))
	var outputLines []string
	for line := range strings.SplitSeq(plain, "\n") {
		if strings.Contains(line, "↳") || strings.HasPrefix(strings.TrimSpace(line), "a") ||
			strings.HasPrefix(strings.TrimSpace(line), "b") || strings.HasPrefix(strings.TrimSpace(line), "c") ||
			strings.HasPrefix(strings.TrimSpace(line), "d") || strings.HasPrefix(strings.TrimSpace(line), "e") {
			outputLines = append(outputLines, line)
		}
	}
	if len(outputLines) != 3 {
		t.Fatalf("output lines = %d (%q), want 3", len(outputLines), outputLines)
	}
	// 保留末尾三行
	joined := strings.Join(outputLines, "\n")
	if !strings.Contains(joined, "c") || !strings.Contains(joined, "d") || !strings.Contains(joined, "e") {
		t.Fatalf("output = %q, want the last three lines", joined)
	}
	if strings.Contains(joined, "↳ a") || strings.Contains(joined, "↳ b") {
		t.Fatalf("output = %q, want the first two lines dropped", joined)
	}
}

// 只有第一行输出带 ↳,后续行用对齐的空格
func TestShellPanelArrowOnlyOnFirstOutputLine(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsMsg{Shells: []ShellView{{
		Command: "sleep 1", Status: "running",
		OutputTail: []string{"first", "second", "third"},
	}}})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	plain := ansi.Strip(m.renderMessage(*message))
	lines := strings.Split(plain, "\n")
	var outputLines []string
	for _, line := range lines {
		if strings.Contains(line, "first") || strings.Contains(line, "second") || strings.Contains(line, "third") {
			outputLines = append(outputLines, line)
		}
	}
	if len(outputLines) != 3 {
		t.Fatalf("output lines = %d (%q), want 3", len(outputLines), outputLines)
	}
	if !strings.Contains(outputLines[0], "↳") {
		t.Fatalf("first output line = %q, want the arrow prefix", outputLines[0])
	}
	for _, line := range outputLines[1:] {
		if strings.Contains(line, "↳") {
			t.Fatalf("line = %q, want no arrow on later output lines", line)
		}
	}
	// 后续行缩进与第一行的正文起点对齐:↳ 占两列,故用 6 个空格
	if !strings.HasPrefix(outputLines[0], "    ↳ first") {
		t.Fatalf("first output line = %q, want %q", outputLines[0], "    ↳ first")
	}
	if !strings.HasPrefix(outputLines[1], "      second") {
		t.Fatalf("second output line = %q, want %q", outputLines[1], "      second")
	}
}

// 超过 16 个 shell 时给出剩余数量提示
func TestShellPanelCapsProcessCount(t *testing.T) {
	m := shellPanelModel(t)
	shells := make([]ShellView, 20)
	for i := range shells {
		shells[i] = ShellView{Command: "cmd", Status: "running"}
	}
	m.Update(ShellsMsg{Shells: shells})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	plain := ansi.Strip(m.renderMessage(*message))
	if !strings.Contains(plain, "... and 4 more running") {
		t.Fatalf("panel = %q, want the remaining-count hint", plain)
	}
}

// 截断提示与输出行同缩进，且不再重复箭头
func TestShellPanelTruncationHintHasNoArrow(t *testing.T) {
	m := shellPanelModel(t)
	m.Update(ShellsMsg{Shells: []ShellView{{
		Command: "sleep 1", Status: "running",
		OutputTail: []string{"first"}, Truncated: true,
	}}})
	m.refreshTranscript()

	message := lastMessageWithRole(m, "shells")
	plain := ansi.Strip(m.renderMessage(*message))
	if strings.Contains(plain, "↳ output truncated") {
		t.Fatalf("panel = %q, want no arrow on the truncation hint", plain)
	}
	if !strings.Contains(plain, "      output truncated") {
		t.Fatalf("panel = %q, want the hint aligned with the output column", plain)
	}
	// 整个面板只出现一次箭头
	if count := strings.Count(plain, "↳"); count != 1 {
		t.Fatalf("arrow count = %d, want 1", count)
	}
}
