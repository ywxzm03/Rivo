package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// plainLines 去掉 ANSI 并裁掉行尾填充空格，便于按插件 golden 的形状断言
func plainLines(view string) []string {
	lines := strings.Split(ansi.Strip(view), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

func rowCall(id, name string, args map[string]any, status ToolStatus) ToolCallView {
	data, _ := json.Marshal(args)
	return ToolCallView{ID: id, Name: name, Arguments: string(data), Status: status, ArgsComplete: true}
}

func rowDetails(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func rowLines(call ToolCallView, expanded bool) []string {
	return plainLines(strings.Join(renderToolRow(call, 100, expanded, time.UnixMilli(0)), "\n"))
}

func groupLines(group *ToolGroupView, state toolRenderState) []string {
	view, _ := renderToolRows(group, 100, state, time.UnixMilli(0))
	return plainLines(view)
}

// 调用行是 `⦁ Label(argument)`，标签加粗，参数进括号
func TestToolRowCallTitles(t *testing.T) {
	tests := []struct {
		name string
		call ToolCallView
		want string
	}{
		{"bash", rowCall("1", "bash", map[string]any{"command": "npm test"}, ToolSuccess), " ⦁ Bash(npm test)"},
		{"read", rowCall("1", "read", map[string]any{"path": "src/app.ts"}, ToolSuccess), " ⦁ Read(src/app.ts)"},
		{"edit", rowCall("1", "edit", map[string]any{"path": "app.ts"}, ToolSuccess), " ⦁ Update(app.ts)"},
		{"write", rowCall("1", "write", map[string]any{"path": "notes.md"}, ToolSuccess), " ⦁ Write(notes.md)"},
		{"ls", rowCall("1", "ls", map[string]any{"path": "src"}, ToolSuccess), " ⦁ List(src)"},
		{"ls default", rowCall("1", "ls", map[string]any{}, ToolSuccess), " ⦁ List(.)"},
		{"grep", rowCall("1", "grep", map[string]any{"pattern": "TODO", "path": "src"}, ToolSuccess), ` ⦁ Search(pattern: "TODO", path: "src")`},
		{"grep without path", rowCall("1", "grep", map[string]any{"pattern": "TODO"}, ToolSuccess), ` ⦁ Search(pattern: "TODO")`},
		{"find", rowCall("1", "find", map[string]any{"pattern": "*.ts", "path": "src"}, ToolSuccess), ` ⦁ Search(pattern: "*.ts", path: "src")`},
		{"powershell", rowCall("1", "powershell", map[string]any{"command": "Get-ChildItem"}, ToolSuccess), " ⦁ PowerShell(Get-ChildItem)"},
		{"custom", rowCall("1", "web_search", map[string]any{"query": "pi tui", "limit": 5}, ToolSuccess), ` ⦁ web_search(query: "pi tui", limit: 5)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := rowLines(tt.call, false)
			if len(lines) == 0 || lines[0] != tt.want {
				t.Fatalf("title = %q, want %q", lines, tt.want)
			}
		})
	}
}

// 收起的命令只在前两行或 160 字符处截断，标题在宽度内换行而不是被裁掉
func TestToolRowCommandCollapse(t *testing.T) {
	long := strings.Repeat("x", 200)
	call := rowCall("1", "bash", map[string]any{"command": long}, ToolSuccess)
	lines := rowLines(call, false)
	title := strings.Join(lines[:len(lines)-1], "")
	if !strings.HasPrefix(lines[0], " ⦁ Bash(") || !strings.HasSuffix(title, "…)") {
		t.Fatalf("long command title = %q", lines)
	}

	wide := plainLines(strings.Join(renderToolRow(call, 400, true, time.UnixMilli(0)), "\n"))
	if !strings.Contains(strings.Join(wide, ""), long) {
		t.Fatalf("expanded command was shortened:\n%s", strings.Join(wide, "\n"))
	}

	multi := rowCall("2", "bash", map[string]any{"command": "echo one\necho two\necho three"}, ToolSuccess)
	joined := strings.Join(rowLines(multi, false), "\n")
	if !strings.Contains(joined, "echo one") || !strings.Contains(joined, "echo two") || strings.Contains(joined, "echo three") {
		t.Fatalf("multi-line command = %q", joined)
	}
}

// 窄宽度下标题换行到工具名下方，而不是被截断
func TestToolRowTitleWrapsUnderLabel(t *testing.T) {
	call := rowCall("1", "bash", map[string]any{"command": strings.Repeat("ab ", 30)}, ToolSuccess)
	view, _ := renderToolRows(&ToolGroupView{ID: "g", Calls: []ToolCallView{call}}, 44, toolRenderState{}, time.UnixMilli(0))
	lines := plainLines(view)
	if len(lines) < 2 {
		t.Fatalf("narrow title did not wrap: %q", lines)
	}
	if !strings.HasPrefix(lines[0], " ⦁ Bash(ab") || strings.HasPrefix(lines[1], " ⦁") {
		t.Fatalf("narrow title = %q", lines)
	}
	if !strings.HasPrefix(lines[1], "   ab") {
		t.Fatalf("continuation is not under the label: %q", lines[1])
	}
}

// read 收起时只有结果句，展开后是带行号的代码与续读提示
func TestToolRowReadRows(t *testing.T) {
	call := rowCall("1", "read", map[string]any{"path": "src/app.ts"}, ToolSuccess)
	call.Result = "line 1\nline 2\nline 3\n\n[Showing lines 1-3 of 80. Use offset=4 to continue.]"

	collapsed := rowLines(call, false)
	want := []string{" ⦁ Read(src/app.ts)", "   ⎿  Read 3 lines"}
	if strings.Join(collapsed, "\n") != strings.Join(want, "\n") {
		t.Fatalf("collapsed read =\n%s", strings.Join(collapsed, "\n"))
	}

	joined := strings.Join(rowLines(call, true), "\n")
	for _, want := range []string{"   ⎿  Read 3 lines", "       1 line 1", "       3 line 3", "Showing lines 1-3 of 80"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded read missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "[Showing") {
		t.Fatalf("footer kept its brackets:\n%s", joined)
	}
}

// read 的行号从 offset 起算
func TestToolRowReadOffsetLineNumbers(t *testing.T) {
	call := rowCall("1", "read", map[string]any{"path": "src/app.ts", "offset": 40}, ToolSuccess)
	call.Result = "const a = 1;\nconst b = 2;"
	joined := strings.Join(rowLines(call, true), "\n")
	if !strings.Contains(joined, "       40 const a = 1;") {
		t.Fatalf("offset read rows:\n%s", joined)
	}
}

// 命令输出只预览三行，多出来的按行数提示
func TestToolRowBashOutputRows(t *testing.T) {
	call := rowCall("1", "bash", map[string]any{"command": "npm test"}, ToolSuccess)
	call.Result = "> test\n> node --test\nok 1\nok 2\nok 3"

	lines := rowLines(call, false)
	want := []string{
		" ⦁ Bash(npm test)",
		"   ⎿  > test",
		"      > node --test",
		"      ok 1",
		"      … +2 lines (ctrl+o to expand)",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("collapsed bash =\n%s", strings.Join(lines, "\n"))
	}

	expanded := strings.Join(rowLines(call, true), "\n")
	if !strings.Contains(expanded, "ok 3") || strings.Contains(expanded, "ctrl+o") {
		t.Fatalf("expanded bash =\n%s", expanded)
	}
}

// 只有一行多余时不折叠；空输出有固定文案
func TestToolRowBashOutputEdgeCases(t *testing.T) {
	call := rowCall("1", "bash", map[string]any{"command": "seq 1 4"}, ToolSuccess)
	call.Result = "1\n2\n3\n4"
	if joined := strings.Join(rowLines(call, false), "\n"); strings.Contains(joined, "ctrl+o") || !strings.Contains(joined, "4") {
		t.Fatalf("four-line output =\n%s", joined)
	}

	quiet := rowCall("2", "bash", map[string]any{"command": "true"}, ToolSuccess)
	quiet.Result = "(no output)"
	if joined := strings.Join(rowLines(quiet, false), "\n"); !strings.Contains(joined, "⎿  (No output)") {
		t.Fatalf("quiet bash =\n%s", joined)
	}
}

// edit 始终展示 diff：三行上下文、行号与符号、变更行有底色，断开处是 ⋮
func TestToolRowEditRows(t *testing.T) {
	patch := strings.Join([]string{
		"--- app.ts",
		"+++ app.ts",
		"@@ -1,6 +1,6 @@",
		" line 1",
		" line 2",
		" line 3",
		" line 4",
		"-line 5",
		"+changed 5",
		" line 6",
		" line 7",
		" line 8",
		"@@ -19,3 +19,4 @@",
		" line 19",
		"-line 20",
		"+changed 20",
		"+added 20b",
		" line 21",
	}, "\n")
	call := rowCall("1", "edit", map[string]any{"path": "app.ts"}, ToolSuccess)
	call.Result = "Successfully replaced 2 block(s) in app.ts."
	call.Details = rowDetails(t, map[string]any{"Patch": patch, "Diff": ""})

	lines := rowLines(call, false)
	if lines[0] != " ⦁ Update(app.ts)" {
		t.Fatalf("edit title = %q", lines[0])
	}
	if lines[1] != "   ⎿  Added 3 lines, removed 2 lines" {
		t.Fatalf("edit outcome = %q", lines[1])
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"  2 │", "  5 -", "  5 +", " 21 +"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("edit diff missing %q:\n%s", want, joined)
		}
	}
	// 变更行整行带背景，上下文行不带
	view := strings.Join(renderToolRow(call, 100, false, time.UnixMilli(0)), "\n")
	ansiLines := strings.Split(view, "\n")
	removed, context := ansiLines[5], ansiLines[4]
	if !strings.Contains(removed, "\x1b[48;2;") {
		t.Fatalf("removed row has no tint: %q", removed)
	}
	if strings.Contains(context, "\x1b[48;2;") {
		t.Fatalf("context row is tinted: %q", context)
	}
}

// write 收起时预览十行，展开后是全文件
func TestToolRowWriteRows(t *testing.T) {
	content := strings.Join(lineTexts(15), "\n")
	call := rowCall("1", "write", map[string]any{"path": "notes.md", "content": content}, ToolSuccess)
	call.Result = "Successfully wrote to notes.md"

	lines := rowLines(call, false)
	if lines[1] != "   ⎿  Wrote 15 lines to notes.md" {
		t.Fatalf("write outcome = %q", lines[1])
	}
	if !strings.Contains(strings.Join(lines, "\n"), "… +5 lines (ctrl+o to expand)") {
		t.Fatalf("write preview =\n%s", strings.Join(lines, "\n"))
	}

	joined := strings.Join(rowLines(call, true), "\n")
	if !strings.Contains(joined, "15 line 15") || strings.Contains(joined, "ctrl+o") {
		t.Fatalf("expanded write =\n%s", joined)
	}
}

// 失败行给出原因：命令用退出码打头并铺输出，其他工具只给一行原因
func TestToolRowFailureRows(t *testing.T) {
	bash := rowCall("1", "bash", map[string]any{"command": "npm test"}, ToolError)
	bash.Result = "npm ERR! missing script: test\nnpm ERR! A complete log is in ~/.npm/_logs\n\nCommand exited with code 1"
	lines := rowLines(bash, false)
	want := []string{
		" ⦁ Bash(npm test)",
		"   ⎿  Error: Exit code 1",
		"      npm ERR! missing script: test",
		"      npm ERR! A complete log is in ~/.npm/_logs",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("failed bash =\n%s", strings.Join(lines, "\n"))
	}

	read := rowCall("2", "read", map[string]any{"path": "src/app.ts"}, ToolError)
	read.Result = "tool error: open src/app.ts: no such file or directory"
	if lines := rowLines(read, false); lines[1] != "   ⎿  Error: open src/app.ts: no such file or directory" {
		t.Fatalf("failed read = %q", lines[1])
	}

	timedOut := rowCall("3", "bash", map[string]any{"command": "sleep 9"}, ToolError)
	timedOut.Result = "tool error: \n\nCommand timed out after 5 seconds"
	if lines := rowLines(timedOut, false); lines[1] != "   ⎿  Error: Command timed out after 5 seconds" {
		t.Fatalf("timed out bash = %q", lines[1])
	}
}

// 参数还没流完、工具也没开始执行时只有调用行，没有结果行；
// 执行开始后写 Running…；中断的调用说明被取消
func TestToolRowRunningAndInterruptedRows(t *testing.T) {
	// 参数还在流式写入：点呼吸，没有结果行
	streaming := rowCall("0", "bash", map[string]any{"command": "npm test"}, ToolPending)
	if lines := rowLines(streaming, false); len(lines) != 1 {
		t.Fatalf("streaming row = %q, want the call line only", lines)
	}
	// 参数写完、工具还没开始执行：点变暗，仍没有结果行
	queued := rowCall("0", "bash", map[string]any{"command": "npm test"}, ToolPending)
	queued.ArgsComplete = true
	if lines := rowLines(queued, false); len(lines) != 1 {
		t.Fatalf("queued row = %q, want the call line only", lines)
	}
	if view := strings.Join(renderToolRow(queued, 80, false, time.UnixMilli(0)), "\n"); !strings.Contains(view, colored(dimColor, "⦁")) {
		t.Fatalf("queued dot is not dim: %q", view)
	}

	// 执行开始后写 Running…
	running := rowCall("1", "bash", map[string]any{"command": "npm test"}, ToolPending)
	running.StartedAt = time.UnixMilli(1)
	if lines := rowLines(running, false); lines[1] != "   ⎿  Running…" {
		t.Fatalf("running row = %q", lines)
	}

	interrupted := rowCall("2", "read", map[string]any{"path": "a.go"}, ToolInterrupted)
	if lines := rowLines(interrupted, false); lines[1] != "   ⎿  Error: Operation aborted" {
		t.Fatalf("interrupted row = %q", lines[1])
	}
}

// 只读调用折叠成一行摘要，点击后展开成各自的调用行
func TestToolRowGroupSummary(t *testing.T) {
	calls := []ToolCallView{
		rowCall("1", "grep", map[string]any{"pattern": "greet", "path": "demo"}, ToolSuccess),
		rowCall("2", "read", map[string]any{"path": "demo/greet.ts"}, ToolSuccess),
		rowCall("3", "read", map[string]any{"path": "demo/config.json"}, ToolSuccess),
	}
	calls[0].Result = "demo/greet.ts:1: export function greet"
	calls[1].Result = "export function greet"
	calls[2].Result = "{}"
	group := &ToolGroupView{ID: "g", Calls: calls}

	lines := groupLines(group, toolRenderState{})
	want := " ⦁ Searched for 1 pattern, read 2 files (click to expand)"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("group summary = %q, want %q", lines, want)
	}

	joined := strings.Join(groupLines(group, toolRenderState{groups: map[string]bool{"g": true}}), "\n")
	for _, want := range []string{"Searched for 1 pattern, read 2 files", "⦁ Search(", "⦁ Read(demo/greet.ts)", "⦁ Read(demo/config.json)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded group missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "click to expand") {
		t.Fatalf("opened group still asks to expand:\n%s", joined)
	}
}

// 一个命令结束一组：它自己不参与折叠
func TestToolRowGroupBreaksOnCommand(t *testing.T) {
	calls := []ToolCallView{
		rowCall("1", "read", map[string]any{"path": "a.go"}, ToolSuccess),
		rowCall("2", "bash", map[string]any{"command": "go test ./..."}, ToolSuccess),
		rowCall("3", "read", map[string]any{"path": "b.go"}, ToolSuccess),
	}
	calls[0].Result = "a"
	calls[1].Result = "ok"
	calls[2].Result = "b"
	group := &ToolGroupView{ID: "g", Calls: calls}

	lines := groupLines(group, toolRenderState{})
	want := []string{
		" ⦁ Read 1 file (click to expand)",
		" ⦁ Bash(go test ./...)",
		"   ⎿  ok",
		" ⦁ Read 1 file (click to expand)",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("group split =\n%s", strings.Join(lines, "\n"))
	}
}

// 只读命令也进组；运行中的组用进行时动词并在下面点出正在看什么
func TestToolRowGroupRunningHint(t *testing.T) {
	read := rowCall("1", "read", map[string]any{"path": "src/a"}, ToolSuccess)
	read.Result = "a"
	search := rowCall("2", "bash", map[string]any{"command": "rg x src"}, ToolPending)
	search.StartedAt = time.UnixMilli(1)
	group := &ToolGroupView{ID: "g", Calls: []ToolCallView{read, search}}

	lines := groupLines(group, toolRenderState{})
	if !strings.HasPrefix(lines[0], " ⦁ Searching for 1 pattern, reading 1 file") {
		t.Fatalf("running group head = %q", lines[0])
	}
	if !strings.Contains(lines[0], "(click to expand)") {
		t.Fatalf("running group has no expand hint: %q", lines[0])
	}
	if len(lines) < 2 || lines[1] != `   ⎿  $ rg x src` {
		t.Fatalf("running group hint = %q", lines)
	}
}

// 收起的组仍把失败原因留在摘要下面
func TestToolRowGroupFailures(t *testing.T) {
	ok := rowCall("1", "read", map[string]any{"path": "src/a"}, ToolSuccess)
	ok.Result = "a"
	failed := rowCall("2", "grep", map[string]any{"pattern": "x", "path": "src"}, ToolError)
	failed.Result = "rg: src: No such file or directory"
	group := &ToolGroupView{ID: "g", Calls: []ToolCallView{ok, failed}}

	lines := groupLines(group, toolRenderState{})
	if len(lines) != 2 || !strings.Contains(lines[1], "Error: rg: src: No such file or directory") {
		t.Fatalf("group failure =\n%s", strings.Join(lines, "\n"))
	}
}

// 状态点：成功绿、失败红、运行中在灰度间呼吸
func TestToolRowStatusDot(t *testing.T) {
	success := rowCall("1", "read", map[string]any{"path": "a"}, ToolSuccess)
	if view := strings.Join(renderToolRow(success, 80, false, time.UnixMilli(0)), "\n"); !strings.Contains(view, colored(greenColor, "⦁")) {
		t.Fatalf("success dot is not green: %q", view)
	}

	failed := rowCall("1", "read", map[string]any{"path": "a"}, ToolError)
	if view := strings.Join(renderToolRow(failed, 80, false, time.UnixMilli(0)), "\n"); !strings.Contains(view, colored(redColor, "⦁")) {
		t.Fatalf("failure dot is not red: %q", view)
	}

	running := rowCall("1", "bash", map[string]any{"command": "x"}, ToolPending)
	running.StartedAt = time.UnixMilli(1)
	first := strings.Join(renderToolRow(running, 80, false, time.UnixMilli(0)), "\n")
	second := strings.Join(renderToolRow(running, 80, false, time.UnixMilli(7*45)), "\n")
	if first == second {
		t.Fatal("running dot does not pulse")
	}
}

// 思考默认只有一行 Thinking...，展开后是完整正文
func TestToolRowThinking(t *testing.T) {
	text := strings.Repeat("reasoning line\n", 40)
	group := &ToolGroupView{ID: "g", Thinking: []ThinkingBlockView{{Index: 0, Text: text}}}

	lines := groupLines(group, toolRenderState{})
	if len(lines) != 1 || lines[0] != " Thinking..." {
		t.Fatalf("collapsed thinking = %q", lines)
	}
	view, hits := renderToolRows(group, 100, toolRenderState{}, time.UnixMilli(0))
	if !strings.Contains(view, "\x1b[1;3") && !strings.Contains(view, "\x1b[3;1") {
		t.Fatalf("thinking line is not bold italic: %q", view)
	}
	if len(hits) != 1 || hits[0].Kind != "thinking" || hits[0].Key != "g/r0" {
		t.Fatalf("thinking hit = %+v", hits)
	}

	expanded := strings.Join(groupLines(group, toolRenderState{thoughts: map[string]bool{"g/r0": true}}), "\n")
	if count := strings.Count(expanded, "reasoning line"); count != 40 {
		t.Fatalf("expanded thinking has %d of 40 lines", count)
	}
}

// 空组不渲染；点击命中按行区间返回
func TestToolRowGroupHits(t *testing.T) {
	if view, hits := renderToolRows(&ToolGroupView{ID: "g"}, 80, toolRenderState{}, time.UnixMilli(0)); view != "" || len(hits) != 0 {
		t.Fatalf("empty group rendered: %q %+v", view, hits)
	}

	call := rowCall("1", "bash", map[string]any{"command": "echo hi"}, ToolSuccess)
	call.Result = "hi\nho"
	group := &ToolGroupView{ID: "g", Calls: []ToolCallView{call}}
	view, hits := renderToolRows(group, 80, toolRenderState{}, time.UnixMilli(0))
	if len(hits) != 1 || hits[0].Kind != "call" || hits[0].Key != "1" || hits[0].Start != 0 {
		t.Fatalf("call hit = %+v", hits)
	}
	if end := len(plainLines(view)); hits[0].End != end-1 {
		t.Fatalf("call hit end = %d, want %d", hits[0].End, end-1)
	}
}

// clickRow 在屏幕某行模拟一次不拖动的左键单击
func clickRow(m *Model, row int) {
	m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: row})
	m.handleMouse(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: row})
}

func lineTexts(count int) []string {
	lines := make([]string, count)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i+1)
	}
	return lines
}

// 单击摘要行展开分组，再点一次收起；单击单条调用只展开它自己
func TestToolRowClickTogglesRows(t *testing.T) {
	m := testModel(80, 24)
	group := &ToolGroupView{ID: "g", Calls: []ToolCallView{
		rowCall("1", "read", map[string]any{"path": "a"}, ToolSuccess),
		rowCall("2", "read", map[string]any{"path": "b"}, ToolSuccess),
	}}
	group.Calls[0].Result = "x"
	group.Calls[1].Result = "y"
	m.state.Messages = []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}
	m.refreshTranscript()

	clickRow(m, m.lastToolHits[0].Start)
	if !m.expandedGroups["g"] {
		t.Fatalf("click did not expand the group: %+v", m.lastToolHits)
	}
	// 展开后摘要行仍在首行，再点一次收起
	m.refreshTranscript()
	clickRow(m, m.lastToolHits[0].Start)
	if m.expandedGroups["g"] {
		t.Fatal("second click did not collapse the group")
	}
}

// 单击 Thinking... 展开完整正文，不截断
func TestToolRowClickExpandsThinking(t *testing.T) {
	m := testModel(80, 24)
	text := strings.Repeat("reasoning line\n", 30)
	group := &ToolGroupView{ID: "g", Thinking: []ThinkingBlockView{{Index: 0, Text: text}}}
	m.state.Messages = []Message{{ID: "tools", Role: "assistant", ToolGroup: group}}
	m.refreshTranscript()

	var thinkingLine int = -1
	for _, hit := range m.lastToolHits {
		if hit.Kind == "thinking" {
			thinkingLine = hit.Start
		}
	}
	if thinkingLine < 0 {
		t.Fatalf("no thinking hit: %+v", m.lastToolHits)
	}
	clickRow(m, thinkingLine)
	if !m.expandedThoughts["g/r0"] {
		t.Fatalf("click did not expand thinking: %+v", m.expandedThoughts)
	}
	view := ansi.Strip(m.renderMessage(m.state.Messages[0]))
	if strings.Count(view, "reasoning line") != 30 {
		t.Fatalf("expanded thinking is truncated: %d lines", strings.Count(view, "reasoning line"))
	}
}

// 展开的思考正文按宽度换行后，每一行都保持同一缩进
func TestToolRowThinkingWrapKeepsIndent(t *testing.T) {
	group := &ToolGroupView{ID: "g", Thinking: []ThinkingBlockView{{Index: 0, Text: strings.Repeat("word ", 60)}}}
	lines := groupLines(group, toolRenderState{thoughts: map[string]bool{"g/r0": true}})
	if len(lines) < 3 {
		t.Fatalf("thinking did not wrap: %q", lines)
	}
	for index, line := range lines[1:] {
		if !strings.HasPrefix(line, rowIndent) {
			t.Fatalf("wrapped row %d lost its indent: %q", index+1, line)
		}
	}
}

// 每轮的 thinking 排在该轮调用之前；不同轮的思考各占一行，互不覆盖
func TestToolRowThinkingPerRound(t *testing.T) {
	group := &ToolGroupView{ID: "g",
		Thinking: []ThinkingBlockView{
			{Index: 0, Round: 1, Text: "round one reasoning"},
			{Index: 0, Round: 2, Text: "round two reasoning"},
		},
		Calls: []ToolCallView{
			{ID: "c1", Name: "bash", Round: 1, Arguments: `{"command":"go test ./..."}`, Result: "ok", Status: ToolSuccess, ArgsComplete: true},
			{ID: "c2", Name: "bash", Round: 2, Arguments: `{"command":"go vet ./..."}`, Result: "ok", Status: ToolSuccess, ArgsComplete: true},
		},
	}
	lines := groupLines(group, toolRenderState{})
	want := []string{
		" Thinking...",
		" ⦁ Bash(go test ./...)",
		"   ⎿  ok",
		" Thinking...",
		" ⦁ Bash(go vet ./...)",
		"   ⎿  ok",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("per-round thinking =\n%s", strings.Join(lines, "\n"))
	}

	// 两轮各自的思考都能独立展开
	expanded := strings.Join(groupLines(group, toolRenderState{thoughts: map[string]bool{"g/r2": true}}), "\n")
	if !strings.Contains(expanded, "round two reasoning") || strings.Contains(expanded, "round one reasoning") {
		t.Fatalf("only round two should be open:\n%s", expanded)
	}
}

// 最后一轮只有思考没有调用时，思考仍然出现在末尾
func TestToolRowThinkingTailRound(t *testing.T) {
	group := &ToolGroupView{ID: "g",
		Thinking: []ThinkingBlockView{
			{Index: 0, Round: 1, Text: "first"},
			{Index: 0, Round: 2, Text: "second"},
		},
		Calls: []ToolCallView{{ID: "c1", Name: "bash", Round: 1, Arguments: `{"command":"x"}`, Result: "y", Status: ToolSuccess, ArgsComplete: true}},
	}
	lines := groupLines(group, toolRenderState{})
	if strings.Count(strings.Join(lines, "\n"), "Thinking...") != 2 {
		t.Fatalf("tail thinking was dropped:\n%s", strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != " Thinking..." {
		t.Fatalf("last line = %q, want the tail thinking line", lines[len(lines)-1])
	}
}

// 名字还没到的调用不上屏，避免出现只有状态点的空行
func TestToolRowHidesNamelessCall(t *testing.T) {
	group := &ToolGroupView{ID: "g", Calls: []ToolCallView{
		{ID: "c1", Name: "", Arguments: "", Status: ToolPending},
		{ID: "c2", Name: "bash", Arguments: `{"command":"npm test"}`, Status: ToolPending, StartedAt: time.UnixMilli(1), ArgsComplete: true},
	}}
	lines := groupLines(group, toolRenderState{})
	if len(lines) != 2 || lines[0] != " ⦁ Bash(npm test)" || lines[1] != "   ⎿  Running…" {
		t.Fatalf("nameless call was drawn:\n%s", strings.Join(lines, "\n"))
	}
}
