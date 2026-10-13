package prompt

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// skillsDirName 全局与项目级 skills 的目录名
const skillsDirName = "skills"

const (
	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
)

// skillFrontmatter SKILL.md 的 frontmatter 字段
type skillFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

// discoverSkills 加载全局与项目级 skills；全局先加载，重名时先加载者胜
func discoverSkills(cwd, agentDir string) []Skill {
	var skills []Skill
	seen := make(map[string]bool)
	// 1.全局目录优先，冲突时它胜出
	for _, skill := range loadSkillsFromDir(filepath.Join(agentDir, skillsDirName)) {
		if seen[skill.Name] {
			continue
		}
		seen[skill.Name] = true
		skills = append(skills, skill)
	}
	// 2.项目级目录补充，重名项跳过
	for _, skill := range loadSkillsFromDir(filepath.Join(cwd, ".rivo", skillsDirName)) {
		if seen[skill.Name] {
			continue
		}
		seen[skill.Name] = true
		skills = append(skills, skill)
	}
	return skills
}

// loadSkillsFromDir 扫描 skills 根目录：含 SKILL.md 的子目录视为 skill，不再向内递归；
// 目录不存在或读取失败按空结果处理
func loadSkillsFromDir(dir string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(dir, entry.Name())
		skillPath := filepath.Join(skillDir, "SKILL.md")
		info, err := os.Stat(skillPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if skill, ok := loadSkillFromFile(skillPath, skillDir); ok {
			skills = append(skills, skill)
		}
	}
	return skills
}

// loadSkillFromFile 解析单个 SKILL.md：frontmatter 非法或校验失败时跳过
func loadSkillFromFile(path, baseDir string) (Skill, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	frontmatter, err := parseFrontmatter(stripBOM(string(raw)))
	if err != nil {
		return Skill{}, false
	}
	name := frontmatter.Name
	if name == "" {
		// 无 frontmatter 名时回退为父目录名
		name = filepath.Base(baseDir)
	}
	if !validSkillName(name) {
		return Skill{}, false
	}
	description := normalizeDescription(frontmatter.Description)
	if description == "" || len([]rune(description)) > maxSkillDescriptionLength {
		return Skill{}, false
	}
	return Skill{
		Name:                   name,
		Description:            description,
		FilePath:               path,
		BaseDir:                baseDir,
		DisableModelInvocation: frontmatter.DisableModelInvocation,
	}, true
}

// parseFrontmatter 解析 YAML frontmatter；无 frontmatter 或 frontmatter 为空时返回零值结构，
// 有 frontmatter 但解析失败时返回错误
func parseFrontmatter(content string) (skillFrontmatter, error) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	if !strings.HasPrefix(normalized, "---") {
		return skillFrontmatter{}, nil
	}
	// 从 3 起查找结束标记，YAML 容忍前导换行；end 为 0 时区间为空，覆盖 "---\n---" 这种空 frontmatter
	end := strings.Index(normalized[3:], "\n---")
	if end == -1 {
		return skillFrontmatter{}, nil
	}
	var frontmatter skillFrontmatter
	if err := yaml.Unmarshal([]byte(normalized[3:end+3]), &frontmatter); err != nil {
		return skillFrontmatter{}, err
	}
	return frontmatter, nil
}

// validSkillName 校验 skill 名：小写字母、数字与连字符，长度受限，首尾无连字符且无连续连字符
func validSkillName(name string) bool {
	if name == "" || len(name) > maxSkillNameLength {
		return false
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// normalizeDescription 把多行描述压成单行，空描述返回空串
func normalizeDescription(description string) string {
	return strings.Join(strings.Fields(description), " ")
}
