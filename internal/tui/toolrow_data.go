package tui

import (
	"fmt"
	"regexp"
	"strings"
)

// toolRowOutcome 是结果句：read/grep/find/ls/write/edit 用一句话说明结果，
// 命令与自定义工具直接铺输出，因此返回空串
func toolRowOutcome(call ToolCallView) string {
	switch call.Name {
	case "read":
		return "Read " + countNoun(toolRowResultLines(call), "line", "lines")
	case "grep":
		return "Found " + countNoun(toolRowResultLines(call), "line", "lines")
	case "find":
		return "Found " + countNoun(toolRowResultLines(call), "file", "files")
	case "ls":
		return "Listed " + countNoun(toolRowResultLines(call), "path", "paths")
	case "write":
		lines := toolRowContentLines(call)
		return "Wrote " + countNoun(lines, "line", "lines") + " to " + toolPath(toolArgs(call))
	case "edit":
		added, removed := toolRowEditChanges(call)
		switch {
		case added > 0 && removed > 0:
			return "Added " + countNoun(added, "line", "lines") + ", removed " + countNoun(removed, "line", "lines")
		case added > 0:
			return "Added " + countNoun(added, "line", "lines")
		case removed > 0:
			return "Removed " + countNoun(removed, "line", "lines")
		case call.Details != "":
			// 详情存在却解析不出增删行时交给 Result 原文，避免报成“0 行”
			return ""
		}
	}
	return ""
}

func countNoun(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", count, plural)
}

// toolRowContentLines 数 write 写入的行数
func toolRowContentLines(call ToolCallView) int {
	content := strings.TrimRight(argString(toolArgs(call), "content"), "\n")
	if content == "" {
		return 0
	}
	return len(strings.Split(content, "\n"))
}

// toolRowResultLines 数结果文本的行数，先剥掉工具自己追加的尾部提示
func toolRowResultLines(call ToolCallView) int {
	body, _ := splitReadFooter(call.Result)
	text := strings.TrimRight(stripGeneratedFooter(body), "\n")
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

var generatedFooter = regexp.MustCompile(`\n\n\[[^\n]*\]$`)
var readFooter = regexp.MustCompile(`(?s)^(.*)\n\n\[([^\n]*)\]$`)

// splitReadFooter 拆出 read 结果正文与它自己的续读提示
func splitReadFooter(output string) (body, footer string) {
	text := strings.TrimRight(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	if match := readFooter.FindStringSubmatch(text); match != nil {
		return match[1], match[2]
	}
	return text, ""
}

// stripGeneratedFooter 去掉工具追加的 `[N matches limit reached...]` 之类提示
func stripGeneratedFooter(output string) string {
	text := strings.TrimRight(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	return generatedFooter.ReplaceAllString(text, "")
}

// toolRowEditChanges 从 diff 详情里数增删行
func toolRowEditChanges(call ToolCallView) (added, removed int) {
	for _, line := range parseRowDiff(toolRowDiffPatch(call)) {
		switch line.Kind {
		case diffAdd:
			added++
		case diffRemove:
			removed++
		}
	}
	return added, removed
}

var shellStatusLine = regexp.MustCompile(`^(?:Command (?:exited with code \d+|timed out.*|aborted)|Operation aborted)$`)

// toolRowFailure 给出失败行的标题与需要另铺的细节：
// 命令用退出码打头、输出放在下面；其他工具只给一行原因
func toolRowFailure(call ToolCallView) (headline, detail string) {
	if call.Status == ToolInterrupted {
		return "Error: Operation aborted", ""
	}
	// 1.agent 给失败结果加了 `tool error: ` 前缀，先剥掉才能认出末行的退出状态
	output := strings.TrimRight(strings.ReplaceAll(call.Result, "\r\n", "\n"), "\n")
	output = strings.TrimSpace(strings.TrimPrefix(output, "tool error:"))
	lines := strings.Split(output, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	isShell := call.Name == "bash" || call.Name == "powershell"
	// 2.命令的退出状态在最后一行，输出在它上面
	if isShell && shellStatusLine.MatchString(last) {
		body := strings.TrimSpace(strings.TrimRight(strings.Join(lines[:len(lines)-1], "\n"), "\n"))
		headline := last
		if code, ok := strings.CutPrefix(last, "Command exited with code "); ok {
			headline = "Exit code " + code
		}
		return "Error: " + headline, body
	}
	if last == "Operation aborted" {
		return "Error: Operation aborted", ""
	}
	// 3.其余工具只报原因；命令把整段输出当作原因
	reason := strings.TrimSpace(stripGeneratedFooter(output))
	if reason == "" {
		return "Error", ""
	}
	if isShell {
		return "Error", reason
	}
	return "Error: " + oneLineText(reason, 0), ""
}

// lookKind 是一种“只看不改”的探索方式
type lookKind string

const (
	lookNone   lookKind = ""
	lookSearch lookKind = "search"
	lookRead   lookKind = "read"
	lookList   lookKind = "list"
)

var (
	searchPrograms = map[string]bool{"find": true, "grep": true, "rg": true, "ag": true, "ack": true, "locate": true, "which": true, "whereis": true, "fd": true, "fdfind": true}
	readPrograms   = map[string]bool{"cat": true, "head": true, "tail": true, "less": true, "more": true, "wc": true, "stat": true, "file": true, "strings": true, "jq": true, "cut": true, "sort": true, "uniq": true, "tr": true, "type": true}
	listPrograms   = map[string]bool{"ls": true, "tree": true, "du": true, "dir": true}
	neutralProgram = map[string]bool{"echo": true, "printf": true, "true": true, "false": true, ":": true, "cd": true, "pushd": true, "popd": true}
	// 会写文件或运行别的程序的选项：命中就不进探索组
	writingFlags = map[string][]string{
		"find": {"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"},
		"fd":   {"-x", "-X", "--exec", "--exec-batch"},
		"rg":   {"--pre", "--hostname-bin"},
		"sort": {"-o", "--output", "--compress-program"},
		"tree": {"-o", "--output"},
		"git":  {"--open-files-in-pager"},
	}
	writingPrograms = map[string]bool{"xargs": true, "tee": true, "sed": true, "awk": true, "gawk": true, "mawk": true, "nawk": true, "bat": true, "batcat": true, "pt": true}
)

// shellSegments 按未加引号的分隔符切分命令
func shellSegments(command string) []string {
	var segments []string
	var current strings.Builder
	var quote rune
	for index := 0; index < len(command); index++ {
		character := rune(command[index])
		switch {
		case quote != 0:
			current.WriteRune(character)
			if character == quote {
				quote = 0
			}
		case character == '\'' || character == '"' || character == '`':
			quote = character
			current.WriteRune(character)
		case character == '\n' || character == ';':
			segments = append(segments, current.String())
			current.Reset()
		case character == '&' || character == '|':
			// `2>&1` 里的 & 属于重定向，不是分隔符
			text := current.String()
			if character == '&' && (strings.HasSuffix(text, ">") || strings.HasSuffix(text, "<")) {
				current.WriteRune(character)
				continue
			}
			segments = append(segments, text)
			current.Reset()
			// && 与 || 一起吞掉第二个符号
			if index+1 < len(command) && rune(command[index+1]) == character {
				index++
			}
		default:
			current.WriteRune(character)
		}
	}
	segments = append(segments, current.String())
	return segments
}

// shellWords 按空白切词，去掉引号
func shellWords(segment string) []string {
	var words []string
	var current strings.Builder
	var quote rune
	for _, character := range segment {
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
				continue
			}
			current.WriteRune(character)
		case character == '\'' || character == '"':
			quote = character
		case character == ' ' || character == '\t':
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(character)
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// harmlessRedirect 判断重定向是否只丢弃输出或合并流：
// `> /dev/null`、`2>&1`、`>&2`、`<&0` 都不写文件
func harmlessRedirect(segment string) bool {
	if !strings.ContainsAny(segment, "<>") {
		return true
	}
	for _, word := range strings.Fields(segment) {
		if !strings.ContainsAny(word, "<>") {
			continue
		}
		switch {
		case strings.Contains(word, "/dev/null"):
		case isDescriptorCopy(word):
		default:
			return false
		}
	}
	return true
}

// stripRedirectWords 去掉重定向词及其目标，只留下命令词
func stripRedirectWords(words []string) []string {
	result := make([]string, 0, len(words))
	for index := 0; index < len(words); index++ {
		word := words[index]
		if !strings.ContainsAny(word, "<>") {
			result = append(result, word)
			continue
		}
		// 重定向后面紧跟的目标词（如 `> out.txt`）也要跳过
		if index+1 < len(words) && !strings.ContainsAny(words[index+1], "<>") && !strings.HasPrefix(word, "2>&") && !strings.HasPrefix(word, ">&") && !strings.HasPrefix(word, "<&") {
			index++
		}
	}
	return result
}

// isDescriptorCopy 判断一个重定向词是否只是把流接到另一个描述符：`2>&1`、`>&2`、`<&0`
func isDescriptorCopy(word string) bool {
	if !strings.Contains(word, "&") {
		return false
	}
	operators := []string{">&", "<&"}
	for _, operator := range operators {
		index := strings.Index(word, operator)
		if index < 0 {
			continue
		}
		target := strings.Trim(word[index+len(operator):], " ;|")
		if target == "-" {
			return true
		}
		if target == "" {
			return false
		}
		for _, r := range target {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// writesOrRuns 判断一条命令能否写文件或运行别的程序
func writesOrRuns(words []string) bool {
	if len(words) == 0 {
		return false
	}
	program := strings.ToLower(words[0])
	args := words[1:]
	if flags, ok := writingFlags[program]; ok {
		for _, arg := range args {
			for _, flag := range flags {
				if arg == flag || strings.HasPrefix(arg, flag+"=") {
					return true
				}
			}
		}
	}
	if program == "sed" {
		for _, arg := range args {
			if arg == "--in-place" || strings.HasPrefix(arg, "--in-place=") || strings.HasPrefix(arg, "-i") || strings.Contains(arg, "w ") || strings.HasSuffix(arg, "w") {
				return true
			}
		}
	}
	if program == "awk" || program == "gawk" || program == "mawk" || program == "nawk" {
		for _, arg := range args {
			if strings.Contains(arg, ">") || strings.Contains(arg, "|") || strings.Contains(arg, "system(") {
				return true
			}
		}
	}
	if program == "git" {
		// git grep 是搜索，其余子命令都在运行程序
		if len(args) == 0 || args[0] != "grep" {
			return true
		}
		// git grep -O 会把匹配交给 pager 打开
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "-O") || strings.HasPrefix(arg, "--open-files-in-pager") {
				return true
			}
		}
		return false
	}
	return writingPrograms[program]
}

// opaqueCommand 判断命令是否含无法静态判定的构造：后台、子 shell、heredoc、命令替换
func opaqueCommand(command string) bool {
	if strings.Contains(command, "$(") || strings.Contains(command, "`") || strings.Contains(command, "<<") {
		return true
	}
	// 1.只有单个 & 表示后台；&& 是连接符，`2>&1` 里的 & 是描述符拷贝
	for index := 0; index < len(command); index++ {
		if command[index] != '&' {
			continue
		}
		next, previous := byte(0), byte(0)
		if index+1 < len(command) {
			next = command[index+1]
		}
		if index > 0 {
			previous = command[index-1]
		}
		if next == '&' || previous == '&' {
			continue
		}
		if previous == '>' || previous == '<' {
			continue
		}
		return true
	}
	return false
}

// classifyShellCommand 判断一条命令是否只是在“看”，以及怎么看；
// 任何可能写文件或运行别的程序的分支都返回 lookNone，组里就不会藏住它
func classifyShellCommand(command string) lookKind {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || opaqueCommand(trimmed) {
		return lookNone
	}
	segments := shellSegments(trimmed)
	var search, read, list bool
	for _, segment := range segments {
		if !harmlessRedirect(segment) {
			return lookNone
		}
		words := shellWords(segment)
		if len(words) == 0 {
			continue
		}
		// 重定向的目标不是命令词，切完词后要先摘掉，避免把 `1` 当成程序名
		words = stripRedirectWords(words)
		// 跳过 sudo/env 之类的包装与变量赋值
		for len(words) > 0 && (strings.Contains(words[0], "=") || words[0] == "sudo" || words[0] == "env" || words[0] == "time" || words[0] == "command") {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		program := strings.ToLower(words[0])
		if strings.Contains(program, "/") {
			program = program[strings.LastIndex(program, "/")+1:]
		}
		if writesOrRuns(words) {
			return lookNone
		}
		switch {
		case neutralProgram[program]:
		case program == "git":
			// git grep 归到搜索，其它 git 子命令已在 writesOrRuns 里挡掉
			search = true
		case searchPrograms[program]:
			search = true
		case readPrograms[program]:
			read = true
		case listPrograms[program]:
			list = true
		default:
			return lookNone
		}
	}
	// 1.同一条命令只算一次：列目录优先，其次搜索，最后读取
	switch {
	case list:
		return lookList
	case search:
		return lookSearch
	case read:
		return lookRead
	}
	return lookNone
}

// lookKindOf 返回一个工具调用是否只是在看；只看内置工具与只读命令
func lookKindOf(call ToolCallView) lookKind {
	switch call.Name {
	case "read":
		return lookRead
	case "grep", "find":
		return lookSearch
	case "ls":
		return lookList
	case "bash", "powershell":
		// 只有 shell 需要读命令内容，其余工具不必解码参数
		return classifyShellCommand(argString(toolArgs(call), "command"))
	}
	return lookNone
}

// toolRowGroupPhrases 是每种探索在组摘要里的措辞，顺序固定
var toolRowGroupPhrases = []struct {
	kind             lookKind
	running, done    string
	singular, plural string
}{
	{lookSearch, "searching for", "searched for", "pattern", "patterns"},
	{lookRead, "reading", "read", "file", "files"},
	{lookList, "listing", "listed", "directory", "directories"},
}

// toolRowGroupCounts 数一个组覆盖了几个模式、文件、目录：
// 文件按不同路径去重，没有读到文件时按只读命令数计
func toolRowGroupCounts(calls []ToolCallView) map[lookKind]int {
	counts := map[lookKind]int{}
	paths := map[string]bool{}
	readCommands := 0
	for _, call := range calls {
		kind := lookKindOf(call)
		switch {
		case kind == lookRead && call.Name == "read":
			paths[toolPath(toolArgs(call))] = true
		case kind == lookRead:
			readCommands++
		case kind != lookNone:
			counts[kind]++
		}
	}
	if len(paths) > 0 {
		counts[lookRead] = len(paths)
	} else {
		counts[lookRead] = readCommands
	}
	return counts
}

// groupRunning 判断组里是否还有调用在跑
func groupRunning(calls []ToolCallView) bool {
	for _, call := range calls {
		if toolRowStatus(call) == "running" {
			return true
		}
	}
	return false
}

// summarizeToolRowGroup 拼出 “Searched for 1 pattern, read 2 files”；
// 有调用还在跑时每个动词都读进行时
func summarizeToolRowGroup(calls []ToolCallView) string {
	counts := toolRowGroupCounts(calls)
	running := groupRunning(calls)
	var parts []string
	for _, phrase := range toolRowGroupPhrases {
		count := counts[phrase.kind]
		if count == 0 {
			continue
		}
		verb := phrase.done
		if running {
			verb = phrase.running
		}
		noun := phrase.plural
		if count == 1 {
			noun = phrase.singular
		}
		parts = append(parts, fmt.Sprintf("%s %d %s", verb, count, noun))
	}
	text := strings.Join(parts, ", ")
	if text == "" {
		return ""
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// groupHint 在组运行中给出正在看的东西：一个路径、一个模式、一条命令
func groupHint(calls []ToolCallView) string {
	for index := len(calls) - 1; index >= 0; index-- {
		call := calls[index]
		if toolRowStatus(call) != "running" {
			continue
		}
		args := toolArgs(call)
		switch call.Name {
		case "grep", "find":
			if pattern := oneLineText(argString(args, "pattern"), 0); pattern != "" {
				return `"` + pattern + `"`
			}
			return ""
		case "bash", "powershell":
			if command := oneLineText(argString(args, "command"), 0); command != "" {
				return "$ " + command
			}
			return ""
		case "ls":
			if path := toolPath(args); path != "" {
				return path
			}
			return "."
		default:
			return toolPath(args)
		}
	}
	return ""
}

// groupFailures 保留组里每个失败的原因，收起的组也要看得见
func groupFailures(calls []ToolCallView) []string {
	var failures []string
	for _, call := range calls {
		if toolRowStatus(call) != "error" {
			continue
		}
		headline, _ := toolRowFailure(call)
		failures = append(failures, headline)
	}
	return failures
}
