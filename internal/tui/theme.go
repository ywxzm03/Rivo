package tui

import "github.com/charmbracelet/lipgloss"

// Pi dark.json 的 okhsl 经 Pi colors 转换为 hex；仅保留本阶段使用的 token
const (
	textColor     = "#dee0e1" // text / userMessageText
	inputBorder   = "#dee0e1" // editor border and prompt
	mutedColor    = "#9da5a9" // muted / mdQuote / mdLinkUrl
	accentColor   = "#a798d7" // accent / mdCode / mdListBullet
	borderColor   = "#5fa8cc" // border
	dimColor      = "#7e888e" // dim
	thinkingColor = "#96a0a4" // thinkingText
	userBg        = "#3a3d42" // user message frame
	greenColor    = "#68b78d" // success / mdCodeBlock / syntaxNumber
	redColor      = "#ea7f81" // error
	yellowColor   = "#cd9a22" // warning / mdHeading / syntaxFunction
	blueColor     = "#69add0" // mdLink / syntaxKeyword
	variableColor = "#5db3ba" // syntaxVariable
	stringColor   = "#de8d5a" // syntaxString
)

func colored(color, value string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(value)
}
