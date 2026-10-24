package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// 移植自插件 shell.test.ts / compact-tools.test.ts 的命令分类用例：
// 只看的命令折进探索组，能写文件或运行别的程序的一律不折
func TestClassifyShellCommand(t *testing.T) {
	cases := []struct {
		command string
		want    lookKind
		reason  string
	}{
		{"rg -n TODO src | head -20", lookSearch, "a pipeline of searches searches"},
		{"cat a.ts && wc -l b.ts", lookRead, "two reads read"},
		{"ls -la; echo done", lookList, "a listing and a neutral step list"},
		{"grep x a | sort | uniq", lookSearch, "sorting and uniqing a search still searches"},
		{"rg 'a|b' src", lookSearch, "a pipe inside quotes is part of the pattern"},
		{"rg x src 2>/dev/null", lookSearch, "output thrown away writes nothing"},
		{"rg x src 2>&1 | head -n 5", lookSearch, "joining two streams writes nothing"},
		{"cat a.ts | head -n 5", lookRead, "paging a read still reads"},
		{"find . -name '*.go'", lookSearch, "find looks around"},
		{"echo hi", lookNone, "a command that only prints looks at nothing"},
		{"true", lookNone, "a neutral command alone looks at nothing"},
		{"echo x && cat y", lookRead, "a neutral step does not change what cat does"},
		{"npm test", lookNone, "running a program is not looking around"},
		{"cat a.ts && npm test", lookNone, "one step that runs something keeps the command apart"},
		{"cat a.ts > b.ts", lookNone, "a read that writes a file is no read"},
		{"find . -delete", lookNone, "a find that deletes is no search"},
		{"fd x -x rm", lookNone, "an fd that runs a program is no search"},
		{"rg --pre ./pre.sh x", lookNone, "rg told to run a preprocessor is no search"},
		{"sort -o out.txt in.txt", lookNone, "sort told to write is no read"},
		{"sed -i s/a/b/ f.ts", lookNone, "an in-place sed writes"},
		{"sed 's/a/b/w out' f.ts", lookNone, "a sed script that writes writes"},
		{"awk '{print > \"out\"}' f.ts", lookNone, "an awk that prints to a file writes"},
		{"tee out.txt", lookNone, "tee exists to write"},
		{"xargs rm", lookNone, "xargs exists to run"},
		{"git grep TODO", lookSearch, "git grep searches"},
		{"git status", lookNone, "git status runs a program"},
		{"cat a.ts &", lookNone, "a background job is opaque"},
		{"echo $(rm -rf x)", lookNone, "a command substitution is opaque"},
		{"cat <<EOF", lookNone, "a heredoc is opaque"},
		{"", lookNone, "nothing looks at nothing"},
	}
	for _, tt := range cases {
		if got := classifyShellCommand(tt.command); got != tt.want {
			t.Fatalf("classifyShellCommand(%q) = %q, want %q (%s)", tt.command, got, tt.want, tt.reason)
		}
	}
}

// 工具本身决定它怎么“看”，不看参数
func TestLookKindOfTools(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want lookKind
	}{
		{"read", map[string]any{"path": "a"}, lookRead},
		{"grep", map[string]any{"pattern": "x"}, lookSearch},
		{"find", map[string]any{"pattern": "*.go"}, lookSearch},
		{"ls", map[string]any{}, lookList},
		{"bash", map[string]any{"command": "ls src"}, lookList},
		{"bash", map[string]any{"command": "go build ./..."}, lookNone},
		{"write", map[string]any{"path": "a"}, lookNone},
		{"edit", map[string]any{"path": "a"}, lookNone},
		{"web_search", map[string]any{"query": "x"}, lookNone},
	}
	for _, tt := range cases {
		if got := lookKindOf(tt.name, tt.args); got != tt.want {
			t.Fatalf("lookKindOf(%s, %v) = %q, want %q", tt.name, tt.args, got, tt.want)
		}
	}
}

// 组头按固定措辞计数：文件按不同路径去重，只读命令在没有文件时按命令数
func TestGroupSummaryCounts(t *testing.T) {
	calls := []ToolCallView{
		rowCall("1", "grep", map[string]any{"pattern": "a"}, ToolSuccess),
		rowCall("2", "grep", map[string]any{"pattern": "b"}, ToolSuccess),
		rowCall("3", "read", map[string]any{"path": "a.go"}, ToolSuccess),
		rowCall("4", "read", map[string]any{"path": "a.go"}, ToolSuccess),
		rowCall("5", "ls", map[string]any{}, ToolSuccess),
		rowCall("6", "bash", map[string]any{"command": "cat b.go"}, ToolSuccess),
	}
	if got := summarizeToolRowGroup(calls); got != "Searched for 2 patterns, read 1 file, listed 1 directory" {
		t.Fatalf("summary = %q", got)
	}

	searches := []ToolCallView{rowCall("1", "grep", map[string]any{"pattern": "a"}, ToolSuccess)}
	if got := summarizeToolRowGroup(searches); got != "Searched for 1 pattern" {
		t.Fatalf("single search = %q", got)
	}
	reads := []ToolCallView{rowCall("1", "read", map[string]any{"path": "a.go"}, ToolSuccess)}
	if got := summarizeToolRowGroup(reads); got != "Read 1 file" {
		t.Fatalf("single read = %q", got)
	}
}

// 运行中的组把每个动词都改成进行时
func TestGroupSummaryRunning(t *testing.T) {
	calls := []ToolCallView{
		rowCall("1", "read", map[string]any{"path": "a.go"}, ToolSuccess),
		rowCall("2", "grep", map[string]any{"pattern": "x"}, ToolPending),
	}
	if got := summarizeToolRowGroup(calls); got != "Searching for 1 pattern, reading 1 file" {
		t.Fatalf("running summary = %q", got)
	}
}

// 结果行摘要按插件规则剥离自己生成的尾部提示，再数行
func TestToolRowLineCounts(t *testing.T) {
	cases := []struct {
		name string
		call ToolCallView
		want string
	}{
		{
			name: "read with footer",
			call: ToolCallView{Name: "read", Result: "a\nb\nc\n\n[Showing lines 1-3 of 80. Use offset=4 to continue.]"},
			want: "Read 3 lines",
		},
		{
			name: "read single line",
			call: ToolCallView{Name: "read", Result: "only"},
			want: "Read 1 line",
		},
		{
			name: "grep",
			call: ToolCallView{Name: "grep", Result: "a.go:1: x\na.go:2: y"},
			want: "Found 2 lines",
		},
		{
			// 工具自己追加的尾部提示不算结果行
			name: "grep with generated footer",
			call: ToolCallView{Name: "grep", Result: "a.go:1: x\na.go:2: y\n\n[100 matches limit reached. Use limit=200 for more, or refine pattern]"},
			want: "Found 2 lines",
		},
		{
			name: "find",
			call: ToolCallView{Name: "find", Result: "a.go\nb.go\nc.go"},
			want: "Found 3 files",
		},
		{
			name: "ls",
			call: ToolCallView{Name: "ls", Result: "a\nb\nc"},
			want: "Listed 3 paths",
		},
		{
			name: "ls empty",
			call: ToolCallView{Name: "ls", Result: "(empty directory)"},
			want: "Listed 1 path",
		},
		{
			name: "write counts its content",
			call: ToolCallView{Name: "write", Arguments: `{"path":"notes.md","content":"a\nb\nc"}`, Result: "Successfully wrote to notes.md"},
			want: "Wrote 3 lines to notes.md",
		},
		{
			name: "bash has no summary",
			call: ToolCallView{Name: "bash", Result: "hi"},
			want: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolRowOutcome(tt.call); got != tt.want {
				t.Fatalf("outcome = %q, want %q", got, tt.want)
			}
		})
	}
}

// edit 的增删行从 diff 详情里数，配对行按删除在前、新增在后
func TestToolRowEditChangeCount(t *testing.T) {
	patch := "@@ -1,2 +1,3 @@\n a\n-b\n+B\n+c"
	call := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Details = rowDetails(t, map[string]any{"Patch": patch})
	if got := toolRowOutcome(call); got != "Added 2 lines, removed 1 line" {
		t.Fatalf("edit outcome = %q", got)
	}

	onlyAdds := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	onlyAdds.Details = rowDetails(t, map[string]any{"Patch": "@@ -1,1 +1,2 @@\n a\n+b"})
	if got := toolRowOutcome(onlyAdds); got != "Added 1 line" {
		t.Fatalf("adds-only outcome = %q", got)
	}

	onlyRemoves := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	onlyRemoves.Details = rowDetails(t, map[string]any{"Patch": "@@ -1,2 +1,1 @@\n a\n-b"})
	if got := toolRowOutcome(onlyRemoves); got != "Removed 1 line" {
		t.Fatalf("removes-only outcome = %q", got)
	}
}

// 命令的失败以退出码打头，其余工具只给一行原因
func TestToolRowFailureHeadline(t *testing.T) {
	cases := []struct {
		name    string
		call    ToolCallView
		head    string
		detail  string
		hasBody bool
	}{
		{
			name:    "exit code",
			call:    ToolCallView{Name: "bash", Result: "boom\nmore\n\nCommand exited with code 2"},
			head:    "Error: Exit code 2",
			detail:  "boom\nmore",
			hasBody: true,
		},
		{
			name:   "timeout",
			call:   ToolCallView{Name: "bash", Result: "Command timed out after 5 seconds"},
			head:   "Error: Command timed out after 5 seconds",
			detail: "",
		},
		{
			name:   "tool error prefix",
			call:   ToolCallView{Name: "read", Result: "tool error: open a.go: no such file or directory"},
			head:   "Error: open a.go: no such file or directory",
			detail: "",
		},
		{
			name:   "bare reason",
			call:   ToolCallView{Name: "grep", Result: "rg: src: No such file or directory"},
			head:   "Error: rg: src: No such file or directory",
			detail: "",
		},
		{
			name:   "aborted",
			call:   ToolCallView{Name: "bash", Result: "Operation aborted", Status: ToolInterrupted},
			head:   "Error: Operation aborted",
			detail: "",
		},
		{
			// agent 会给失败结果加前缀，退出状态仍要认出来，前缀不能漏进输出
			name:    "tool error prefix on a command",
			call:    ToolCallView{Name: "bash", Result: "tool error: boom\nmore\n\nCommand exited with code 3"},
			head:    "Error: Exit code 3",
			detail:  "boom\nmore",
			hasBody: true,
		},
		{
			name:   "tool error prefix without output",
			call:   ToolCallView{Name: "bash", Result: "tool error: Command exited with code 1"},
			head:   "Error: Exit code 1",
			detail: "",
		},
		{
			name:   "tool error prefix on another tool",
			call:   ToolCallView{Name: "grep", Result: "tool error: Path not found: /x"},
			head:   "Error: Path not found: /x",
			detail: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			head, detail := toolRowFailure(tt.call)
			if head != tt.head || detail != tt.detail {
				t.Fatalf("failure = (%q, %q), want (%q, %q)", head, detail, tt.head, tt.detail)
			}
		})
	}
}

// diff 解析：统一 patch 优先，退回带行号的展示 diff；三行上下文裁剪用 ⋮ 标断点
func TestParseRowDiff(t *testing.T) {
	patch := "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c"
	lines := parseRowDiff(patch, "")
	want := []rowDiffLine{
		{Kind: diffContext, Number: 1, Content: "a"},
		{Kind: diffRemove, Number: 2, Content: "b"},
		{Kind: diffAdd, Number: 2, Content: "B"},
		{Kind: diffContext, Number: 3, Content: "c"},
	}
	if len(lines) != len(want) {
		t.Fatalf("parsed %d lines, want %d", len(lines), len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}

	display := " 1  keep\n-2  old\n+2  new\n ...\n 9  tail"
	lines = parseRowDiff("", display)
	if len(lines) != 5 || lines[4].Number != 9 || lines[3].Kind != diffSeparator {
		t.Fatalf("display diff = %+v", lines)
	}
}

// 上下文裁剪：每个变更两侧留三行，两段变更之间被裁掉的部分用一个 ⋮；
// 文件开头或结尾被裁掉时没有东西可断开，因此不留标记
func TestTrimRowDiff(t *testing.T) {
	var lines []rowDiffLine
	for number := 1; number <= 30; number++ {
		kind := diffContext
		switch number {
		case 10:
			kind = diffRemove
		case 11:
			kind = diffAdd
		case 25:
			kind = diffRemove
		case 26:
			kind = diffAdd
		}
		lines = append(lines, rowDiffLine{Kind: kind, Number: number, Content: "x"})
	}
	trimmed := trimRowDiff(lines, rowDiffContext)
	separators := 0
	for _, line := range trimmed {
		if line.Kind == diffSeparator {
			separators++
		}
	}
	if separators != 1 {
		t.Fatalf("separators = %d, want 1 (the gap between the two changes)", separators)
	}
	if trimmed[0].Kind == diffSeparator || trimmed[len(trimmed)-1].Kind == diffSeparator {
		t.Fatalf("edge separator kept: %+v", trimmed[0])
	}
	// 3 context + 2 changes + 3 context + ⋮ + 3 context + 2 changes + 3 context
	if len(trimmed) != 17 {
		t.Fatalf("trimmed to %d rows, want 17", len(trimmed))
	}
}

// 两个 hunk 之间用一个 ⋮ 断开
func TestParseRowDiffHunks(t *testing.T) {
	patch := strings.Join([]string{
		"@@ -1,2 +1,2 @@",
		" a",
		"-b",
		"+B",
		"@@ -40,2 +40,2 @@",
		" c",
		"-d",
		"+D",
	}, "\n")
	lines := parseRowDiff(patch, "")
	separators := 0
	for _, line := range lines {
		if line.Kind == diffSeparator {
			separators++
		}
	}
	if separators != 1 {
		t.Fatalf("hunk separators = %d, want 1", separators)
	}
	if lines[3].Kind != diffSeparator || lines[3].Content != "⋮" {
		t.Fatalf("separator row = %+v", lines[3])
	}
}

// 词级高亮：只把两行里不同的词换成更亮的底色
func TestWordDiffRanges(t *testing.T) {
	removed, added := wordDiffRanges("  return `Hello, ${name}!`;", "  return `Welcome, ${name}!`;")
	if len(removed) != 1 || len(added) != 1 {
		t.Fatalf("ranges = %v %v, want one range each", removed, added)
	}
	if got := "  return `Hello, ${name}!`;"[removed[0].Start:removed[0].End]; got != "Hello" {
		t.Fatalf("removed range = %q, want Hello", got)
	}
	if got := "  return `Welcome, ${name}!`;"[added[0].Start:added[0].End]; got != "Welcome" {
		t.Fatalf("added range = %q, want Welcome", got)
	}

	// 几乎完全重写的两行不做词级标记
	if removed, added := wordDiffRanges("completely different", "nothing alike at all"); len(removed)+len(added) != 0 {
		t.Fatalf("rewrite got word marks: %v %v", removed, added)
	}
}

// 多 hunk 的 edit 在收起时也保留 ⋮ 断点，并且只留三行上下文
func TestToolRowMultiHunkEdit(t *testing.T) {
	patch := strings.Join([]string{
		"@@ -1,12 +1,12 @@",
		" a1", " a2", " a3", " a4", " a5", " a6",
		"-a7", "+A7",
		" a8", " a9", " a10", " a11", " a12",
		"@@ -40,3 +40,3 @@",
		" b1", " b2",
		"-b3", "+B3",
	}, "\n")
	call := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Result = "Successfully replaced 2 block(s) in a.ts."
	call.Details = rowDetails(t, map[string]any{"Patch": patch})
	lines := strings.Join(rowLines(call, false), "\n")
	if !strings.Contains(lines, "⋮") {
		t.Fatalf("multi-hunk diff lost its break mark:\n%s", lines)
	}
	if !strings.Contains(lines, "A7") || !strings.Contains(lines, "B3") {
		t.Fatalf("multi-hunk diff lost a change:\n%s", lines)
	}
	if strings.Contains(lines, " 1  a1") || strings.Contains(lines, " 3  a3") {
		t.Fatalf("first hunk kept too much context:\n%s", lines)
	}
	if strings.Contains(lines, "⋮\n                 ⋮") {
		t.Fatalf("two break marks in a row:\n%s", lines)
	}
}

// 会话恢复时没有耗时，结果句不编造时间；行数仍然按结果算
func TestToolRowRestoredCallOmitsDuration(t *testing.T) {
	call := rowCall("1", "read", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Result = "one\ntwo"
	lines := rowLines(call, false)
	if len(lines) != 2 || lines[1] != "   ⎿  Read 2 lines" {
		t.Fatalf("restored row = %q", lines)
	}
}

// diff 的每一行都必须恰好占满渲染宽度：超出会让终端折行，行号与内容被拆成两行
func TestToolRowDiffRowsFitWidth(t *testing.T) {
	patch := strings.Join([]string{
		"@@ -360,9 +360,9 @@",
		" // Insert 插入键，已存在时返回 false",
		" func (t *Tree[K]) Insert(key K) bool {",
		"-",
		"-var path []*node[K]",
		"+var parent *node[K]",
		" cur := t.root",
	}, "\n")
	call := rowCall("1", "edit", map[string]any{"path": "rbtree.go"}, ToolSuccess)
	call.Result = "Successfully replaced 1 block(s) in rbtree.go."
	call.Details = rowDetails(t, map[string]any{"Patch": patch})

	for _, width := range []int{100, 80, 44} {
		for _, line := range renderToolRow(call, width, false, time.UnixMilli(0)) {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d: row is %d cells, which wraps:\n%q", width, got, line)
			}
		}
	}
}

// 变更行的行号与符号用变更色（红/绿），上下文行用 dim
func TestToolRowDiffGutterColors(t *testing.T) {
	patch := strings.Join([]string{
		"@@ -1,3 +1,3 @@",
		" keep",
		"-old",
		"+new",
	}, "\n")
	call := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Details = rowDetails(t, map[string]any{"Patch": patch})
	lines := renderToolRow(call, 100, false, time.UnixMilli(0))

	// lines[0] 是调用行，lines[1] 是结果句，之后是 diff
	removed, added, context := lines[3], lines[4], lines[2]
	if !strings.Contains(removed, fgSequence(redColor)+" 2 ") {
		t.Fatalf("removed gutter number is not red: %q", removed)
	}
	if !strings.Contains(added, fgSequence(greenColor)+" 2 ") {
		t.Fatalf("added gutter number is not green: %q", added)
	}
	if !strings.Contains(context, fgSequence(dimColor)+" 1 ") {
		t.Fatalf("context gutter number is not dim: %q", context)
	}
}

// 变更行的底色从行首一直铺到行尾，行号格子也带底色：中间不能出现重置码
func TestToolRowDiffTintIsContinuous(t *testing.T) {
	patch := "@@ -1,2 +1,2 @@\n keep\n-old\n+new"
	call := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Details = rowDetails(t, map[string]any{"Patch": patch})
	lines := renderToolRow(call, 60, false, time.UnixMilli(0))

	removed := lines[3]
	// 底色在正文之前就打开，且行内不再出现重置
	body := strings.TrimPrefix(removed, rowIndent)
	if !strings.HasPrefix(body, bgSequence(removedTint)) {
		t.Fatalf("removed row does not open with its tint: %q", removed)
	}
	if strings.Contains(strings.TrimSuffix(body, "\x1b[39m\x1b[49m"), "\x1b[0m") {
		t.Fatalf("removed row resets its tint mid-row: %q", removed)
	}
	if !strings.HasSuffix(body, "\x1b[39m\x1b[49m") {
		t.Fatalf("removed row does not reset at the end: %q", removed)
	}
}

// 超宽行按正文宽度换行：续行重复符号、行号留空，整行仍然只有渲染宽度
func TestToolRowDiffWrapsLongLines(t *testing.T) {
	long := strings.Repeat("abcdefghij", 12)
	patch := "@@ -1,1 +1,2 @@\n keep\n+" + long
	call := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Details = rowDetails(t, map[string]any{"Patch": patch})
	lines := renderToolRow(call, 60, false, time.UnixMilli(0))

	var added []string
	for _, line := range lines[3:] {
		if strings.Contains(line, "+") || strings.Contains(line, "abcdefghij") {
			added = append(added, line)
		}
	}
	if len(added) < 2 {
		t.Fatalf("long added line was not wrapped: %d rows", len(added))
	}
	for index, line := range added {
		if width := ansi.StringWidth(line); width > 60 {
			t.Fatalf("wrapped row %d is %d cells: %q", index, width, line)
		}
	}
	// 续行：符号重复，行号列留空
	continuation := plainLines(added[1])[0]
	if !strings.Contains(continuation, "     +") {
		t.Fatalf("continuation row = %q, want a repeated + and a blank number", continuation)
	}
}

// 上下文行的符号列是竖线 │，变更行是 + 或 -：与参考 diff 的 gutter 一致
func TestToolRowDiffContextSeparator(t *testing.T) {
	patch := "@@ -1,2 +1,2 @@\n keep\n-old\n+new"
	call := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Details = rowDetails(t, map[string]any{"Patch": patch})
	lines := rowLines(call, false)
	if lines[2] != "       1 │keep" {
		t.Fatalf("context row = %q, want a │ separator", lines[2])
	}
	if lines[3] != "       2 -old" {
		t.Fatalf("removed row = %q", lines[3])
	}
	if lines[4] != "       2 +new" {
		t.Fatalf("added row = %q", lines[4])
	}
}

// 词级高亮用更亮的底色，且只覆盖被改动的词
func TestToolRowDiffWordTintColors(t *testing.T) {
	patch := "@@ -1,1 +1,1 @@\n-  return `Hello, ${name}!`;\n+  return `Welcome, ${name}!`;"
	call := rowCall("1", "edit", map[string]any{"path": "a.ts"}, ToolSuccess)
	call.Details = rowDetails(t, map[string]any{"Patch": patch})
	lines := renderToolRow(call, 100, false, time.UnixMilli(0))

	removed, added := lines[2], lines[3]
	if !strings.Contains(removed, bgSequence(removedWordTint)+"Hello") {
		t.Fatalf("removed row does not highlight the changed word: %q", removed)
	}
	if !strings.Contains(added, bgSequence(addedWordTint)+"Welcome") {
		t.Fatalf("added row does not highlight the changed word: %q", added)
	}
	// 高亮只覆盖改动词：词后紧接整行底色的恢复
	if !strings.Contains(removed, "Hello"+bgSequence(removedTint)) {
		t.Fatalf("removed row does not restore the base tint after the word: %q", removed)
	}
}
