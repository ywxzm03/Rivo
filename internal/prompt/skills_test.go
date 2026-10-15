package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkill 在 skills 根目录下写入一个 skill
func writeSkill(t *testing.T, root, dir, content string) {
	t.Helper()
	writeFile(t, filepath.Join(root, dir, "SKILL.md"), content)
}

// linkSkillPath 创建测试用的目录或文件软链接
func linkSkillPath(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func TestDiscoverSkillsParsesFrontmatter(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "one", "---\nname: one\ndescription: First skill\n---\n\n# One\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 {
		t.Fatalf("skills = %+v, want 1", skills)
	}
	if skills[0].Name != "one" || skills[0].Description != "First skill" {
		t.Errorf("skill = %+v, want name/description parsed", skills[0])
	}
	if skills[0].BaseDir != filepath.Join(skillsDir, "one") {
		t.Errorf("BaseDir = %q, want skill directory", skills[0].BaseDir)
	}
}

func TestDiscoverSkillsMultilineDescriptionNormalized(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "multi", "---\nname: multi\ndescription: |\n  Line one.\n  Line two.\n---\n\nbody\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 {
		t.Fatalf("skills = %+v, want 1", skills)
	}
	// 多行块标量压成单行
	if skills[0].Description != "Line one. Line two." {
		t.Errorf("Description = %q, want normalized single line", skills[0].Description)
	}
}

func TestDiscoverSkillsNameFallsBackToDirName(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "fallback", "---\ndescription: No name given\n---\n\nbody\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 || skills[0].Name != "fallback" {
		t.Fatalf("skills = %+v, want name from directory", skills)
	}
}

func TestDiscoverSkillsInvalidFrontmatterSkipped(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "bad", "---\nname: bad\ndescription: [unclosed\n---\n\nbody\n")

	if skills := discoverSkills(root, agentDir); len(skills) != 0 {
		t.Fatalf("skills = %+v, want invalid frontmatter skipped", skills)
	}
}

func TestDiscoverSkillsRejectsInvalidNames(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	cases := map[string]string{
		"upper":     "---\nname: BadName\ndescription: d\n---\n",
		"lead-hyph": "---\nname: -bad\ndescription: d\n---\n",
		"double-hy": "---\nname: bad--name\ndescription: d\n---\n",
		"trail-hy":  "---\nname: bad-\ndescription: d\n---\n",
	}
	for dir, content := range cases {
		writeSkill(t, skillsDir, dir, content)
	}
	if skills := discoverSkills(root, agentDir); len(skills) != 0 {
		t.Fatalf("skills = %+v, want invalid names rejected", skills)
	}
}

func TestDiscoverSkillsRequiresDescription(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "nodesc", "---\nname: nodesc\n---\n")

	if skills := discoverSkills(root, agentDir); len(skills) != 0 {
		t.Fatalf("skills = %+v, want missing description rejected", skills)
	}
}

func TestDiscoverSkillsDoesNotRecurseBelowSkillRoot(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	// 含 SKILL.md 的目录视为 skill root，其子目录不再扫描
	writeSkill(t, skillsDir, "outer", "---\nname: outer\ndescription: d\n---\n")
	writeSkill(t, filepath.Join(skillsDir, "outer"), "nested", "---\nname: nested\ndescription: d\n---\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 || skills[0].Name != "outer" {
		t.Fatalf("skills = %+v, want nested skill skipped", skills)
	}
}

func TestDiscoverSkillsFollowsDirectorySymlinks(t *testing.T) {
	cases := []struct {
		name, link, skill string
		relative          bool
	}{
		{"skills-root", "agent/skills", "docs", false},
		{"skill-directory", "agent/skills/alias", "", false},
		{"nested-directory", "agent/skills/catalog", "tools/docs", false},
		{"project-directory", ".rivo/skills/alias", "", false},
		{"relative-directory", "agent/skills/alias", "", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "library")
			writeSkill(t, target, tt.skill, "---\nname: docs\ndescription: Linked instructions\n---\nBody")
			link := filepath.Join(root, filepath.FromSlash(tt.link))
			if tt.relative {
				var err error
				target, err = filepath.Rel(filepath.Dir(link), target)
				if err != nil {
					t.Fatal(err)
				}
			}
			linkSkillPath(t, target, link)

			skills := discoverSkills(root, filepath.Join(root, "agent"))
			if len(skills) != 1 || skills[0].Name != "docs" {
				t.Fatalf("skills = %+v, want linked docs skill", skills)
			}
			wantDir := filepath.Join(link, tt.skill)
			if skills[0].BaseDir != wantDir || skills[0].FilePath != filepath.Join(wantDir, "SKILL.md") {
				t.Fatalf("skill = %+v, want paths through %q", skills[0], wantDir)
			}
			if body, err := ReadSkillBody(skills[0].FilePath); err != nil || body != "Body" {
				t.Fatalf("ReadSkillBody() = %q, %v, want linked body", body, err)
			}
		})
	}
}

func TestDiscoverSkillsFollowsSkillFileSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "library", "SKILL.md")
	writeFile(t, target, "---\ndescription: Linked file\n---\nBody")
	dir := filepath.Join(root, "agent", "skills", "alias")
	path := filepath.Join(dir, "SKILL.md")
	linkSkillPath(t, target, path)

	skills := discoverSkills(root, filepath.Join(root, "agent"))
	if len(skills) != 1 || skills[0].Name != "alias" || skills[0].BaseDir != dir || skills[0].FilePath != path {
		t.Fatalf("skills = %+v, want file link path and alias name", skills)
	}
}

func TestDiscoverSkillsRecursesIntoContainers(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "catalog/tools/docs", "---\nname: docs\ndescription: Nested instructions\n---\n")
	writeSkill(t, skillsDir, "other", "---\nname: other\ndescription: Other instructions\n---\n")
	// 普通 Markdown 文件即使元数据有效也不声明 Skill
	writeFile(t, filepath.Join(skillsDir, "root.md"), "---\nname: root\ndescription: Root markdown\n---\n")
	writeFile(t, filepath.Join(skillsDir, "catalog", "nested.md"), "---\nname: nested\ndescription: Nested markdown\n---\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 2 || skills[0].Name != "docs" || skills[1].Name != "other" {
		t.Fatalf("skills = %+v, want nested docs followed by other", skills)
	}
}

func TestDiscoverSkillsPrefersRootSkill(t *testing.T) {
	for name, content := range map[string]string{
		"valid":          "---\nname: root\ndescription: Root instructions\n---\n",
		"invalid-yaml":   "---\nname: root\ndescription: [unclosed\n---\n",
		"no-description": "---\nname: root\n---\n",
		"directory":      "",
		"broken-link":    "",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			agentDir := filepath.Join(root, "agent")
			skillsDir := filepath.Join(agentDir, "skills")
			writeSkill(t, skillsDir, "nested", "---\nname: nested\ndescription: Nested instructions\n---\n")
			path := filepath.Join(skillsDir, "SKILL.md")
			switch name {
			case "directory":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "broken-link":
				linkSkillPath(t, filepath.Join(root, "missing.md"), path)
			default:
				writeFile(t, path, content)
			}

			skills := discoverSkills(root, agentDir)
			if name == "valid" {
				if len(skills) != 1 || skills[0].Name != "root" || skills[0].BaseDir != skillsDir {
					t.Fatalf("skills = %+v, want only the root skill", skills)
				}
			} else if len(skills) != 0 {
				t.Fatalf("skills = %+v, want invalid root to stop recursion", skills)
			}
		})
	}
}

func TestDiscoverSkillsSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	for _, dir := range []string{".hidden/docs", "node_modules/docs", "catalog/.hidden/docs", "catalog/node_modules/docs"} {
		writeSkill(t, skillsDir, dir, "---\nname: ignored\ndescription: Ignored instructions\n---\n")
	}
	writeSkill(t, skillsDir, "catalog/docs", "---\nname: docs\ndescription: Visible instructions\n---\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 || skills[0].Name != "docs" {
		t.Fatalf("skills = %+v, want only the visible skill", skills)
	}
}

func TestDiscoverSkillsSkipsInvalidSymlinks(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "docs", "---\nname: docs\ndescription: Valid instructions\n---\n")
	linkSkillPath(t, filepath.Join(root, "missing"), filepath.Join(skillsDir, "broken-directory"))
	linkSkillPath(t, filepath.Join(root, "missing.md"), filepath.Join(skillsDir, "broken-file", "SKILL.md"))
	writeFile(t, filepath.Join(root, "plain.txt"), "not a directory")
	linkSkillPath(t, filepath.Join(root, "plain.txt"), filepath.Join(skillsDir, "file-target"))
	linkSkillPath(t, root, filepath.Join(skillsDir, "directory-target", "SKILL.md"))

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 || skills[0].Name != "docs" {
		t.Fatalf("skills = %+v, want invalid links skipped without losing docs", skills)
	}
}

func TestLoadSkillsFromDirAvoidsDirectoryCycles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "docs", "---\nname: docs\ndescription: Valid instructions\n---\n")
	linkSkillPath(t, root, filepath.Join(root, "catalog", "back"))
	linkSkillPath(t, filepath.Join(root, "self"), filepath.Join(root, "self"))

	skills := loadSkillsFromDir(root)
	if len(skills) != 1 || skills[0].Name != "docs" {
		t.Fatalf("skills = %+v, want one docs skill despite directory cycles", skills)
	}
}

func TestDiscoverSkillsDeduplicatesRealFiles(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	target := filepath.Join(root, "library", "SKILL.md")
	writeFile(t, target, "---\ndescription: Shared instructions\n---\n")
	for _, dir := range []string{"agent/skills/alpha", "agent/skills/beta", ".rivo/skills/gamma"} {
		linkSkillPath(t, target, filepath.Join(root, filepath.FromSlash(dir), "SKILL.md"))
	}

	skills := discoverSkills(root, agentDir)
	wantPath := filepath.Join(agentDir, "skills", "alpha", "SKILL.md")
	if len(skills) != 1 || skills[0].Name != "alpha" || skills[0].FilePath != wantPath {
		t.Fatalf("skills = %+v, want first alias of the shared file", skills)
	}
}

func TestDiscoverSkillsNameCollisionDoesNotConsumeFile(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSkill(t, filepath.Join(agentDir, "skills"), "a-first", "---\nname: dup\ndescription: First instructions\n---\n")
	target := filepath.Join(root, "library", "SKILL.md")
	writeFile(t, target, "---\ndescription: Shared instructions\n---\n")
	linkSkillPath(t, target, filepath.Join(agentDir, "skills", "z-container", "dup", "SKILL.md"))
	linkSkillPath(t, target, filepath.Join(root, ".rivo", "skills", "other", "SKILL.md"))

	skills := discoverSkills(root, agentDir)
	if len(skills) != 2 || skills[0].Name != "dup" || skills[1].Name != "other" {
		t.Fatalf("skills = %+v, want rejected name collision to leave the file available", skills)
	}
}

func TestDiscoverSkillsGlobalWinsCollision(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSkill(t, filepath.Join(agentDir, "skills"), "dup", "---\nname: dup\ndescription: global\n---\n")
	writeSkill(t, filepath.Join(root, ".rivo", "skills"), "dup", "---\nname: dup\ndescription: project\n---\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 || skills[0].Description != "global" {
		t.Fatalf("skills = %+v, want global skill to win", skills)
	}
}

func TestDiscoverSkillsProjectAddsToGlobal(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSkill(t, filepath.Join(agentDir, "skills"), "a", "---\nname: a\ndescription: from global\n---\n")
	writeSkill(t, filepath.Join(root, ".rivo", "skills"), "b", "---\nname: b\ndescription: from project\n---\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 2 {
		t.Fatalf("skills = %+v, want both locations loaded", skills)
	}
	if skills[0].Name != "a" || skills[1].Name != "b" {
		t.Errorf("skills = %+v, want global before project", skills)
	}
}

func TestDiscoverSkillsDisableModelInvocationParsed(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "hidden", "---\nname: hidden\ndescription: d\ndisable-model-invocation: true\n---\n")

	skills := discoverSkills(root, agentDir)
	if len(skills) != 1 || !skills[0].DisableModelInvocation {
		t.Fatalf("skills = %+v, want disable-model-invocation parsed", skills)
	}
}

func TestDiscoverSkillsMissingDirIsEmpty(t *testing.T) {
	root := t.TempDir()
	if skills := discoverSkills(root, filepath.Join(root, "agent")); len(skills) != 0 {
		t.Fatalf("skills = %+v, want none", skills)
	}
}

func TestDiscoverSkillsIgnoresDirWithoutSkillFile(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeFile(t, filepath.Join(agentDir, "skills", "plain", "README.md"), "not a skill")
	if skills := discoverSkills(root, agentDir); len(skills) != 0 {
		t.Fatalf("skills = %+v, want dir without SKILL.md ignored", skills)
	}
}

func TestValidSkillName(t *testing.T) {
	valid := []string{"a", "ab-c", "a1-b2", "x9"}
	for _, name := range valid {
		if !validSkillName(name) {
			t.Errorf("validSkillName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "A", "a_b", "a b", "-a", "a-", "a--b", "a.b", "技能"}
	for _, name := range invalid {
		if validSkillName(name) {
			t.Errorf("validSkillName(%q) = true, want false", name)
		}
	}
}

// TestParseFrontmatterEmptyFrontmatter 空 frontmatter 不 panic 且按无字段处理
func TestParseFrontmatterEmptyFrontmatter(t *testing.T) {
	frontmatter, err := parseFrontmatter("---\n---")
	if err != nil {
		t.Fatalf("parseFrontmatter() error = %v, want nil", err)
	}
	if frontmatter.Name != "" || frontmatter.Description != "" {
		t.Errorf("frontmatter = %+v, want zero value", frontmatter)
	}
}

// TestParseFrontmatterEmptyFrontmatterWithBody 空 frontmatter 后仍有正文
func TestParseFrontmatterEmptyFrontmatterWithBody(t *testing.T) {
	frontmatter, err := parseFrontmatter("---\n---\n\n# Body\n")
	if err != nil {
		t.Fatalf("parseFrontmatter() error = %v, want nil", err)
	}
	if frontmatter.Name != "" {
		t.Errorf("name = %q, want empty", frontmatter.Name)
	}
}

// TestParseFrontmatterSingleLine 单行 frontmatter 正常解析
func TestParseFrontmatterSingleLine(t *testing.T) {
	frontmatter, err := parseFrontmatter("---\nname: one\ndescription: d\n---\nbody")
	if err != nil {
		t.Fatalf("parseFrontmatter() error = %v", err)
	}
	if frontmatter.Name != "one" || frontmatter.Description != "d" {
		t.Errorf("frontmatter = %+v, want parsed fields", frontmatter)
	}
}

// TestLoadSkillFromFileEmptyFrontmatterDoesNotPanic 空 frontmatter 的 SKILL.md 走目录名回退
func TestLoadSkillFromFileEmptyFrontmatterDoesNotPanic(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	skillsDir := filepath.Join(agentDir, "skills")
	writeSkill(t, skillsDir, "emptyfm", "---\n---\n\n# Body\n")

	// 无 description 应被跳过，关键是不得 panic
	if skills := discoverSkills(root, agentDir); len(skills) != 0 {
		t.Fatalf("skills = %+v, want skipped without description", skills)
	}
}
