package tui

import "fmt"

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
