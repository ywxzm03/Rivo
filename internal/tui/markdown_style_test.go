package tui

import (
	"testing"

	style "github.com/charmbracelet/glamour/ansi"
)

func styleFlag(value *bool) bool {
	return value != nil && *value
}

func TestMarkdownStyleMatchesClaudeHeadingHierarchy(t *testing.T) {
	config := markdownStyle("assistant")

	if config.H1.Color == nil || *config.H1.Color != textColor {
		t.Fatalf("h1 color = %v, want %s", config.H1.Color, textColor)
	}
	if !styleFlag(config.H1.Bold) || !styleFlag(config.H1.Italic) || !styleFlag(config.H1.Underline) {
		t.Fatalf("h1 style = %+v, want bold italic underline", config.H1.StylePrimitive)
	}
	for name, heading := range map[string]style.StyleBlock{"h2": config.H2, "h3": config.H3} {
		if !styleFlag(heading.Bold) || styleFlag(heading.Italic) || styleFlag(heading.Underline) {
			t.Fatalf("%s style = %+v, want bold only", name, heading.StylePrimitive)
		}
	}
}

func TestMarkdownStyleUsesClaudeMonokaiTokens(t *testing.T) {
	chroma := markdownStyle("assistant").CodeBlock.Chroma
	if chroma == nil {
		t.Fatal("code block chroma style is nil")
	}
	want := map[string]*string{
		"keyword":  chroma.Keyword.Color,
		"function": chroma.NameFunction.Color,
		"literal":  chroma.LiteralNumber.Color,
		"string":   chroma.LiteralString.Color,
		"comment":  chroma.Comment.Color,
	}
	wantColors := map[string]string{
		"keyword":  syntaxKeywordColor,
		"function": syntaxTypeColor,
		"literal":  syntaxLiteralColor,
		"string":   stringColor,
		"comment":  syntaxCommentColor,
	}
	for name, color := range want {
		if color == nil || *color != wantColors[name] {
			t.Fatalf("%s color = %v, want %s", name, color, wantColors[name])
		}
	}
}
