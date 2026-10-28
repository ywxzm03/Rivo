package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// 工具行的排版常量：状态点后一个空格、结果挂在 ⎿ 下、正文再缩进两列
const (
	// rowResultPrefix 是 `⦁ Name(...)` 之后结果行的前缀，`⎿` 落在工具名首字母列
	rowResultPrefix = "   ⎿  "
	// rowIndent 是 ⎿ 之后各行与 diff/代码的统一缩进
	rowIndent = "      "
	// rowTitleIndent 是调用行换行后的续行前缀，落在工具名下方
	rowTitleIndent = "   "

	// rowOutputRows 是收起的命令输出行数上限
	rowOutputRows = 3
	// rowWriteRows 是收起的 write 预览行数上限
	rowWriteRows = 10
	// rowDiffContext 是 diff 每个变更两侧保留的上下文行数
	rowDiffContext = 3
	// rowCommandLines / rowCommandChars 是收起命令的截断阈值
	rowCommandLines = 2
	rowCommandChars = 160
	// rowArgumentChars 是收起状态下通用参数行的截断宽度
	rowArgumentChars = 120
	// rowExpandHint 提示点击可展开
	rowExpandHint = "(click to expand)"
)

// toolRowLabels 是各工具在调用行上的显示名
var toolRowLabels = map[string]string{
	"read":       "Read",
	"grep":       "Search",
	"find":       "Search",
	"ls":         "List",
	"bash":       "Bash",
	"powershell": "PowerShell",
	"edit":       "Update",
	"write":      "Write",
	"agent":      "Agent",
	"TaskStop":   "TaskStop",
}

func toolRowLabel(name string) string {
	if label, ok := toolRowLabels[name]; ok {
		return label
	}
	return name
}

// toolArgs 解析工具参数；参数还没流完时返回空表
func toolArgs(call ToolCallView) map[string]any {
	if strings.TrimSpace(call.Arguments) == "" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return nil
	}
	return args
}

func argString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

// toolPath 取路径参数并压缩 home 前缀
func toolPath(args map[string]any) string {
	path := argString(args, "path")
	if path == "" {
		return ""
	}
	return shortenToolPath(path)
}

// oneLineText 把多行文本压成一行，超出长度加省略号
func oneLineText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit > 1 && ansi.StringWidth(text) > limit {
		return ansi.Truncate(text, limit, "…")
	}
	return text
}

// toolRowCommand 收起时只在前两行或 160 字符处截断，展开时给全文
func toolRowCommand(command string, expanded bool) string {
	text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(command, "\r\n", "\n"), "\r", "\n"))
	if expanded {
		return text
	}
	lines := strings.Split(text, "\n")
	shown := text
	if len(lines) > rowCommandLines {
		shown = strings.Join(lines[:rowCommandLines], "\n")
	}
	if len(shown) > rowCommandChars {
		shown = shown[:rowCommandChars]
	}
	if shown == text {
		return text
	}
	return strings.TrimRight(shown, "\n ") + "…"
}

// formatArgValue 把一个参数值写成调用行里的写法：字符串带引号，标量原样，简单数组用方括号
func formatArgValue(value any) string {
	switch value := value.(type) {
	case string:
		return `"` + oneLineText(value, 0) + `"`
	case float64:
		if value == float64(int64(value)) {
			return fmt.Sprintf("%d", int64(value))
		}
		return fmt.Sprintf("%v", value)
	case bool:
		return fmt.Sprintf("%t", value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			switch item.(type) {
			case map[string]any, []any, nil:
				return ""
			}
			parts = append(parts, formatArgValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return ""
	}
}

// argumentOrder 让常用参数按固定顺序排在前面，其余按字母序
var argumentOrder = []string{"command", "query", "queries", "q", "url", "urls", "path", "paths", "pattern", "prompt", "question", "claim", "name", "id", "tool"}

// formatArguments 把自定义工具的参数排成 `query: "x", limit: 5`
func formatArguments(args map[string]any) string {
	keys := make([]string, 0, len(args))
	seen := make(map[string]bool, len(args))
	for _, key := range argumentOrder {
		if _, ok := args[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var rest []string
	for key := range args {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if formatted := formatArgValue(args[key]); formatted != "" {
			parts = append(parts, key+": "+formatted)
		}
	}
	return strings.Join(parts, ", ")
}

// toolRowArgument 是括号里的内容：`Bash(npm test)`、`Search(pattern: "x", path: "src")`
func toolRowArgument(name string, args map[string]any, expanded bool) string {
	path := toolPath(args)
	switch name {
	case "bash", "powershell":
		return toolRowCommand(argString(args, "command"), expanded)
	case "grep", "find":
		pattern := oneLineText(argString(args, "pattern"), 0)
		if pattern == "" {
			return ""
		}
		if path == "" {
			return `pattern: "` + pattern + `"`
		}
		return `pattern: "` + pattern + `", path: "` + path + `"`
	case "ls":
		if path == "" {
			return "."
		}
		return path
	case "read", "edit", "write":
		return path
	case "agent":
		// 子任务调用的关键信息是描述与类型;prompt 正文默认折叠
		description := oneLineText(argString(args, "description"), 0)
		kind := oneLineText(argString(args, "subagent_type"), 0)
		if kind == "" {
			kind = "fork"
		}
		line := kind
		if description != "" {
			line = kind + ": " + description
		}
		return oneLineText(line, rowArgumentChars)
	default:
		return oneLineText(formatArguments(args), rowArgumentChars)
	}
}

// toolRowStatus 把投影状态映射成四种行状态：
// 参数还在流式写入（ArgsComplete 为假）时算 running，写长参数的调用也会呼吸；
// 参数写完但工具还没开始执行的短暂间隙算 pending，点不再呼吸
func toolRowStatus(call ToolCallView) string {
	switch call.Status {
	case ToolPending:
		if call.ArgsComplete && call.StartedAt.IsZero() {
			return "pending"
		}
		return "running"
	case ToolError, ToolInterrupted:
		return "error"
	default:
		return "success"
	}
}

// toolRowDot 画状态点：运行中在灰度之间呼吸，成功绿、失败红
func toolRowDot(call ToolCallView, now time.Time) string {
	switch toolRowStatus(call) {
	case "running":
		// 呼吸按 45ms 一帧、14 帧一轮，最暗不低于 8% 的强度
		frame := (now.UnixMilli() / 45) % 14
		wave := (cosine(frame, 14) + 1) / 2
		strength := 0.08 + 0.92*wave
		return colored(mixHex(backgroundFill, textColor, strength), "⦁")
	case "pending":
		return colored(dimColor, "⦁")
	case "error":
		return colored(redColor, "⦁")
	default:
		return colored(greenColor, "⦁")
	}
}

// cosine 返回 frame 在一轮里的余弦值，用来做平滑呼吸
func cosine(frame, total int64) float64 {
	if total == 0 {
		return 0
	}
	// 角度已归一化到一轮内，直接按弧度求值
	radians := 2 * 3.141592653589793 * float64(frame) / float64(total)
	return mathCos(radians)
}

// toolRowCallTitle 返回调用行（不含状态点），换行由调用方按宽度处理
func toolRowCallTitle(call ToolCallView, expanded bool) string {
	args := toolArgs(call)
	label := toolRowLabel(call.Name)
	argument := toolRowArgument(call.Name, args, expanded)
	if argument == "" {
		return boldText(label)
	}
	return boldText(label) + colored(mutedColor, "("+argument+")")
}

// wrapToolRowTitle 把调用行按宽度换行：状态点在首行，续行对齐到工具名下方
func wrapToolRowTitle(dot, title string, width int) []string {
	available := max(1, width-ansi.StringWidth(rowTitleIndent)-1)
	var rows []string
	for line := range strings.SplitSeq(title, "\n") {
		rows = append(rows, hardWrap(line, available)...)
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	rows[0] = " " + dot + " " + rows[0]
	for index := 1; index < len(rows); index++ {
		// 续行对齐到工具名下方，换行处落在空格上时不再留出额外缩进
		rows[index] = rowTitleIndent + strings.TrimLeft(rows[index], " ")
	}
	return rows
}

// hardWrap 按显示宽度硬换行，保留原有 ANSI
func hardWrap(text string, width int) []string {
	width = max(1, width)
	if ansi.StringWidth(text) <= width {
		return []string{text}
	}
	return strings.Split(ansi.Hardwrap(text, width, true), "\n")
}

// renderToolRow 画一条调用行和它的结果块
func renderToolRow(call ToolCallView, width int, expanded bool, now time.Time) []string {
	lines := wrapToolRowTitle(toolRowDot(call, now), toolRowCallTitle(call, expanded), width)
	return append(lines, renderToolRowResult(call, width, expanded)...)
}

// renderToolRowResult 画 ⎿ 起头的结果块
func renderToolRowResult(call ToolCallView, width int, expanded bool) []string {
	args := toolArgs(call)
	// 结果区只在工具开始执行后才有，pending 阶段只画调用行；
	// 点的呼吸与结果区是否存在是两件事
	if call.Status == ToolPending && call.StartedAt.IsZero() {
		return nil
	}
	switch toolRowStatus(call) {
	case "pending", "running":
		return toolRowSentence("Running…", width)
	case "error":
		headline, detail := toolRowFailure(call)
		lines := toolRowSentence(headline, width)
		if detail == "" {
			return lines
		}
		return append(lines, toolRowOutputWithPrefix(detail, width, expanded, true, rowIndent)...)
	}
	// 成功：先一句结果，再按工具补正文
	body := toolRowBody(call, args, width, expanded)
	if outcome := toolRowOutcome(call); outcome != "" {
		return append(toolRowSentence(outcome, width), body...)
	}
	return append(toolRowOutputWithPrefix(call.Result, width, expanded, false, rowResultPrefix), body...)
}

// toolRowSentence 把一句话挂在 ⎿ 下，超出宽度按词换行
func toolRowSentence(text string, width int) []string {
	available := max(1, width-ansi.StringWidth(rowResultPrefix))
	rows := wrapWords(text, available)
	lines := make([]string, 0, len(rows))
	for index, row := range rows {
		if index == 0 {
			lines = append(lines, colored(dimColor, rowResultPrefix)+row)
			continue
		}
		lines = append(lines, rowIndent+row)
	}
	return lines
}

// wrapWords 按词换行，超长单词再按宽度硬切
func wrapWords(text string, width int) []string {
	width = max(1, width)
	var rows []string
	for paragraph := range strings.SplitSeq(text, "\n") {
		if paragraph == "" {
			rows = append(rows, "")
			continue
		}
		line := ""
		for word := range strings.SplitSeq(paragraph, " ") {
			switch {
			case line == "":
				line = word
			case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= width:
				line += " " + word
			default:
				rows = append(rows, line)
				line = word
			}
			for ansi.StringWidth(line) > width {
				rows = append(rows, ansi.Truncate(line, width, ""))
				line = ansi.Truncate(line, ansi.StringWidth(line)-width, "")
				line = strings.TrimLeft(line, " ")
			}
		}
		rows = append(rows, line)
	}
	return rows
}

func toolRowOutputWithPrefix(output string, width int, expanded bool, isError bool, firstPrefix string) []string {
	text := strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(output, "\r\n", "\n"), "\r", "\n"), "\n")
	if text == "" || (!expanded && text == "(no output)") {
		return toolRowSentence("(No output)", width)
	}
	color := mutedColor
	if isError {
		color = redColor
	}
	rows := make([]string, 0, 8)
	for line := range strings.SplitSeq(text, "\n") {
		rows = append(rows, hardWrap(colored(color, line), max(1, width-ansi.StringWidth(rowIndent)))...)
	}
	hidden := 0
	if !expanded && len(rows) > rowOutputRows+1 {
		hidden = len(rows) - rowOutputRows
		rows = rows[:rowOutputRows]
	}
	lines := make([]string, 0, len(rows)+1)
	for index, row := range rows {
		if index == 0 {
			lines = append(lines, colored(dimColor, firstPrefix)+row)
			continue
		}
		lines = append(lines, rowIndent+row)
	}
	if hidden > 0 {
		lines = append(lines, rowIndent+toolRowMoreLines(hidden))
	}
	return lines
}

// toolRowMoreLines 是“还有多少行”的提示
func toolRowMoreLines(hidden int) string {
	noun := "lines"
	if hidden == 1 {
		noun = "line"
	}
	return colored(dimColor, fmt.Sprintf("… +%d %s (ctrl+o to expand)", hidden, noun))
}

// toolRowBody 是结果句之后的正文：edit 的 diff、write 的预览、展开后的 read 正文
func toolRowBody(call ToolCallView, args map[string]any, width int, expanded bool) []string {
	path := argString(args, "path")
	switch call.Name {
	case "edit":
		lines := trimRowDiff(parseRowDiff(toolRowDiffPatch(call)), rowDiffContext)
		return renderRowDiff(lines, path, rowIndent, width)
	case "write":
		content := strings.TrimRight(argString(args, "content"), "\n")
		if content == "" {
			return nil
		}
		rows := strings.Split(content, "\n")
		hidden := 0
		if !expanded && len(rows) > rowWriteRows {
			hidden = len(rows) - rowWriteRows
			rows = rows[:rowWriteRows]
		}
		lines := renderRowCode(rows, path, 1, rowIndent, "")
		if hidden > 0 {
			lines = append(lines, rowIndent+toolRowMoreLines(hidden))
		}
		return lines
	case "read":
		if !expanded {
			return nil
		}
		body, footer := splitReadFooter(call.Result)
		rows := strings.Split(strings.TrimRight(body, "\n"), "\n")
		if len(rows) == 1 && rows[0] == "" {
			return nil
		}
		startLine := readStartLine(args)
		return renderRowCode(rows, path, startLine, rowIndent, footer)
	default:
		return nil
	}
}

// toolDetails 是工具结果里工具行会读的字段，对应 tool.EditDetails
type toolDetails struct {
	Patch string
}

// toolRowDiffPatch 取 edit 结果里的统一 patch
func toolRowDiffPatch(call ToolCallView) string {
	if strings.TrimSpace(call.Details) == "" {
		return ""
	}
	var details toolDetails
	if err := json.Unmarshal([]byte(call.Details), &details); err != nil {
		return ""
	}
	return details.Patch
}

// readStartLine 取 read 的起始行，offset 是模型给的 JSON 数字
func readStartLine(args map[string]any) int {
	if value, ok := args["offset"].(float64); ok && value >= 1 {
		return int(value)
	}
	return 1
}

// shortenToolPath 把 home 目录压成 ~
func shortenToolPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && (path == home || strings.HasPrefix(path, home+string(os.PathSeparator))) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// mathCos 是余弦的泰勒近似，只用于呼吸明暗，精度足够且不引入 math 依赖
func mathCos(x float64) float64 {
	const twoPi = 6.283185307179586
	for x > twoPi {
		x -= twoPi
	}
	for x < -twoPi {
		x += twoPi
	}
	term, sum := 1.0, 1.0
	for n := 1; n <= 8; n++ {
		term *= -x * x / float64((2*n-1)*(2*n))
		sum += term
	}
	return sum
}
