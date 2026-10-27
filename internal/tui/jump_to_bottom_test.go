package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func scrollableModel(t *testing.T) *Model {
	t.Helper()
	m := testModel(80, 24)
	for i := range 40 {
		m.state.Messages = append(m.state.Messages, Message{ID: string(rune('a' + i)), Role: "assistant", Text: "scrollable line"})
	}
	m.refreshTranscript()
	m.viewport.GotoTop()
	return m
}

func TestJumpToBottomPillUsesClaudeLabelAndOverlay(t *testing.T) {
	m := scrollableModel(t)
	if !m.jumpPillVisible() {
		t.Fatal("jump pill should be visible after scrolling away from bottom")
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, jumpToBottomLabel) {
		t.Fatalf("view = %q, want %q", plain, jumpToBottomLabel)
	}
	if strings.Contains(plain, "(click)") {
		t.Fatalf("view still contains legacy click hint: %q", plain)
	}
	bounds, ok := m.jumpPillBounds()
	if !ok || bounds.y != m.viewport.Height-1 {
		t.Fatalf("pill bounds = %+v, visible=%v, want last transcript row", bounds, ok)
	}
	assertFrame(t, m)
}

func TestJumpToBottomEscapeHasPriorityWhileBusy(t *testing.T) {
	m := scrollableModel(t)
	canceled := 0
	m.busy = true
	m.handlers.Cancel = func() { canceled++ }
	press(m, tea.KeyEsc)
	if !m.viewport.AtBottom() {
		t.Fatal("Escape should jump to the bottom")
	}
	if canceled != 0 {
		t.Fatalf("Escape canceled the operation %d times, want 0", canceled)
	}
}

func TestJumpToBottomMouseClick(t *testing.T) {
	m := scrollableModel(t)
	bounds, ok := m.jumpPillBounds()
	if !ok {
		t.Fatal("jump pill should be visible before click")
	}
	m.Update(tea.MouseMsg{X: (bounds.left + bounds.right) / 2, Y: bounds.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.viewport.AtBottom() {
		t.Fatal("clicking jump pill should jump to the bottom")
	}
	if m.hasSelection() {
		t.Fatal("clicking jump pill should not create a text selection")
	}
}

func TestJumpToBottomPillHoverUsesClaudeHoverBackground(t *testing.T) {
	m := scrollableModel(t)
	bounds, ok := m.jumpPillBounds()
	if !ok {
		t.Fatal("jump pill should be visible before hover")
	}
	base := m.View()
	if !strings.Contains(base, "48;2;55;55;55") || strings.Contains(base, "48;2;70;70;70") {
		t.Fatalf("base pill background = %q, want #373737 without hover color", base)
	}
	m.Update(tea.MouseMsg{X: (bounds.left + bounds.right) / 2, Y: bounds.y, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	hovered := m.View()
	if !strings.Contains(hovered, "48;2;70;70;70") {
		t.Fatalf("hovered pill background = %q, want #464646", hovered)
	}
	m.Update(tea.MouseMsg{X: 0, Y: 0, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	if strings.Contains(m.View(), "48;2;70;70;70") {
		t.Fatal("moving away from pill should restore the base background")
	}
}

func TestJumpToBottomPillHiddenForCompletionAndSelector(t *testing.T) {
	m := scrollableModel(t)
	typeText(m, "/")
	if m.jumpPillVisible() {
		t.Fatal("completion UI should hide jump pill")
	}
	press(m, tea.KeyEsc)
	if m.composer.completionHidden == false {
		t.Fatal("Escape should dismiss completion when jump pill is hidden")
	}

	m = scrollableModel(t)
	m.openSelector("model", "")
	if m.jumpPillVisible() {
		t.Fatal("selector UI should hide jump pill")
	}
}

func TestJumpToBottomPillHiddenAtBottom(t *testing.T) {
	m := scrollableModel(t)
	m.jumpToBottom()
	if m.jumpPillVisible() {
		t.Fatal("jump pill should hide at the bottom")
	}
	if strings.Contains(ansi.Strip(m.View()), jumpToBottomLabel) {
		t.Fatal("bottom view should not contain jump pill")
	}
}
