package tui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
)

// diffKind 是一种 diff 行的类别
type diffKind uint8

const (
	diffContext diffKind = iota
	diffAdd
	diffRemove
	diffSeparator
)

// rowDiffLine 是解析后的一行 diff；separator 只有 ⋮ 标记，没有行号
type rowDiffLine struct {
	Kind    diffKind
	Number  int
	Content string
}

var unifiedHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseRowDiff 解析统一 patch
// tab 在这里统一展开：显示宽度与词级高亮的字符下标都基于展开后的文本
func parseRowDiff(patch string) []rowDiffLine {
	lines := parseUnifiedDiff(patch)
	for index := range lines {
		lines[index].Content = expandTabs(lines[index].Content)
	}
	return lines
}

func parseUnifiedDiff(patch string) []rowDiffLine {
	var lines []rowDiffLine
	oldLine, newLine := 0, 0
	inHunk := false
	for line := range strings.SplitSeq(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		if match := unifiedHunkHeader.FindStringSubmatch(line); match != nil {
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[2])
			// 两个 hunk 之间用 ⋮ 断开
			if inHunk {
				lines = append(lines, rowDiffLine{Kind: diffSeparator, Content: "⋮"})
			}
			inHunk = true
			continue
		}
		if !inHunk || line == "" || strings.HasPrefix(line, `\ No newline`) {
			continue
		}
		content := line[1:]
		switch line[0] {
		case '+':
			lines = append(lines, rowDiffLine{Kind: diffAdd, Number: newLine, Content: content})
			newLine++
		case '-':
			lines = append(lines, rowDiffLine{Kind: diffRemove, Number: oldLine, Content: content})
			oldLine++
		case ' ':
			lines = append(lines, rowDiffLine{Kind: diffContext, Number: newLine, Content: content})
			oldLine++
			newLine++
		}
	}
	return lines
}

// trimRowDiff 只保留每个变更附近 contextLines 行上下文，其余用 ⋮ 代替
func trimRowDiff(lines []rowDiffLine, contextLines int) []rowDiffLine {
	kept := make([]bool, len(lines))
	for i, line := range lines {
		// 变更与 hunk 断点都保留，只有离变更太远的上下文会被裁掉
		kept[i] = line.Kind != diffContext
	}
	for i, line := range lines {
		if line.Kind != diffAdd && line.Kind != diffRemove {
			continue
		}
		for offset := 1; offset <= contextLines; offset++ {
			if i-offset >= 0 && lines[i-offset].Kind == diffContext {
				kept[i-offset] = true
			}
			if i+offset < len(lines) && lines[i+offset].Kind == diffContext {
				kept[i+offset] = true
			}
		}
	}
	result := make([]rowDiffLine, 0, len(lines))
	// 1.被裁掉的一段只产生一个标记；hunk 自带的分隔符与它合并，不会连出两个 ⋮
	gap := false
	for i, line := range lines {
		if !kept[i] {
			gap = len(result) > 0
			continue
		}
		if gap && line.Kind != diffSeparator && result[len(result)-1].Kind != diffSeparator {
			result = append(result, rowDiffLine{Kind: diffSeparator, Content: "⋮"})
		}
		gap = false
		result = append(result, line)
	}
	return result
}

// 高亮上限：超过这些规模的代码，着色比可读性更贵
const (
	highlightMaxChars     = 512 * 1024
	highlightMaxLines     = 10_000
	highlightMaxLineChars = 4 * 1024
)

// styledCell 是一个字符连同它的前景色
type styledCell struct {
	Text string
	FG   string
}

// toolRowSyntaxStyle 把 chroma 的 token 映射到 Rivo 的语法色
var toolRowSyntaxStyle = chroma.MustNewStyle("rivo", chroma.StyleEntries{
	chroma.Text:                codeTextColor,
	chroma.Keyword:             syntaxKeywordColor,
	chroma.KeywordType:         syntaxTypeColor,
	chroma.KeywordNamespace:    syntaxKeywordColor,
	chroma.NameFunction:        syntaxTypeColor,
	chroma.NameClass:           syntaxTypeColor,
	chroma.NameBuiltin:         syntaxTypeColor,
	chroma.NameDecorator:       syntaxTypeColor,
	chroma.NameTag:             syntaxTypeColor,
	chroma.NameAttribute:       syntaxTypeColor,
	chroma.NameConstant:        syntaxLiteralColor,
	chroma.Name:                variableColor,
	chroma.LiteralString:       stringColor,
	chroma.LiteralStringEscape: stringColor,
	chroma.LiteralNumber:       syntaxLiteralColor,
	chroma.Literal:             syntaxLiteralColor,
	chroma.Comment:             syntaxCommentColor,
	chroma.Operator:            syntaxKeywordColor,
	chroma.Punctuation:         codeTextColor,
	chroma.GenericDeleted:      redColor,
	chroma.GenericInserted:     greenColor,
})

// highlightRowLines 按文件后缀整块着色，让多行字符串和注释保持状态；
// 无法识别语言或规模过大时返回 nil，调用方退回单色
func highlightRowLines(contents []string, path string) [][]styledCell {
	if len(contents) == 0 || len(contents) > highlightMaxLines {
		return nil
	}
	total := 0
	for _, content := range contents {
		if len(content) > highlightMaxLineChars {
			return nil
		}
		total += len(content) + 1
		if total > highlightMaxChars {
			return nil
		}
	}
	lexer := lexers.Match(path)
	if lexer == nil {
		return nil
	}
	iterator, err := lexer.Tokenise(nil, strings.Join(contents, "\n"))
	if err != nil {
		return nil
	}
	lines := make([][]styledCell, len(contents))
	line := 0
	// 1.逐 token 拆行：token 内部的换行推进目标行，同一行的片段按顺序累积
	for _, token := range iterator.Tokens() {
		color := codeTextColor
		if entry := toolRowSyntaxStyle.Get(token.Type); entry.Colour.IsSet() {
			color = "#" + strings.TrimPrefix(strings.ToLower(entry.Colour.String()), "#")
		}
		pieces := strings.Split(token.Value, "\n")
		for index, piece := range pieces {
			if piece != "" && line < len(lines) {
				lines[line] = append(lines[line], styledCell{Text: piece, FG: color})
			}
			if index < len(pieces)-1 {
				line++
			}
		}
	}
	return lines
}

// highlightedCells 取一行的着色片段；无法着色时退回单色，调用方不必再判空
func highlightedCells(highlighted [][]styledCell, index int, content string) []styledCell {
	if index < len(highlighted) && len(highlighted[index]) > 0 {
		return highlighted[index]
	}
	return []styledCell{{Text: content, FG: codeTextColor}}
}

// expandTabs 把制表符按制表位展开成空格，与粘贴内容的制表宽度保持一致
func expandTabs(text string) string {
	if !strings.ContainsRune(text, '\t') {
		return text
	}
	var builder strings.Builder
	column := 0
	for _, r := range text {
		if r != '\t' {
			builder.WriteRune(r)
			column++
			continue
		}
		width := pasteTabWidth - column%pasteTabWidth
		builder.WriteString(strings.Repeat(" ", width))
		column += width
	}
	return builder.String()
}

// 词级高亮：一对被替换的行共享大部分字符时，只把不同的词染得更亮
const (
	wordDiffMaxCells      = 40_000
	wordDiffMinSimilarity = 0.6
)

type charRange struct{ Start, End int }

func diffTokenKind(r rune) int {
	switch {
	case r == ' ' || r == '\t':
		return 0
	case r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127:
		return 1
	default:
		return 2
	}
}

// splitDiffTokens 把一行切成词、空白、标点三类片段，保留各自的原始偏移
func splitDiffTokens(text string) []string {
	var tokens []string
	start := -1
	kind := 0
	for index, r := range text {
		current := diffTokenKind(r)
		if start < 0 {
			start, kind = index, current
			continue
		}
		if current != kind {
			tokens = append(tokens, text[start:index])
			start, kind = index, current
		}
	}
	if start >= 0 {
		tokens = append(tokens, text[start:])
	}
	return tokens
}

// wordDiffRanges 用最长公共子序列找出两行里各自不共享的词
func wordDiffRanges(removed, added string) (removedRanges, addedRanges []charRange) {
	before, after := splitDiffTokens(removed), splitDiffTokens(added)
	if len(before)*len(after) > wordDiffMaxCells {
		return nil, nil
	}
	width := len(after) + 1
	lengths := make([]int, (len(before)+1)*width)
	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				lengths[i*width+j] = lengths[(i+1)*width+j+1] + 1
			} else if left, down := lengths[(i+1)*width+j], lengths[i*width+j+1]; left >= down {
				lengths[i*width+j] = left
			} else {
				lengths[i*width+j] = down
			}
		}
	}
	removedChanged := make([]bool, len(before))
	addedChanged := make([]bool, len(after))
	for i := range removedChanged {
		removedChanged[i] = true
	}
	for i := range addedChanged {
		addedChanged[i] = true
	}
	shared := 0
	for i, j := 0, 0; i < len(before) && j < len(after); {
		if before[i] == after[j] {
			removedChanged[i], addedChanged[j] = false, false
			shared += len(before[i])
			i++
			j++
		} else if lengths[(i+1)*width+j] >= lengths[i*width+j+1] {
			i++
		} else {
			j++
		}
	}
	if shared == 0 || float64(2*shared) < wordDiffMinSimilarity*float64(len(removed)+len(added)) {
		return nil, nil
	}
	// 2.合并相邻片段，并让区间起止落在词上而不是空白上
	ranges := func(text string, tokens []string, changed []bool) []charRange {
		var output []charRange
		offset := 0
		for index, token := range tokens {
			if changed[index] {
				if len(output) > 0 && output[len(output)-1].End == offset {
					output[len(output)-1].End = offset + len(token)
				} else {
					output = append(output, charRange{Start: offset, End: offset + len(token)})
				}
			}
			offset += len(token)
		}
		kept := output[:0]
		for _, current := range output {
			piece := text[current.Start:current.End]
			current.Start += len(piece) - len(strings.TrimLeft(piece, " \t"))
			current.End -= len(piece) - len(strings.TrimRight(piece, " \t"))
			if current.End > current.Start {
				kept = append(kept, current)
			}
		}
		return kept
	}
	return ranges(removed, before, removedChanged), ranges(added, after, addedChanged)
}

// pairWordRanges 把相邻的删除块与新增块按顺序配对，返回每行的词级区间
func pairWordRanges(lines []rowDiffLine) map[int][]charRange {
	marked := make(map[int][]charRange)
	for index := 0; index < len(lines); {
		if lines[index].Kind != diffRemove {
			index++
			continue
		}
		removedStart := index
		for index < len(lines) && lines[index].Kind == diffRemove {
			index++
		}
		addedStart := index
		for index < len(lines) && lines[index].Kind == diffAdd {
			index++
		}
		pairs := min(addedStart-removedStart, index-addedStart)
		for offset := range pairs {
			removedRanges, addedRanges := wordDiffRanges(lines[removedStart+offset].Content, lines[addedStart+offset].Content)
			if len(removedRanges) > 0 {
				marked[removedStart+offset] = removedRanges
			}
			if len(addedRanges) > 0 {
				marked[addedStart+offset] = addedRanges
			}
		}
	}
	return marked
}

func hexChannels(color string) (int, int, int, bool) {
	color = strings.TrimPrefix(color, "#")
	if len(color) != 6 {
		return 0, 0, 0, false
	}
	value, err := strconv.ParseUint(color, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(value>>16) & 0xff, int(value>>8) & 0xff, int(value) & 0xff, true
}

// mixHex 按比例把 from 混向 to，用于从主题色推出 diff 底色
func mixHex(from, to string, amount float64) string {
	fr, fg, fb, ok := hexChannels(from)
	if !ok {
		return to
	}
	tr, tg, tb, ok := hexChannels(to)
	if !ok {
		return from
	}
	blend := func(a, b int) int { return int(float64(a) + (float64(b)-float64(a))*amount + 0.5) }
	return "#" + twoHex(blend(fr, tr)) + twoHex(blend(fg, tg)) + twoHex(blend(fb, tb))
}

func twoHex(value int) string {
	value = min(255, max(0, value))
	const digits = "0123456789abcdef"
	return string([]byte{digits[value>>4], digits[value&0xf]})
}

func bgSequence(color string) string {
	r, g, b, ok := hexChannels(color)
	if !ok {
		return ""
	}
	return "\x1b[48;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}

func fgSequence(color string) string {
	r, g, b, ok := hexChannels(color)
	if !ok {
		return ""
	}
	return "\x1b[38;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}

// cell 是待着色的一段文字：前景色，以及它是否属于词级高亮的区间
type cell struct {
	Text string
	FG   string
	Word bool
}

// paintCells 把一串 cell 画出来；带底色时整行（含右侧填充）连续着色
func paintCells(cells []cell, baseFill, wordFill string, width int) string {
	var builder strings.Builder
	if baseFill != "" {
		builder.WriteString(bgSequence(baseFill))
	}
	used, lastColor, lastFill := 0, "", baseFill
	for _, current := range cells {
		if current.Text == "" {
			continue
		}
		if baseFill != "" {
			fill := baseFill
			if current.Word && wordFill != "" {
				fill = wordFill
			}
			if fill != lastFill {
				builder.WriteString(bgSequence(fill))
				lastFill = fill
			}
			if current.FG != lastColor {
				builder.WriteString(fgSequence(current.FG))
				lastColor = current.FG
			}
		} else if current.FG != lastColor {
			builder.WriteString(fgSequence(current.FG))
			lastColor = current.FG
		}
		builder.WriteString(current.Text)
		used += ansi.StringWidth(current.Text)
	}
	if baseFill != "" {
		if width > used {
			builder.WriteString(strings.Repeat(" ", width-used))
		}
		builder.WriteString("\x1b[39m\x1b[49m")
	}
	return builder.String()
}

// inWords 判断一个字节偏移是否落在任一高亮区间内
func inWords(offset int, words []charRange) bool {
	for _, current := range words {
		if offset >= current.Start && offset < current.End {
			return true
		}
	}
	return false
}

// cellsFromLine 把一行的高亮片段展开成 cell，并按词级区间切成同色同标记的连续段
func cellsFromLine(segments []styledCell, words []charRange) []cell {
	cells := make([]cell, 0, len(segments))
	offset := 0
	for _, segment := range segments {
		if segment.Text == "" {
			continue
		}
		if len(words) == 0 {
			cells = append(cells, cell{Text: segment.Text, FG: segment.FG})
			offset += len(segment.Text)
			continue
		}
		// 1.逐 rune 判定是否命中词级区间，相邻同标记的 rune 合并成一段
		var run strings.Builder
		marked := false
		flush := func() {
			if run.Len() > 0 {
				cells = append(cells, cell{Text: run.String(), FG: segment.FG, Word: marked})
				run.Reset()
			}
		}
		for _, r := range segment.Text {
			current := inWords(offset, words)
			if run.Len() > 0 && current != marked {
				flush()
			}
			marked = current
			run.WriteRune(r)
			offset += len(string(r))
		}
		flush()
	}
	return cells
}

// diffGutter 画行号与符号两列。变更行的行号与符号用同一个变更色，
// 上下文行的行号用 dim、符号列用竖线。
// tint 非空时只用 SGR 前景码而不重置，底色因此能连续铺到行尾
func diffGutter(number int, kind diffKind, numberWidth int, tint string) string {
	text := strconv.Itoa(number)
	text = strings.Repeat(" ", numberWidth-len(text)) + text
	switch kind {
	case diffAdd:
		return paintGutter(tint, greenColor, " "+text+" ") + paintGutter(tint, greenColor, "+")
	case diffRemove:
		return paintGutter(tint, redColor, " "+text+" ") + paintGutter(tint, redColor, "-")
	default:
		// 上下文行的符号列是一条竖线，把行号与正文隔开
		return paintGutter("", dimColor, " "+text+" ") + paintGutter("", dimColor, "│")
	}
}

// paintGutter 画 gutter 的一段：带底色时只写前景码，不带底色时用完整样式
func paintGutter(tint, color, text string) string {
	if tint == "" {
		return colored(color, text)
	}
	return fgSequence(color) + text
}

// renderRowDiff 画出带行号、符号、整行底色与语法高亮的 diff；indent 是每行前缀。
// 变更行的底色从行首一直铺到行尾（行号格子也带底色），超宽行按正文宽度换行，
// 续行重复符号并把行号列留空
func renderRowDiff(lines []rowDiffLine, path string, indent string, width int) []string {
	if len(lines) == 0 {
		return nil
	}
	numberWidth := 1
	for _, line := range lines {
		if line.Kind != diffSeparator {
			numberWidth = max(numberWidth, len(strconv.Itoa(line.Number)))
		}
	}
	contents := make([]string, len(lines))
	for i, line := range lines {
		contents[i] = line.Content
	}
	highlighted := highlightRowLines(contents, path)
	words := pairWordRanges(lines)
	// gutter 占 “空格 + 行号 + 空格 + 符号” 共 numberWidth+3 列，正文只能用在其余宽度上
	gutterWidth := numberWidth + 3
	bodyWidth := max(1, width-ansi.StringWidth(indent)-gutterWidth)

	output := make([]string, 0, len(lines))
	for index, line := range lines {
		if line.Kind == diffSeparator {
			output = append(output, indent+strings.Repeat(" ", numberWidth+1)+colored(dimColor, line.Content))
			continue
		}
		segments := highlightedCells(highlighted, index, line.Content)
		switch line.Kind {
		case diffContext:
			// 上下文行不着底色，超宽时按列切分，保证一行终端一行文本
			gutter := diffGutter(line.Number, line.Kind, numberWidth, "")
			body := paintCells(cellsFromLine(segments, nil), "", "", 0)
			output = append(output, indent+gutter+ansi.Truncate(body, bodyWidth, ""))
		default:
			output = append(output, renderTintedDiffRow(line, segments, words[index], numberWidth, indent, width, bodyWidth)...)
		}
	}
	return output
}

// renderTintedDiffRow 画一条变更行：整行连续底色，超宽时换行并重复符号
func renderTintedDiffRow(
	line rowDiffLine,
	segments []styledCell,
	wordRanges []charRange,
	numberWidth int,
	indent string,
	width int,
	bodyWidth int,
) []string {
	fill, wordFill := addedTint, addedWordTint
	if line.Kind == diffRemove {
		fill, wordFill = removedTint, removedWordTint
	}
	cells := cellsFromLine(segments, wordRanges)
	// 1.先按正文宽度切块，每块一行
	chunks := wrapCells(cells, bodyWidth)
	output := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		// 2.续行重复符号、行号留空
		gutter := diffContinuationGutter(line.Kind, numberWidth)
		if index == 0 {
			gutter = diffGutter(line.Number, line.Kind, numberWidth, fill)
		}
		body := paintCells(chunk, fill, wordFill, bodyWidth)
		output = append(output, indent+bgSequence(fill)+gutter+body)
	}
	return output
}

// diffContinuationGutter 是换行续行的 gutter：行号列留空，符号重复
func diffContinuationGutter(kind diffKind, numberWidth int) string {
	blank := strings.Repeat(" ", numberWidth+2)
	switch kind {
	case diffAdd:
		return bgSequence(addedTint) + colored(greenColor, blank+"+")
	case diffRemove:
		return bgSequence(removedTint) + colored(redColor, blank+"-")
	default:
		return blank + "│"
	}
}

// wrapCells 按显示宽度把 cell 序列切成多块，每块不超过 width 列
func wrapCells(cells []cell, width int) [][]cell {
	width = max(1, width)
	var chunks [][]cell
	var current []cell
	used := 0
	for _, item := range cells {
		runes := []rune(item.Text)
		for start := 0; start < len(runes); {
			space := width - used
			if space <= 0 {
				chunks = append(chunks, current)
				current, used = nil, 0
				continue
			}
			// 1.取尽可能多的 rune，凑满这一行
			end, taken := start, 0
			for end < len(runes) {
				w := ansi.StringWidth(string(runes[end]))
				if taken+w > space {
					break
				}
				taken += w
				end++
			}
			if end == start {
				// 单个字符就超宽：单独占一行，避免死循环
				end, taken = start+1, ansi.StringWidth(string(runes[start]))
			}
			current = append(current, cell{Text: string(runes[start:end]), FG: item.FG, Word: item.Word})
			used += taken
			start = end
			if used >= width {
				chunks = append(chunks, current)
				current, used = nil, 0
			}
		}
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	if len(chunks) == 0 {
		chunks = append(chunks, nil)
	}
	return chunks
}

// renderRowCode 画带行号的文件内容（read 的正文、write 的预览）
func renderRowCode(contents []string, path string, startLine int, indent string, footer string) []string {
	if len(contents) == 0 {
		return nil
	}
	// tab 的显示宽度是 0 但终端会展开，先换成空格，避免整行被终端折行
	expanded := make([]string, len(contents))
	for index, content := range contents {
		expanded[index] = expandTabs(content)
	}
	contents = expanded
	numberWidth := len(strconv.Itoa(startLine + len(contents) - 1))
	highlighted := highlightRowLines(contents, path)
	output := make([]string, 0, len(contents)+1)
	for index, content := range contents {
		number := strconv.Itoa(startLine + index)
		number = strings.Repeat(" ", numberWidth-len(number)) + number
		segments := highlightedCells(highlighted, index, content)
		output = append(output, indent+colored(dimColor, " "+number+" ")+paintCells(cellsFromLine(segments, nil), "", "", 0))
	}
	if footer != "" {
		output = append(output, indent+colored(dimColor, footer))
	}
	return output
}

// diff 底色的四个层级：变更行的整行底色，以及其中被改动词的更亮底色。
// 取参考 diff 的实测色值，而不是从终端底色混出——混色无法同时满足
// “红通道抬高、绿蓝压到近零”这种低明度饱和色
var (
	removedTint     = "#3d0101"
	addedTint       = "#022800"
	removedWordTint = "#5c0100"
	addedWordTint   = "#044700"
)
