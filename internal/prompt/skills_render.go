package prompt

import (
	"strings"
)

// Skill 一个已发现的 skill：清单只放 name/description/location，正文由模型按 location 自行读取
type Skill struct {
	Name        string
	Description string
	// FilePath 是 SKILL.md 的绝对路径，作为清单里的 location
	FilePath string
	// BaseDir 供相对路径引用解析
	BaseDir string
	// DisableModelInvocation 为真时该 skill 不进入清单
	DisableModelInvocation bool
	// fileKey 是 SKILL.md 的规范真实路径，仅用于软链接去重；为空时按 FilePath 解析
	fileKey string
}

// formatSkills 渲染 skills 清单，无可见 skill 时返回空串
func formatSkills(skills []Skill) string {
	var visible []Skill
	for _, s := range skills {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<skills>\n")
	b.WriteString("The following skills provide specialized instructions for specific tasks.\n")
	b.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	b.WriteString("When a skill file references a relative path, resolve it against the skill directory " +
		"(parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n")
	b.WriteString("\n<available_skills>")
	for _, s := range visible {
		b.WriteString("\n  <skill>")
		b.WriteString("\n    <name>" + escapeXML(s.Name) + "</name>")
		b.WriteString("\n    <description>" + escapeXML(s.Description) + "</description>")
		b.WriteString("\n    <location>" + escapeXML(s.FilePath) + "</location>")
		b.WriteString("\n  </skill>")
	}
	b.WriteString("\n</available_skills>\n</skills>")
	return b.String()
}

func escapeXML(text string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(text)
}
