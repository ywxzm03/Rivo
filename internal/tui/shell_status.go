package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	// shellCommandMaxWidth 是面板里命令行保留的最大字符数,超出按 codex 的做法截断
	shellCommandMaxWidth = 80
	// shellOutputMaxLines 是每条 shell 保留的输出行数,与 codex 的 MAX_RECENT_CHUNKS 一致
	shellOutputMaxLines = 3
	// shellMaxProcesses 是面板最多列出的 shell 数量,超出给出剩余提示
	shellMaxProcesses = 16
)

// backgroundShellStatusLine 是输入框上方固定的后台 shell 提示行,无运行中 shell 时返回空
func backgroundShellStatusLine(running int) string {
	if running <= 0 {
		return ""
	}
	noun := "shell"
	if running > 1 {
		noun = "shells"
	}
	return fmt.Sprintf("%d %s still running · /ps to view · /stop to close", running, noun)
}

// renderShellsPanel 渲染 /ps 面板。
// 标题顶格无圆点,与上方的 Worked for 行左对齐;
// 命令行用 accent 色,输出行缩进四格并以 ↳ 起首
func renderShellsPanel(message Message, width int) string {
	limit := max(1, width)
	lines := []string{"Background shells", ""}
	if message.ShellError != "" {
		lines = append(lines, colored(mutedColor, "  • "+message.ShellError))
		return strings.Join(lines, "\n")
	}
	if len(message.Shells) == 0 {
		lines = append(lines, colored(mutedColor, "  • No background shells running."))
		return strings.Join(lines, "\n")
	}
	shown := 0
	for _, shell := range message.Shells {
		if shown >= shellMaxProcesses {
			break
		}
		lines = append(lines, renderShellCommand(shell.Command, limit))
		lines = append(lines, renderShellOutput(shell, limit)...)
		shown++
	}
	if remaining := len(message.Shells) - shown; remaining > 0 {
		lines = append(lines, colored(mutedColor, ansi.Truncate(fmt.Sprintf("  • ... and %d more running", remaining), limit, "…")))
	}
	return strings.Join(lines, "\n")
}

// renderShellCommand 渲染单条命令:圆点用 muted,命令用 accent,超长补省略标记
func renderShellCommand(command string, limit int) string {
	const prefix = "  • "
	budget := max(1, limit-ansi.StringWidth(prefix))
	// 换行在单行里用 ↵ 表示,避免一条命令撑出多行
	flat := strings.NewReplacer("\r\n", "↵", "\n", "↵", "\r", "↵").Replace(command)
	truncated := false
	if len([]rune(flat)) > shellCommandMaxWidth {
		flat = string([]rune(flat)[:shellCommandMaxWidth])
		truncated = true
	}
	if ansi.StringWidth(flat) > budget {
		flat = ansi.Truncate(flat, budget, "")
		truncated = true
	}
	body := colored(accentColor, flat)
	if truncated {
		body += colored(mutedColor, " [...]")
	}
	return colored(mutedColor, prefix) + body
}

// shellStopLine 是 /stop 的受理文案
func shellStopLine(stopped int, err error) string {
	if err != nil {
		return "Stopping all background shells: " + err.Error()
	}
	if stopped == 0 {
		return "No background shells to stop."
	}
	return "Stopping all background shells."
}

// appendShellPanel 把 /ps 结果追加成一条独立的面板消息
func (m *Model) appendShellPanel(msg ShellsMsg) {
	errText := ""
	if msg.Err != nil {
		errText = msg.Err.Error()
	}
	m.shellPanelSeq++
	m.upsertMessage(Message{
		ID:         fmt.Sprintf("shells/%d", m.shellPanelSeq),
		Role:       "shells",
		ShellPanel: true,
		Shells:     msg.Shells,
		ShellError: errText,
	})
	m.refreshTranscript()
}

// echoSlashCommand 追加一条顶格的命令回显,与 /status 的呈现一致
func (m *Model) echoSlashCommand(command string) {
	m.shellPanelSeq++
	m.upsertMessage(Message{
		ID:   fmt.Sprintf("shell-command/%d", m.shellPanelSeq),
		Role: "command",
		Text: command,
	})
	m.refreshTranscript()
}

// renderShellOutput 渲染输出行:只保留末尾若干行,
// 首行用 ↳ 起首,后续行用等宽空格对齐正文起点
func renderShellOutput(shell ShellView, limit int) []string {
	tail := shell.OutputTail
	if len(tail) > shellOutputMaxLines {
		tail = tail[len(tail)-shellOutputMaxLines:]
	}
	// ↳ 在终端里占两列,后续行用 6 个空格把正文对齐到同一列
	const firstPrefix = "    ↳ "
	const nextPrefix = "      "
	rows := make([]string, 0, len(tail)+1)
	for index, output := range tail {
		prefix := nextPrefix
		if index == 0 {
			prefix = firstPrefix
		}
		budget := max(1, limit-ansi.StringWidth(prefix))
		body := ansi.Truncate(output, budget, "…")
		rows = append(rows, colored(mutedColor, prefix+body))
	}
	// 截断提示属于输出块的一部分,不再重复箭头,与正文列对齐
	if shell.Truncated {
		rows = append(rows, colored(mutedColor, nextPrefix+"output truncated"))
	}
	return rows
}
