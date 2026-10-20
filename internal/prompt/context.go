package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// ContextFile 一份已加载的 AGENTS.md
type ContextFile struct {
	Path    string
	Content string
}

// contextFileCandidates 每个目录内的候选文件名，按优先级排列
var contextFileCandidates = []string{"AGENTS.md", "AGENTS.MD"}

// Loader 持有当前生效的 AGENTS.md 链与 skills，重读磁盘时整体替换
type Loader struct {
	cwd      string
	agentDir string
	files    []ContextFile
	skills   []Skill
}

func NewLoader(cwd, agentDir string) *Loader {
	return &Loader{cwd: cwd, agentDir: agentDir}
}

// Reload 重读磁盘并整体替换内存结果，读取失败的文件按跳过处理
func (l *Loader) Reload() {
	l.files = discoverContextFiles(l.cwd, l.agentDir)
	l.skills = discoverSkills(l.cwd, l.agentDir)
}

func (l *Loader) ContextFiles() []ContextFile { return l.files }

func (l *Loader) Skills() []Skill { return l.skills }

// discoverContextFiles 加载 AGENTS.md 链：全局文件优先，再从最外层祖先目录到 cwd 依次层叠
func discoverContextFiles(cwd, agentDir string) []ContextFile {
	var files []ContextFile
	seen := make(map[string]bool)

	if global := loadContextFileFromDir(agentDir); global != nil {
		files = append(files, *global)
		seen[global.Path] = true
	}

	// 祖先链从最外层向内收集，再反转，保证内层目录的内容更靠后
	var ancestors []ContextFile
	shadowed := findShadowedContextFile(cwd)
	current := cwd
	for {
		if file := loadContextFileFromDir(current); file != nil {
			// 与遮蔽路径同用 realpath 比较，否则符号链接（如 macOS 的 /var）会让遮蔽失效
			if !seen[file.Path] && canonicalizePath(file.Path) != shadowed {
				ancestors = append(ancestors, *file)
				seen[file.Path] = true
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}
	return append(files, ancestors...)
}

// loadContextFileFromDir 取目录内第一个存在的普通文件
func loadContextFileFromDir(dir string) *ContextFile {
	for _, name := range contextFileCandidates {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return &ContextFile{Path: path, Content: stripBOM(string(content))}
	}
	return nil
}

// findShadowedContextFile 处理嵌套 worktree：worktree 根有同名候选文件时，
// 主仓中的同名文件被遮蔽，避免同一份说明出现两次
func findShadowedContextFile(cwd string) string {
	gitPaths := findGitPaths(cwd)
	if gitPaths == nil {
		return ""
	}
	commonGitDir := canonicalizePath(gitPaths.commonGitDir)
	worktreeRoot := canonicalizePath(gitPaths.repoDir)
	mainRepoRoot := filepath.Dir(commonGitDir)
	// 普通仓库两者相同，sibling worktree 的主仓也不是其祖先，两种情况都不遮蔽
	if !strings.HasPrefix(worktreeRoot, mainRepoRoot+string(filepath.Separator)) {
		return ""
	}
	// 主仓根下的 .git 与 commonGitDir 一致时才说明该目录本身是主工作树检出，
	// 裸仓布局与 submodule 都不满足
	if canonicalizePath(filepath.Join(mainRepoRoot, ".git")) != commonGitDir {
		return ""
	}
	file := loadContextFileFromDir(worktreeRoot)
	if file == nil {
		return ""
	}
	return filepath.Join(mainRepoRoot, filepath.Base(file.Path))
}

// gitPaths 一个仓库的 git 目录信息
type gitPaths struct {
	repoDir string
	// commonGitDir 共享的 git 目录，worktree 下指向主仓
	commonGitDir string
}

// findGitPaths 从 cwd 向上查找 .git：目录形态直接使用，文件形态（worktree/submodule）
// 解析 gitdir 与 commondir
func findGitPaths(cwd string) *gitPaths {
	dir := cwd
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.IsDir() {
				if !exists(filepath.Join(gitPath, "HEAD")) {
					return nil
				}
				return &gitPaths{repoDir: dir, commonGitDir: gitPath}
			}
			content, err := os.ReadFile(gitPath)
			if err != nil {
				return nil
			}
			trimmed := strings.TrimSpace(string(content))
			if !strings.HasPrefix(trimmed, "gitdir: ") {
				return nil
			}
			gitDir := strings.TrimSpace(strings.TrimPrefix(trimmed, "gitdir: "))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(dir, gitDir)
			}
			if !exists(filepath.Join(gitDir, "HEAD")) {
				return nil
			}
			commonGitDir := gitDir
			if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common := strings.TrimSpace(string(data))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
				commonGitDir = common
			}
			return &gitPaths{repoDir: dir, commonGitDir: commonGitDir}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// canonicalizePath 解析符号链接，路径不存在时原样返回
func canonicalizePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func stripBOM(text string) string {
	return strings.TrimPrefix(text, "\ufeff")
}
