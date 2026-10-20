package adapter

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/tui"
)

type skillInput struct {
	raw, expanded string
}

var skillBlockPattern = regexp.MustCompile(`^<skill name="([^"]+)" location="([^"]+)">\n([\s\S]*?)\n</skill>(?:\n\n([\s\S]*))?$`)

// expandSkillInput 在 TUI 请求边界展开技能，持久化失败后的同一输入复用原正文
func (r *Runtime) expandSkillInput(a *app.App, input string) (string, error) {
	if !strings.HasPrefix(input, "/skill:") {
		return input, nil
	}
	r.mu.Lock()
	pending := r.pendingSkillInput
	r.mu.Unlock()
	if pending != nil && pending.raw == input {
		return pending.expanded, nil
	}
	name, args := strings.TrimPrefix(input, "/skill:"), ""
	if index := strings.IndexFunc(name, unicode.IsSpace); index >= 0 {
		name, args = name[:index], strings.TrimSpace(name[index:])
	}
	if name == "" {
		return "", fmt.Errorf("Usage: /skill:<name> [args]")
	}
	for _, skill := range a.Skills() {
		if skill.Name != name {
			continue
		}
		body, err := prompt.ReadSkillBody(skill.FilePath)
		if err != nil {
			return "", fmt.Errorf("Could not load skill %q: %w", name, err)
		}
		block := fmt.Sprintf("<skill name=\"%s\" location=\"%s\">\nReferences are relative to %s.\n\n%s\n</skill>", html.EscapeString(skill.Name), html.EscapeString(skill.FilePath), skill.BaseDir, body)
		if args != "" {
			block += "\n\n" + args
		}
		return block, nil
	}
	return "", fmt.Errorf("Unknown skill: %s", name)
}

func skillInvocation(text string) *tui.SkillInvocationView {
	match := skillBlockPattern.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	return &tui.SkillInvocationView{Name: html.UnescapeString(match[1]), Location: html.UnescapeString(match[2]), Content: match[3], Arguments: match[4]}
}
