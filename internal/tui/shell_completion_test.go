package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// /ps 与 /stop 必须出现在补全候选里
func TestShellCommandsAppearInCompletion(t *testing.T) {
	m := testModel(100, 30)
	m.editor.SetValue("/")

	names := make(map[string]string, 32)
	for _, item := range m.completionItems() {
		names[item.name] = item.description
	}
	for _, want := range []string{"ps", "stop"} {
		description, ok := names[want]
		if !ok {
			t.Fatalf("completion missing /%s, got %v", want, keysOf(names))
		}
		if strings.TrimSpace(description) == "" {
			t.Fatalf("/%s has no description", want)
		}
	}
}

// 前缀查询要能命中;同分候选的先后由既有模糊匹配决定,只要求出现
func TestShellCommandsMatchByPrefix(t *testing.T) {
	cases := map[string]string{"p": "ps", "ps": "ps", "st": "stop", "stop": "stop"}
	for query, want := range cases {
		m := testModel(100, 30)
		m.editor.SetValue("/" + query)
		found := false
		for _, item := range m.completionItems() {
			if item.name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("query %q did not offer %s", query, want)
		}
	}
}

// 补全选中后按 Tab 要能填入完整的命令名
func TestShellCommandCompletionAccepts(t *testing.T) {
	for _, want := range []string{"ps", "stop"} {
		m := testModel(100, 30)
		m.editor.SetValue("/" + want)
		for index, item := range m.completionItems() {
			if item.name == want {
				m.composer.completion = index
				break
			}
		}
		handled, _ := m.handleCompletionKey(tea.KeyMsg{Type: tea.KeyTab})
		if !handled {
			t.Fatalf("/%s tab not handled by the completion list", want)
		}
		if got := m.editor.Value(); got != "/"+want+" " {
			t.Fatalf("/%s accept produced %q", want, got)
		}
	}
}

// 两个命令都要有可读的描述,与既有命令风格一致
func TestShellCommandDescriptions(t *testing.T) {
	want := map[string]string{
		"ps":   "List background shells",
		"stop": "Stop all background shells",
	}
	for _, item := range commands {
		expected, ok := want[item.name]
		if !ok {
			continue
		}
		if item.description != expected {
			t.Errorf("/%s description = %q, want %q", item.name, item.description, expected)
		}
		delete(want, item.name)
	}
	for name := range want {
		t.Errorf("/%s missing from the command list", name)
	}
}

// keysOf 收集候选名，便于失败信息可读
func keysOf(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	return names
}
