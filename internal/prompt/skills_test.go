package prompt

import (
	"path/filepath"
	"testing"
)

// writeSkill 在 skills 根目录下写入一个 skill
func writeSkill(t *testing.T, root, dir, content string) {
	t.Helper()
	writeFile(t, filepath.Join(root, dir, "SKILL.md"), content)
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
