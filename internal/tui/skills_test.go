package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestSkillCompletionMatchesBareAndFullNames(t *testing.T) {
	m := testModel(100, 30)
	m.state.Skills = []SkillOption{{Name: "deep-research"}, {Name: "research-idea"}, {Name: "to-sidecar"}, {Name: "brainstorm"}}
	for query, expected := range map[string]string{"idea": "skill:research-idea", "mod": "model", "skill:side": "skill:to-sidecar", "skbra": "skill:brainstorm"} {
		m.editor.SetValue("/" + query)
		items := m.completionItems()
		if len(items) == 0 || items[0].name != expected {
			t.Fatalf("query %q: %+v, want %s first", query, items, expected)
		}
	}
	for _, query := range []string{"", "skill"} {
		m.editor.SetValue("/" + query)
		count := 0
		for _, item := range m.completionItems() {
			if strings.HasPrefix(item.name, "skill:") {
				count++
			}
		}
		if count != len(m.state.Skills) {
			t.Fatalf("query %q omitted skills", query)
		}
	}
	items := fuzzyCommands([]slashCommand{{name: "3z"}}, "z3", false)
	if len(items) != 1 {
		t.Fatal("letter-digit query swap did not match")
	}
}

func TestSkillCompletionScrollsToEveryCandidate(t *testing.T) {
	m := testModel(100, 30)
	for i := 0; i < 12; i++ {
		m.state.Skills = append(m.state.Skills, SkillOption{Name: fmt.Sprintf("docs-%02d", i), Description: "Document instructions"})
	}
	typeText(m, "/skill:")
	for i := 0; i < 11; i++ {
		press(m, tea.KeyDown)
	}
	if m.completion != 11 || len(strings.Split(m.completionView(), "\n")) != 5 || !strings.Contains(ansi.Strip(m.completionView()), "❯ skill:docs-11") {
		t.Fatal("completion window did not follow the last skill")
	}
	assertFrame(t, m)
	press(m, tea.KeyTab)
	if m.editor.Value() != "/skill:docs-11 " || m.busy {
		t.Fatal("Tab did not only complete the selected skill")
	}
	var request Request
	m.handlers.Request = func(r Request) tea.Cmd { request = r; return nil }
	typeText(m, "explain this\nkeep formatting")
	press(m, tea.KeyEnter)
	if request.Kind != "run" || request.Value != "/skill:docs-11 explain this\nkeep formatting" {
		t.Fatalf("skill submission = %+v", request)
	}
}

func TestSkillRenderingCollapsesInstructionsAndKeepsArguments(t *testing.T) {
	m := testModel(80, 24)
	message := Message{ID: "skill", Role: "user", Text: "full saved text", Skill: &SkillInvocationView{Name: "docs", Content: "References are relative to /docs.\n\nSecret instructions", Arguments: "User request\nsecond line"}}
	view := ansi.Strip(m.renderMessage(message))
	if !strings.Contains(view, "[skill] docs") || !strings.Contains(view, "User request") || strings.Contains(view, "Secret instructions") {
		t.Fatalf("collapsed skill = %q", view)
	}
	press(m, tea.KeyCtrlO)
	view = ansi.Strip(m.renderMessage(message))
	if !strings.Contains(view, "Secret instructions") || !strings.Contains(view, "References are relative") || !strings.Contains(view, "User request") {
		t.Fatalf("expanded skill = %q", view)
	}
	message.Skill.Arguments = ""
	m.showAllTools = false
	if strings.Contains(ansi.Strip(m.renderMessage(message)), "❯") {
		t.Fatal("argument-free skill created an empty user row")
	}
}
