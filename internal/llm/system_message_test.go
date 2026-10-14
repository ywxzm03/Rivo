package llm

import "testing"

func TestCurrentSystemMessageReplaysSections(t *testing.T) {
	messages := []Message{
		UserMessage{Content: []Content{TextContent{Text: "hi"}}},
		SystemMessage{Sections: map[string]string{"preamble": "base", "cwd": "/a"}},
		SystemMessage{Sections: map[string]string{"cwd": "/b"}},
	}
	got := CurrentSystemMessage(messages)
	if got == nil {
		t.Fatal("CurrentSystemMessage returned nil, want merged sections")
	}
	if got.Sections["preamble"] != "base" {
		t.Fatalf("preamble = %q, want %q", got.Sections["preamble"], "base")
	}
	// 后者覆盖前者
	if got.Sections["cwd"] != "/b" {
		t.Fatalf("cwd = %q, want %q", got.Sections["cwd"], "/b")
	}
}

func TestCurrentSystemMessageDeletesEmptySection(t *testing.T) {
	messages := []Message{
		SystemMessage{Sections: map[string]string{"preamble": "base", "instructions": "<instructions>x</instructions>"}},
		SystemMessage{Sections: map[string]string{"instructions": ""}},
	}
	got := CurrentSystemMessage(messages)
	if got == nil {
		t.Fatal("CurrentSystemMessage returned nil, want sections")
	}
	if _, ok := got.Sections["instructions"]; ok {
		t.Fatalf("instructions still present, want deleted; sections = %v", got.Sections)
	}
	if got.Sections["preamble"] != "base" {
		t.Fatalf("preamble = %q, want %q", got.Sections["preamble"], "base")
	}
}

func TestCurrentSystemMessageWithoutSystemMessages(t *testing.T) {
	messages := []Message{UserMessage{Content: []Content{TextContent{Text: "hi"}}}}
	if got := CurrentSystemMessage(messages); got != nil {
		t.Fatalf("CurrentSystemMessage = %v, want nil", got)
	}
}

func TestRenderSystemMessageTextFollowsSectionOrder(t *testing.T) {
	msg := SystemMessage{Sections: map[string]string{
		"cwd":      "<cwd>/a</cwd>",
		"preamble": "base",
		"rules":    "<rules>\n- r\n</rules>",
	}}
	got := RenderSystemMessageText(msg)
	want := "base\n\n<rules>\n- r\n</rules>\n\n<cwd>/a</cwd>"
	if got != want {
		t.Fatalf("RenderSystemMessageText =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderSystemMessageTextSkipsEmptyAndUnknownSections(t *testing.T) {
	msg := SystemMessage{Sections: map[string]string{
		"preamble": "base",
		"tools":    "",
		"unknown":  "ignored",
	}}
	if got := RenderSystemMessageText(msg); got != "base" {
		t.Fatalf("RenderSystemMessageText = %q, want %q", got, "base")
	}
}

func TestRenderSystemMessageUpdateFramesSections(t *testing.T) {
	msg := SystemMessage{Sections: map[string]string{"instructions": "<instructions path=\"/a\">new</instructions>"}}
	got := RenderSystemMessageUpdate(msg)
	want := "Updated system prompt section \"instructions\":\n\n<instructions path=\"/a\">new</instructions>"
	if got != want {
		t.Fatalf("RenderSystemMessageUpdate =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderSystemMessageUpdateRendersRemoval(t *testing.T) {
	msg := SystemMessage{Sections: map[string]string{"skills": ""}}
	got := RenderSystemMessageUpdate(msg)
	want := "Removed system prompt section \"skills\"."
	if got != want {
		t.Fatalf("RenderSystemMessageUpdate = %q, want %q", got, want)
	}
}

func TestRenderSystemMessageUpdateFollowsSectionOrder(t *testing.T) {
	msg := SystemMessage{Sections: map[string]string{
		"cwd":      "<cwd>/b</cwd>",
		"preamble": "base",
	}}
	got := RenderSystemMessageUpdate(msg)
	want := "Updated system prompt section \"preamble\":\n\nbase\n\nUpdated system prompt section \"cwd\":\n\n<cwd>/b</cwd>"
	if got != want {
		t.Fatalf("RenderSystemMessageUpdate =\n%q\nwant\n%q", got, want)
	}
}
