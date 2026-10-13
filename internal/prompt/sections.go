// Package prompt 构建 system prompt 的分节内容，并负责 AGENTS.md 与 skills 的磁盘发现
package prompt

import (
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// Sections 一组分节文本，键为分节名；空串表示该节不存在或被删除
type Sections map[string]string

// Options 分节构建的输入
type Options struct {
	// Cwd 工作目录，进 cwd 分节
	Cwd string
	// Tools 已装载的工具，按装载顺序贡献 tools 与 rules 分节
	Tools []llm.Tool
	// ContextFiles AGENTS.md 链，进 instructions 分节
	ContextFiles []ContextFile
	// Skills 已发现的 skills，进 skills 分节
	Skills []Skill
}

// preamble 固定开场文案
const preamble = "You are an expert coding assistant operating inside Rivo, a coding agent harness. " +
	"You help users by reading files, executing commands, editing code, and writing new files."

// trailingRules 固定追加在 rules 末尾的两条
var trailingRules = []string{
	"Be concise in your responses",
	"Show file paths clearly when working with files",
}

// BuildSections 构建全部分节：preamble 无标签，其余节包在 <节名> 标签里；
// instructions 与 skills 仅在对应输入非空时出现
func BuildSections(opts Options) Sections {
	sections := Sections{
		"preamble": preamble,
		"tools":    buildTools(opts.Tools),
		"rules":    buildRules(opts.Tools),
	}
	if len(opts.ContextFiles) > 0 {
		sections["instructions"] = buildInstructions(opts.ContextFiles)
	}
	if skills := formatSkills(opts.Skills); skills != "" {
		sections["skills"] = skills
	}
	sections["cwd"] = strings.ReplaceAll(opts.Cwd, "\\", "/")
	return sections
}

// buildTools 渲染工具清单，每行一条 name 与 snippet；无 snippet 的工具不出现
func buildTools(tools []llm.Tool) string {
	var lines []string
	for _, t := range tools {
		if snippet := t.PromptSnippet(); snippet != "" {
			lines = append(lines, "- "+t.Name()+": "+snippet)
		}
	}
	return "<tools>\n" + strings.Join(lines, "\n") + "\n</tools>"
}

// buildRules 按工具装载顺序收集 guideline，trim 后精确去重，末尾追加固定两条
func buildRules(tools []llm.Tool) string {
	var rules []string
	seen := make(map[string]bool)
	add := func(rule string) {
		normalized := strings.TrimSpace(rule)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		rules = append(rules, "- "+normalized)
	}
	for _, t := range tools {
		for _, rule := range t.PromptGuidelines() {
			add(rule)
		}
	}
	for _, rule := range trailingRules {
		add(rule)
	}
	return "<rules>\n" + strings.Join(rules, "\n") + "\n</rules>"
}

// buildInstructions 渲染 AGENTS.md 链：固定前缀后每份文件一段，段间空行
func buildInstructions(files []ContextFile) string {
	var b strings.Builder
	b.WriteString("<instructions>\nProject-specific instructions and guidelines:")
	for _, file := range files {
		b.WriteString("\n\n<instructions path=\"" + file.Path + "\">\n")
		b.WriteString(file.Content)
		b.WriteString("\n</instructions>")
	}
	b.WriteString("\n</instructions>")
	return b.String()
}

// DiffSections 比较当前分节与目标分节：只返回发生变化或需要删除的节，值指向目标文本；
// 无差异返回 nil，空串表示该节需要删除
func DiffSections(current, target Sections) Sections {
	patch := make(Sections)
	for name, text := range target {
		if current[name] != text {
			patch[name] = text
		}
	}
	for name := range current {
		if _, ok := target[name]; !ok {
			patch[name] = ""
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}
