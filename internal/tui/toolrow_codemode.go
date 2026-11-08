package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// renderCodemodeResult 保持父状态、脚本正文和嵌套调用的显示边界
func renderCodemodeResult(call ToolCallView, width int, expanded bool) []string {
	if call.Status == ToolPending {
		if call.StartedAt.IsZero() {
			return nil
		}
		return codemodeBlock(colored(mutedColor, "Running…"), rowResultPrefix, width, true)
	}
	view := call.Codemode
	failed := call.Status == ToolError || call.Status == ToolInterrupted
	var lines []string
	prefix := rowResultPrefix
	appendBlock := func(text string, words bool) {
		lines = append(lines, codemodeBlock(text, prefix, width, words)...)
		prefix = rowIndent
	}
	body := call.Result
	color := textColor
	if failed {
		color = redColor
		errorText := ""
		if view != nil {
			errorText = view.Error
		} else {
			errorText, body = body, ""
		}
		headline, rest := codemodeErrorParts(errorText)
		if headline == "" {
			headline = "Error"
			if call.Status == ToolInterrupted {
				headline = "Error: Operation aborted"
			}
		}
		appendBlock(colored(redColor, headline), false)
		if rest != "" {
			if body != "" {
				body += "\n"
			}
			body += rest
		}
	}
	if expanded && !failed {
		if view != nil && view.Summary.Complete {
			appendBlock(colored(mutedColor, countNoun(view.Output.LineCount, "line", "lines")), true)
		}
		if view != nil && len(view.Calls) > 0 {
			appendBlock(boldText("Calls"), false)
			for _, nested := range view.Calls {
				appendBlock("  "+codemodeCallLine(nested), false)
			}
		}
		appendBlock(boldText("Output"), false)
	}
	empty := body == ""
	if view != nil {
		empty = view.Output.LineCount == 0 && strings.TrimSpace(body) == ""
	}
	if !failed && empty {
		body = "(No content)"
	}
	available := max(1, width-ansi.StringWidth(rowIndent))
	bodyWidth := available
	if !expanded {
		bodyWidth = min(available, max(width-10, 10))
	}
	rows := codemodeBodyRows(body, color, bodyWidth)
	hidden := 0
	if !expanded && len(rows) > rowOutputRows+1 {
		hidden = len(rows) - rowOutputRows
		rows = rows[:rowOutputRows]
	}
	for _, row := range rows {
		lines = append(lines, codemodeFitRows([]string{colored(dimColor, prefix) + row}, width)...)
		prefix = rowIndent
	}
	if view != nil && view.Output.FullOutputPath != "" {
		appendBlock(colored(mutedColor, view.Output.FullOutputPath), false)
	}
	appendBlock(codemodeFooter(call), true)
	if hidden > 0 {
		appendBlock(toolRowMoreLines(hidden), true)
	}
	return lines
}

// codemodeErrorParts 只提取错误首个非空行，其余内容保持顺序
func codemodeErrorParts(text string) (string, string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for {
		line, rest, found := strings.Cut(text, "\n")
		if strings.TrimSpace(line) != "" {
			return line, rest
		}
		if !found {
			return "", ""
		}
		text = rest
	}
}

func codemodeBodyRows(text, color string, width int) []string {
	if text == "" {
		return nil
	}
	text = strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	var rows []string
	for line := range strings.SplitSeq(text, "\n") {
		rows = append(rows, codemodeFitRows([]string{colored(color, line)}, width)...)
	}
	return rows
}

// codemodeFitRows 在前缀也放不下时继续硬换行，单列终端跳过无法容纳的宽字形
func codemodeFitRows(rows []string, width int) []string {
	width = max(1, width)
	var fitted []string
	for _, row := range rows {
		for _, part := range hardWrap(row, width) {
			if ansi.StringWidth(part) > width {
				part = ansi.Truncate(part, width, "")
			}
			fitted = append(fitted, part)
		}
	}
	return fitted
}

func codemodeBlock(text, prefix string, width int, words bool) []string {
	available := max(1, width-ansi.StringWidth(prefix))
	var rows []string
	if words {
		rows = codemodeWordRows(text, available)
	} else {
		rows = codemodeFitRows(strings.Split(text, "\n"), available)
	}
	for i := range rows {
		if i == 0 {
			rows[i] = colored(dimColor, prefix) + rows[i]
		} else {
			rows[i] = rowIndent + rows[i]
		}
	}
	return codemodeFitRows(rows, width)
}

// codemodeWordRows 按词折行，超长词完整硬换行，避免截断路径和提示
func codemodeWordRows(text string, width int) []string {
	var rows []string
	for paragraph := range strings.SplitSeq(text, "\n") {
		line := ""
		for word := range strings.SplitSeq(paragraph, " ") {
			if line != "" && ansi.StringWidth(line)+1+ansi.StringWidth(word) <= width {
				line += " " + word
				continue
			}
			if line != "" {
				rows = append(rows, line)
			}
			parts := codemodeFitRows([]string{word}, width)
			rows = append(rows, parts[:len(parts)-1]...)
			line = parts[len(parts)-1]
		}
		rows = append(rows, line)
	}
	return rows
}

func codemodeDuration(ms int64) string {
	if ms < 60000 {
		return fmt.Sprintf("%d.%03ds", ms/1000, ms%1000)
	}
	seconds := ms / 1000
	if seconds >= 3600 {
		return fmt.Sprintf("%dh %dm %ds", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
}

func codemodeCallLine(call CodemodeCallView) string {
	icon, color := "…", warningColor
	switch call.Status {
	case CodemodeOK:
		icon, color = "✓", greenColor
	case CodemodeError:
		icon, color = "✗", redColor
	case CodemodeCancelled:
		icon, color = "⊘", mutedColor
	}
	line := colored(color, icon) + " " + call.Name
	if call.Args != "" {
		line += " " + call.Args
	}
	if call.DurationMS != nil {
		line += " " + colored(mutedColor, codemodeDuration(*call.DurationMS))
	}
	if call.Error != "" {
		line += " " + colored(redColor, call.Error)
	}
	return line
}

// codemodeFooter 只使用投影统计，不在渲染时重扫调用
func codemodeFooter(call ToolCallView) string {
	status := "Done"
	switch call.Status {
	case ToolError:
		status = "Failed"
	case ToolInterrupted:
		status = "Interrupted"
	}
	view := call.Codemode
	if view == nil {
		return colored(mutedColor, status)
	}
	if view.Output.DurationMS != nil {
		status += " in " + codemodeDuration(*view.Output.DurationMS)
	}
	if call.Status == ToolSuccess && (view.Summary.Complete || view.Output.DurationMS != nil) {
		status += " (" + countNoun(view.Output.LineCount, "line", "lines") + ")"
	}
	parts := []string{colored(mutedColor, status)}
	partial := !view.Summary.Complete
	for _, group := range view.Summary.Groups {
		counts := []string{boldText(group.Name)}
		if group.Success > 0 {
			counts = append(counts, colored(greenColor, strconv.Itoa(group.Success)))
		}
		if group.Failure > 0 {
			counts = append(counts, colored(redColor, strconv.Itoa(group.Failure)))
		}
		if group.Unfinished > 0 {
			partial = true
			unfinished := strconv.Itoa(group.Unfinished)
			if group.Success+group.Failure > 0 {
				unfinished += " unfinished"
			}
			counts = append(counts, colored(warningColor, unfinished))
		}
		parts = append(parts, strings.Join(counts, " "))
	}
	footer := strings.Join(parts, colored(mutedColor, " · "))
	if partial {
		footer += colored(mutedColor, " (partial)")
	}
	return footer
}
