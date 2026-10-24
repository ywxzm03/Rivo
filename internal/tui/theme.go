package tui

import "github.com/charmbracelet/lipgloss"

// Claude Code dark 主题的语义色 token；固定 RGB 并沿用终端颜色降级
const (
	textColor           = "#ffffff" // text / userMessageText
	inputBorder         = "#888888" // editor border and prompt
	mutedColor          = "#999999" // muted / mdQuote / mdLinkUrl
	accentColor         = "#b1b9f9" // accent / mdCode / mdListBullet
	borderColor         = "#888888" // border
	dimColor            = "#999999" // dim
	faintColor          = "#777777" // faint / abort hint
	thinkingColor       = "#999999" // thinkingText
	userBg              = "#373737" // user message frame
	userBgHover         = "#464646" // hovered jump pill
	greenColor          = "#4eba65" // success / inserted
	redColor            = "#ff6b80" // error / deleted
	blueColor           = "#b1b9f9" // mdLink
	variableColor       = "#ffffff" // syntaxVariable / syntaxProperty
	stringColor         = "#e6db74" // syntaxString / syntaxRegexp
	codeTextColor       = "#f8f8f2" // syntaxText / syntaxPunctuation
	syntaxKeywordColor  = "#f92672" // syntaxKeyword / syntaxOperator
	syntaxStorageColor  = "#66d9ef" // syntaxStorage
	syntaxTypeColor     = "#a6e22e" // syntaxType / syntaxFunction
	syntaxLiteralColor  = "#be84ff" // syntaxLiteral / syntaxNumber
	syntaxCommentColor  = "#75715e" // syntaxComment / syntaxMeta
	selectionBackground = "#264f78" // text selection
	// backgroundFill 是终端底色，diff 的红绿底从它混出，避免整块高饱和
	backgroundFill = "#1e1e1e"
)

func colored(color, value string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(value)
}

func boldText(value string) string {
	return lipgloss.NewStyle().Bold(true).Render(value)
}

// boldItalicText 是思考行的样式：白色粗斜体
func boldItalicText(value string) string {
	return lipgloss.NewStyle().Bold(true).Italic(true).Foreground(lipgloss.Color(textColor)).Render(value)
}
