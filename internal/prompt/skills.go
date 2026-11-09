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
	seenNames := make(map[string]bool)
	seenFiles := make(map[string]bool)
	add := func(skill Skill) {
		// 同一份 SKILL.md 通过多个软链接出现时只保留第一次结果
		fileKey := skill.fileKey
		if fileKey == "" {
			fileKey = skillFileKey(skill.FilePath)
		}
		if seenFiles[fileKey] {
			return
		}
		// 全局目录先加入，名称冲突时它胜出
		if seenNames[skill.Name] {
			return
		}
		seenFiles[fileKey] = true
		seenNames[skill.Name] = true
		skills = append(skills, skill)
	}
	for _, skill := range loadSkillsFromDir(filepath.Join(agentDir, skillsDirName)) {
		add(skill)
	}
	// 项目级目录补充，重名项跳过
	for _, skill := range loadSkillsFromDir(filepath.Join(cwd, ".rivo", skillsDirName)) {
		add(skill)
	}
	return skills
}

// loadSkillsFromDir 扫描 skills 根目录及其子目录，目录不存在或读取失败按空结果处理
func loadSkillsFromDir(dir string) []Skill {
	dirKey, ok := skillFileKeyIfExists(dir)
	if !ok {
		return nil
	}
	return loadSkillsFromDirRecursive(dir, dirKey, make(map[string]bool))
}

// loadSkillsFromDirRecursive 递归扫描目录并按真实路径防止软链接循环；
// dir 是展示路径，dirKey 是对应的规范真实路径，只有目录项本身是软链接时才重新解析
func loadSkillsFromDirRecursive(dir, dirKey string, visited map[string]bool) []Skill {
	if visited[dirKey] {
		return nil
	}
	visited[dirKey] = true
	// 当前目录声明了 Skill 时，优先使用它并停止向下递归；直接 Lstat 候选文件，
	// 避免为一个 skill 目录多付一次 ReadDir
	rootSkillPath := filepath.Join(dir, "SKILL.md")
	if info, err := os.Lstat(rootSkillPath); err == nil {
		fileKey := filepath.Join(dirKey, "SKILL.md")
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, ok := skillFileKeyIfExists(rootSkillPath)
			if !ok {
				return nil
			}
			fileKey = resolved
			target, err := os.Stat(rootSkillPath)
			if err != nil || !target.Mode().IsRegular() {
				return nil
			}
		} else if !info.Mode().IsRegular() {
			return nil
		}
		if skill, ok := loadSkillFromFile(rootSkillPath, dir, fileKey); ok {
			return []Skill{skill}
		}
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var skills []Skill
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		skillDir := filepath.Join(dir, name)
		childKey := filepath.Join(dirKey, name)
		// 用 Lstat 结果判断类型，不依赖 DirEntry.Type：部分文件系统返回 DT_UNKNOWN
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, ok := skillFileKeyIfExists(skillDir)
			if !ok {
				continue
			}
			childKey = resolved
		} else if !info.IsDir() {
			continue
		}
		skills = append(skills, loadSkillsFromDirRecursive(skillDir, childKey, visited)...)
	}
	return skills
}

// skillFileKey 返回存在路径的绝对真实路径，用于目录访问和文件去重
func skillFileKey(path string) string {
	if key, ok := skillFileKeyIfExists(path); ok {
		return key
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

// skillFileKeyIfExists 解析文件或目录软链接并确认路径存在
func skillFileKeyIfExists(path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return filepath.Clean(resolved), true
	}
	return filepath.Clean(absolute), true
}

// loadSkillFromFile 解析单个 SKILL.md：frontmatter 非法或校验失败时跳过
func loadSkillFromFile(path, baseDir, fileKey string) (Skill, bool) {
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
		fileKey:                fileKey,
	}, true
}

// parseFrontmatter 解析 YAML frontmatter；无 frontmatter 时返回零值结构，解析失败返回错误
func parseFrontmatter(content string) (skillFrontmatter, error) {
	yamlText, _ := splitFrontmatter(content)
	var frontmatter skillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlText), &frontmatter); err != nil {
		return skillFrontmatter{}, err
	}
	return frontmatter, nil
}

// splitFrontmatter 统一发现与显式调用的正文边界，缺少结束标记时保留完整文本
func splitFrontmatter(content string) (yamlText, body string) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(stripBOM(content), "\r\n", "\n"), "\r", "\n")
	if !strings.HasPrefix(normalized, "---") {
		return "", normalized
	}
	// 从 3 起查找结束标记，YAML 容忍前导换行
	end := strings.Index(normalized[3:], "\n---")
	if end == -1 {
		return "", normalized
	}
	return normalized[3 : end+3], normalized[end+7:]
}

// ReadSkillBody 读取当前磁盘正文并剥离 frontmatter，元数据由 Loader 的快照管理
func ReadSkillBody(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if _, err := parseFrontmatter(string(raw)); err != nil {
		return "", err
	}
	_, body := splitFrontmatter(string(raw))
	return strings.TrimSpace(body), nil
}

// validSkillName 校验 skill 名：小写字母、数字与连字符，首尾无连字符且无连续连字符
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
