package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// sessionsTestModel 构造带会话加载入口的模型
func sessionsTestModel(load func() tea.Cmd) *Model {
	m := New(Snapshot{}, Handlers{Sessions: load})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func TestResumeSelectorLoadsSessionsOnDemand(t *testing.T) {
	loads := 0
	m := sessionsTestModel(func() tea.Cmd {
		loads++
		return func() tea.Msg {
			return SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/one", Summary: "One", Modified: "1m ago"}}}
		}
	})
	cmd := m.openSelector(RequestResume, "")
	if loads != 1 {
		t.Fatalf("loads = %d, want 1", loads)
	}
	if cmd == nil {
		t.Fatal("openSelector() = nil, want sessions command")
	}
	if m.composer.selector != nil {
		t.Fatal("selector opened before sessions arrived")
	}
	m.Update(cmd())
	if m.composer.selector == nil || m.composer.selector.kind != RequestResume {
		t.Fatalf("selector = %+v, want resume selector", m.composer.selector)
	}
	if len(m.sessions) != 1 || m.sessions[0].Path != "/tmp/one" {
		t.Fatalf("sessions = %+v, want loaded session", m.sessions)
	}
	if len(m.composer.selector.items) != 1 || m.composer.selector.items[0].value != "/tmp/one" {
		t.Fatalf("items = %+v, want loaded session item", m.composer.selector.items)
	}
}

func TestResumeSelectorPreservesQueryWhileLoading(t *testing.T) {
	m := sessionsTestModel(func() tea.Cmd {
		return func() tea.Msg { return SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/one", Summary: "One"}}} }
	})
	cmd := m.openSelector(RequestResume, "on")
	m.Update(cmd())
	if m.composer.selector == nil {
		t.Fatal("selector not opened")
	}
	if got := m.composer.selector.input.Value(); got != "on" {
		t.Fatalf("selector query = %q, want preserved query", got)
	}
}

func TestResumeSelectorOpensWithEmptyListAndError(t *testing.T) {
	m := sessionsTestModel(func() tea.Cmd {
		return func() tea.Msg { return SessionsMsg{Err: errors.New("boom")} }
	})
	cmd := m.openSelector(RequestResume, "")
	m.Update(cmd())
	if m.composer.selector == nil || len(m.composer.selector.items) != 0 {
		t.Fatalf("selector = %+v, want empty selector after error", m.composer.selector)
	}
	if len(m.state.Messages) == 0 || m.state.Messages[len(m.state.Messages)-1].Role != "notice" {
		t.Fatalf("messages = %+v, want error notice", m.state.Messages)
	}
}

func TestResumeSelectorWithoutLoaderUsesCurrentList(t *testing.T) {
	m := New(Snapshot{}, Handlers{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.sessions = []SessionOption{{Path: "/tmp/one", Summary: "One"}}
	_ = m.openSelector(RequestResume, "")
	if m.composer.selector == nil || len(m.composer.selector.items) != 1 {
		t.Fatalf("selector = %+v, want one item", m.composer.selector)
	}
}

func TestResumeSelectorDoesNotLoadWhileBusy(t *testing.T) {
	loads := 0
	m := sessionsTestModel(func() tea.Cmd {
		loads++
		return nil
	})
	m.busy = true
	if cmd := m.openSelector(RequestResume, ""); cmd != nil {
		t.Fatal("openSelector() = cmd while busy, want nil")
	}
	if loads != 0 {
		t.Fatalf("loads = %d, want 0 while busy", loads)
	}
}

func TestResumeSessionsIgnoredAfterEscape(t *testing.T) {
	m := sessionsTestModel(func() tea.Cmd {
		return func() tea.Msg { return SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/one"}}} }
	})
	cmd := m.openSelector(RequestResume, "")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(cmd())
	if m.composer.selector != nil {
		t.Fatal("selector opened after the pending request was canceled")
	}
}

func TestResumeSessionsIgnoredAfterAnotherSelector(t *testing.T) {
	m := sessionsTestModel(func() tea.Cmd {
		return func() tea.Msg { return SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/one"}}} }
	})
	cmd := m.openSelector(RequestResume, "")
	m.openSelector(RequestModel, "")
	m.Update(cmd())
	if m.composer.selector == nil || m.composer.selector.kind != RequestModel {
		t.Fatalf("selector = %+v, want model selector to stay", m.composer.selector)
	}
}

func TestResumeSelectorCoalescesConcurrentRequests(t *testing.T) {
	loads := 0
	m := sessionsTestModel(func() tea.Cmd {
		loads++
		return func() tea.Msg { return SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/one"}}} }
	})
	first := m.openSelector(RequestResume, "a")
	if second := m.openSelector(RequestResume, "b"); second != nil {
		t.Fatal("second openSelector() = cmd, want single-flight")
	}
	if loads != 1 {
		t.Fatalf("loads = %d, want 1", loads)
	}
	m.Update(first())
	if m.composer.selector == nil || m.composer.selector.input.Value() != "b" {
		t.Fatalf("selector query = %q, want latest query", m.composer.selector.input.Value())
	}
}

func TestResumeSessionsIgnoredWhenSelectorOpen(t *testing.T) {
	m := sessionsTestModel(nil)
	m.openSelectorNow(RequestModel, "")
	m.resumePending = true
	m.Update(SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/one"}}})
	if m.composer.selector.kind != RequestModel {
		t.Fatalf("selector = %+v, want existing selector untouched", m.composer.selector)
	}
}

func TestResumeSessionsIgnoredAfterCtrlC(t *testing.T) {
	m := sessionsTestModel(func() tea.Cmd {
		return func() tea.Msg { return SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/one"}}} }
	})
	cmd := m.openSelector(RequestResume, "")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m.Update(cmd())
	if m.composer.selector != nil {
		t.Fatal("selector opened after Ctrl+C canceled the pending request")
	}
}

func TestResumeSessionsDropsStaleGeneration(t *testing.T) {
	first := sessionsTestModel(func() tea.Cmd {
		return func() tea.Msg { return SessionsMsg{Sessions: []SessionOption{{Path: "/tmp/old"}}} }
	})
	cmd := first.openSelector(RequestResume, "")
	first.Update(tea.KeyMsg{Type: tea.KeyEsc})
	second := first.openSelector(RequestResume, "")
	first.Update(cmd())
	if first.composer.selector != nil {
		t.Fatal("stale session list opened the selector")
	}
	first.Update(second())
	if first.composer.selector == nil || len(first.composer.selector.items) != 1 || first.composer.selector.items[0].value != "/tmp/old" {
		t.Fatalf("selector = %+v, want latest result", first.composer.selector)
	}
}
