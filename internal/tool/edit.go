package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"golang.org/x/text/unicode/norm"

	"github.com/ywxzm03/Rivo/internal/llm"
	toolargs "github.com/ywxzm03/Rivo/internal/tool/args"
)

type Edit struct {
	History    FileHistoryTracker
	WorkingDir string
}

type EditDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
}

func (EditDetails) ToolDetails() {}

func init() { llm.RegisterToolDetails[EditDetails]("patch") }

type textEdit struct {
	oldText string
	newText string
}

type matchedEdit struct {
	index   int
	start   int
	length  int
	newText string
}

func (e *Edit) Name() string { return "edit" }

func (e *Edit) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionParallel }

func (e *Edit) PromptSnippet() string {
	return "Make precise file edits with exact text replacement, including multiple disjoint edits in one call"
}

func (e *Edit) PromptGuidelines() []string {
	return []string{
		"Use edit for precise changes (edits[].oldText must match exactly)",
		"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
		"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
		"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
	}
}

func (e *Edit) Description() string {
	return "Edit a file using exact text replacement. Every oldText must match a unique, non-overlapping region of the original file. Multiple edits are matched against the original file and applied together."
}

func (e *Edit) Parameters() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"path", "edits"},
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Path to the file to edit (relative or absolute)"},
			"edits": map[string]any{
				"type":        "array",
				"description": "One or more targeted replacements matched against the original file. Do not include overlapping edits.",
				"items": map[string]any{"type": "object", "required": []string{"oldText", "newText"}, "properties": map[string]any{
					"oldText": map[string]any{"type": "string"},
					"newText": map[string]any{"type": "string"},
				}},
			},
		},
	}
}

// Execute 在共享文件变更队列中应用所有目标替换
func (e *Edit) Execute(ctx context.Context, args map[string]any) (llm.ToolResult, error) {
	// 1.统一输入形式并解析目标文件路径
	path, edits, err := parseEditArgs(args)
	if err != nil {
		return llm.ToolResult{}, err
	}
	absPath, err := resolveEditPathFrom(path, toolWorkingDir(ctx, e.WorkingDir))
	if err != nil {
		return llm.ToolResult{}, err
	}
	// 2.在同文件队列中读取并校验全部替换，确保失败时尚未写盘
	return withFileMutationQueue(ctx, absPath, func() (llm.ToolResult, error) {
		return e.executeEdit(ctx, path, absPath, edits)
	})
}

func (e *Edit) executeEdit(ctx context.Context, path, absPath string, edits []textEdit) (llm.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return llm.ToolResult{}, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return llm.ToolResult{}, fmt.Errorf("could not edit file %s: %w", path, err)
	}
	originalData := append([]byte(nil), data...)
	info, err := os.Lstat(absPath)
	if err != nil {
		return llm.ToolResult{}, fmt.Errorf("could not edit file %s: %w", path, err)
	}
	bom := []byte(nil)
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		bom, data = data[:3], data[3:]
	}
	content := normalizeLineEndings(string(data))
	ending := detectLineEnding(string(data))
	newContent, err := applyTextEdits(content, edits, path)
	if err != nil {
		return llm.ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return llm.ToolResult{}, err
	}
	// 3.记录已通过替换校验的文件，复用已读取内容避免备份竞态；
	// 备份失败不阻断写入，但要让用户知道 undo 历史不完整
	historyErr := trackFileEditContent(ctx, e.History, absPath, originalData, info.Mode().Perm())
	// 4.恢复 BOM 与原行尾后写回，再生成 diff 详情
	final := append(append([]byte(nil), bom...), []byte(restoreLineEndings(newContent, ending))...)
	file, err := os.OpenFile(absPath, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return llm.ToolResult{}, fmt.Errorf("could not edit file %s: %w", path, err)
	}
	_, writeErr := file.Write(final)
	closeErr := file.Close()
	if writeErr != nil {
		return llm.ToolResult{}, writeErr
	}
	if closeErr != nil {
		return llm.ToolResult{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		return llm.ToolResult{}, err
	}
	diffText, patch, firstLine := editDiff(content, newContent, path)
	message := fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(edits), path)
	if historyErr != nil {
		message += fmt.Sprintf(" (file history unavailable: %v)", historyErr)
	}
	return llm.ToolResult{
		Content: textContents(message),
		Details: EditDetails{Diff: diffText, Patch: patch, FirstChangedLine: firstLine},
	}, nil
}

// parseEditArgs 兼容多种输入形式，并统一行尾格式
// editParams 只承接 edit 的标量参数，edits 是开放形态需单独解析
type editParams struct {
	Path string `json:"path"`
}

func parseEditArgs(values map[string]any) (string, []textEdit, error) {
	path, err := toolargs.Decode[editParams](values)
	if err != nil {
		return "", nil, err
	}
	if path.Path == "" {
		return "", nil, fmt.Errorf("edit: missing required parameter: path")
	}
	raw := values["edits"]
	if encoded, ok := raw.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
			return "", nil, fmt.Errorf("edit: invalid edits JSON: %w", err)
		}
		raw = parsed
	}
	if object, ok := raw.(map[string]any); ok {
		raw = []any{object}
	}
	items, _ := raw.([]any)
	if len(items) == 0 {
		return "", nil, fmt.Errorf("edit: edits must contain at least one replacement")
	}
	edits := make([]textEdit, 0, len(items))
	for i, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("edit: edits[%d] must be an object", i)
		}
		oldText, oldOK := object["oldText"].(string)
		newText, newOK := object["newText"].(string)
		if !oldOK || !newOK {
			return "", nil, fmt.Errorf("edit: edits[%d] requires string oldText and newText", i)
		}
		edits = append(edits, textEdit{oldText: normalizeLineEndings(oldText), newText: normalizeLineEndings(newText)})
	}
	return path.Path, edits, nil
}

// resolveEditPath 处理路径前缀与空格变体

// normalizePathInput 归一化模型可能给出的路径变体：全角空格、@ 前缀、file:// URL
func normalizePathInput(path string) (string, error) {
	path = strings.Map(func(r rune) rune {
		switch r {
		case '\u00a0', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
			return ' '
		default:
			return r
		}
	}, path)
	path = strings.TrimPrefix(path, "@")
	if !strings.HasPrefix(path, "file://") {
		return path, nil
	}
	localPath := strings.TrimPrefix(path, "file://")
	if runtime.GOOS == "windows" && len(localPath) >= 2 && isWindowsDrivePath(localPath) {
		return localPath, nil
	}
	u, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", fmt.Errorf("edit: file URL must refer to the local machine")
	}
	return u.Path, nil
}

// resolveEditPathFrom 先归一化模型输入，再走通用的路径解析
func resolveEditPathFrom(path, cwd string) (string, error) {
	normalized, err := normalizePathInput(path)
	if err != nil {
		return "", err
	}
	return resolvePathFrom(normalized, cwd)
}

// isWindowsDrivePath 判定 path 是否以盘符开头；不足两个字符时直接判否，
// 避免越界
func isWindowsDrivePath(path string) bool {
	if len(path) < 2 {
		return false
	}
	letter := path[0]
	return ((letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')) && path[1] == ':'
}

func normalizeLineEndings(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func detectLineEnding(content string) string {
	crlf := strings.Index(content, "\r\n")
	lf := strings.Index(content, "\n")
	if lf >= 0 && crlf >= 0 && crlf < lf {
		return "\r\n"
	}
	return "\n"
}

func restoreLineEndings(content, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

// fuzzyNormalize 统一 Unicode 标点、空格及每行尾部空白以供回退匹配
func fuzzyNormalize(text string) string {
	text = norm.NFKC.String(text)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	text = strings.Join(lines, "\n")
	replacer := strings.NewReplacer(
		"‘", "'", "’", "'", "‚", "'", "‛", "'",
		"“", `"`, "”", `"`, "„", `"`, "‟", `"`,
		"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "―", "-", "−", "-",
		"\u00a0", " ", "\u2002", " ", "\u2003", " ", "\u2004", " ", "\u2005", " ",
		"\u2006", " ", "\u2007", " ", "\u2008", " ", "\u2009", " ", "\u200a", " ", "\u202f", " ", "\u205f", " ", "\u3000", " ",
	)
	return replacer.Replace(text)
}

func applyTextEdits(content string, edits []textEdit, path string) (string, error) {
	// 1.只要有一处需要模糊匹配，所有匹配都使用同一份规范化文本
	for i, edit := range edits {
		if edit.oldText == "" {
			return "", fmt.Errorf("edits[%d].oldText must not be empty in %s", i, path)
		}
	}
	matches := make([]matchedEdit, 0, len(edits))
	usedFuzzy := false
	for _, edit := range edits {
		if !strings.Contains(content, edit.oldText) {
			usedFuzzy = true
		}
	}
	base := content
	if usedFuzzy {
		base = fuzzyNormalize(content)
	}
	// 2.先在原文件内容上验证每处唯一性，再统一检查重叠区间
	for i, edit := range edits {
		oldText := edit.oldText
		if usedFuzzy {
			oldText = fuzzyNormalize(oldText)
		}
		start := strings.Index(base, oldText)
		if start < 0 {
			return "", fmt.Errorf("could not find edits[%d] in %s: oldText must match exactly including all whitespace and newlines", i, path)
		}
		count := strings.Count(fuzzyNormalize(content), fuzzyNormalize(edit.oldText))
		if count > 1 {
			return "", fmt.Errorf("found %d occurrences of edits[%d] in %s: each oldText must be unique, provide more context to make it unique", count, i, path)
		}
		matches = append(matches, matchedEdit{index: i, start: start, length: len(oldText), newText: edit.newText})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	for i := 1; i < len(matches); i++ {
		previous, current := matches[i-1], matches[i]
		if previous.start+previous.length > current.start {
			return "", fmt.Errorf("edits[%d] and edits[%d] overlap in %s: merge them into one edit or target disjoint regions", previous.index, current.index, path)
		}
	}
	newBase := applyReplacements(base, matches)
	if usedFuzzy {
		// 模糊匹配只重写命中的行块，避免把其他行的原始字符也规范化
		newBase = preserveUnchangedLines(content, base, matches)
	}
	if newBase == content {
		return "", fmt.Errorf("no changes made to %s: replacements produced identical content", path)
	}
	return newBase, nil
}

func applyReplacements(content string, edits []matchedEdit) string {
	for i := len(edits) - 1; i >= 0; i-- {
		edit := edits[i]
		content = content[:edit.start] + edit.newText + content[edit.start+edit.length:]
	}
	return content
}

type lineSpan struct{ start, end int }

func splitLineSpans(content string) []lineSpan {
	spans := make([]lineSpan, 0)
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			spans = append(spans, lineSpan{start: start, end: i + 1})
			start = i + 1
		}
	}
	if start < len(content) {
		spans = append(spans, lineSpan{start: start, end: len(content)})
	}
	return spans
}

// preserveUnchangedLines 将规范化内容中的替换映射回原文，只改动命中行块
func preserveUnchangedLines(original, base string, edits []matchedEdit) string {
	originalLines := splitLineSpans(original)
	baseLines := splitLineSpans(base)
	groups := make([][3]int, 0, len(edits))
	for _, edit := range edits {
		startLine, endLine := -1, -1
		for i, line := range baseLines {
			if edit.start >= line.start && edit.start < line.end {
				startLine = i
			}
			if edit.start+edit.length > line.start && edit.start+edit.length <= line.end {
				endLine = i + 1
			}
		}
		if endLine < 0 {
			endLine = len(baseLines)
		}
		if len(groups) > 0 && startLine < groups[len(groups)-1][1] {
			last := len(groups) - 1
			groups[last][1] = max(groups[last][1], endLine)
			groups[last][2] = groups[last][2] + 1
		} else {
			groups = append(groups, [3]int{startLine, endLine, 1})
		}
	}
	var result strings.Builder
	originalLine := 0
	for _, group := range groups {
		for ; originalLine < group[0] && originalLine < len(originalLines); originalLine++ {
			result.WriteString(original[originalLines[originalLine].start:originalLines[originalLine].end])
		}
		start, end := baseLines[group[0]].start, baseLines[group[1]-1].end
		groupEdits := make([]matchedEdit, 0, group[2])
		for _, edit := range edits {
			if edit.start >= start && edit.start < end {
				edit.start -= start
				groupEdits = append(groupEdits, edit)
			}
		}
		result.WriteString(applyReplacements(base[start:end], groupEdits))
		originalLine = group[1]
	}
	for ; originalLine < len(originalLines); originalLine++ {
		result.WriteString(original[originalLines[originalLine].start:originalLines[originalLine].end])
	}
	return result.String()
}

func editDiff(before, after, path string) (string, string, *int) {
	edits := gotextdiff.LineEdits(before, myers.ComputeEdits("", before, after))
	unified := gotextdiff.ToUnified(path, path, before, edits)
	unified = extendUnifiedContext(unified, before)
	patch := fmt.Sprintf("%v", unified)
	var display strings.Builder
	lineWidth := len(fmt.Sprint(max(strings.Count(before, "\n"), strings.Count(after, "\n")) + 1))
	var firstChanged *int
	previousEnd := 0
	for _, hunk := range unified.Hunks {
		if previousEnd > 0 && hunk.FromLine-previousEnd > 6 {
			fmt.Fprintf(&display, " %*s ...\n", lineWidth, "")
		}
		oldLine, newLine := hunk.FromLine, hunk.ToLine
		for _, line := range hunk.Lines {
			text := strings.TrimSuffix(line.Content, "\n")
			switch line.Kind {
			case gotextdiff.Delete:
				fmt.Fprintf(&display, "-%*d %s\n", lineWidth, oldLine, text)
				oldLine++
				if firstChanged == nil {
					v := newLine
					firstChanged = &v
				}
			case gotextdiff.Insert:
				fmt.Fprintf(&display, "+%*d %s\n", lineWidth, newLine, text)
				if firstChanged == nil {
					v := newLine
					firstChanged = &v
				}
				newLine++
			default:
				fmt.Fprintf(&display, " %*d %s\n", lineWidth, oldLine, text)
				oldLine++
				newLine++
			}
		}
		previousEnd = oldLine
	}
	return strings.TrimSuffix(display.String(), "\n"), patch, firstChanged
}

// extendUnifiedContext 将 gotextdiff 的三行上下文补到四行
func extendUnifiedContext(unified gotextdiff.Unified, content string) gotextdiff.Unified {
	lines := make([]string, 0)
	for _, span := range splitLineSpans(content) {
		lines = append(lines, content[span.start:span.end])
	}
	originalStarts := make([]int, len(unified.Hunks))
	originalEnds := make([]int, len(unified.Hunks))
	for i, hunk := range unified.Hunks {
		originalStarts[i] = hunk.FromLine - 1
		originalEnds[i] = originalStarts[i] + hunkOldLineCount(hunk)
	}
	for i, hunk := range unified.Hunks {
		start := originalStarts[i] - 1
		if start >= 0 && (i == 0 || start >= originalEnds[i-1]) {
			line := gotextdiff.Line{Kind: gotextdiff.Equal, Content: lines[start]}
			hunk.Lines = append([]gotextdiff.Line{line}, hunk.Lines...)
			hunk.FromLine--
			hunk.ToLine--
		}
		end := hunk.FromLine - 1 + hunkOldLineCount(hunk)
		if end < len(lines) && (i == len(unified.Hunks)-1 || originalStarts[i+1] > end+1) {
			hunk.Lines = append(hunk.Lines, gotextdiff.Line{Kind: gotextdiff.Equal, Content: lines[end]})
		}
	}
	return unified
}

func hunkOldLineCount(hunk *gotextdiff.Hunk) int {
	count := 0
	for _, line := range hunk.Lines {
		if line.Kind != gotextdiff.Insert {
			count++
		}
	}
	return count
}
