package session

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/compaction"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
)

// TestPreparePromptDeclaresOnFirstRequest 首轮请求前重放为空，追加一条含全部分节的声明
func TestPreparePromptDeclaresOnFirstRequest(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	runner := &fakeRunner{}
	coord.Agent = runner
	baseline := prompt.Sections{"preamble": "base", "cwd": "/work"}
	coord.SetPromptSections(baseline)

	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}

	// 落盘一条，且压入内存一条
	ctx := sm.BuildContext()
	if len(ctx.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 declaration", len(ctx.Messages))
	}
	msg, ok := ctx.Messages[0].(llm.SystemMessage)
	if !ok {
		t.Fatalf("message = %T, want SystemMessage", ctx.Messages[0])
	}
	if msg.Sections["preamble"] != "base" || msg.Sections["cwd"] != "/work" {
		t.Errorf("sections = %v, want full baseline", msg.Sections)
	}
	if len(runner.appended) != 1 {
		t.Fatalf("appended = %d, want 1", len(runner.appended))
	}
}

// TestPreparePromptNoChangeNoMessage 无差异时不产生任何条目
func TestPreparePromptNoChangeNoMessage(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	runner := &fakeRunner{}
	coord.Agent = runner
	coord.SetPromptSections(prompt.Sections{"preamble": "base", "cwd": "/work"})

	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}
	// 第二次调用：重放结果已与基线一致
	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}

	ctx := sm.BuildContext()
	if len(ctx.Messages) != 1 {
		t.Fatalf("messages = %d, want still 1 after no-op", len(ctx.Messages))
	}
	if len(runner.appended) != 1 {
		t.Fatalf("appended = %d, want 1", len(runner.appended))
	}
}

// TestPreparePromptEmitsPatchForChangedSection 分节变化时只追加发生变化的那一节
func TestPreparePromptEmitsPatchForChangedSection(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	runner := &fakeRunner{}
	coord.Agent = runner
	coord.SetPromptSections(prompt.Sections{"preamble": "base", "cwd": "/old"})
	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}

	// 模拟 /reload 后 cwd 变化
	coord.SetPromptSections(prompt.Sections{"preamble": "base", "cwd": "/new"})
	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}

	ctx := sm.BuildContext()
	if len(ctx.Messages) != 2 {
		t.Fatalf("messages = %d, want declaration + patch", len(ctx.Messages))
	}
	patch, ok := ctx.Messages[1].(llm.SystemMessage)
	if !ok {
		t.Fatalf("message = %T, want SystemMessage patch", ctx.Messages[1])
	}
	// 补丁只含变化的分节
	if len(patch.Sections) != 1 || patch.Sections["cwd"] != "/new" {
		t.Errorf("patch = %v, want only cwd", patch.Sections)
	}
	// 重放后与基线一致
	current := llm.CurrentSystemMessage(ctx.Messages)
	if current.Sections["cwd"] != "/new" || current.Sections["preamble"] != "base" {
		t.Errorf("replayed = %v, want baseline", current.Sections)
	}
}

// TestPreparePromptEmitsRemovalWhenSectionDisappears instructions 消失时追加空串表示删除
func TestPreparePromptEmitsRemovalWhenSectionDisappears(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	runner := &fakeRunner{}
	coord.Agent = runner
	coord.SetPromptSections(prompt.Sections{"preamble": "base", "instructions": "<instructions>x</instructions>"})
	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}

	coord.SetPromptSections(prompt.Sections{"preamble": "base"})
	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}

	ctx := sm.BuildContext()
	patch, ok := ctx.Messages[1].(llm.SystemMessage)
	if !ok {
		t.Fatalf("message = %T, want SystemMessage patch", ctx.Messages[1])
	}
	if value, present := patch.Sections["instructions"]; !present || value != "" {
		t.Errorf("patch = %v, want instructions removal marker", patch.Sections)
	}
	// 重放后该节确实消失
	if current := llm.CurrentSystemMessage(ctx.Messages); current.Sections["instructions"] != "" {
		t.Errorf("replayed = %v, want instructions removed", current.Sections)
	}
}

// TestPreparePromptRunsBeforeAgentRun Run 进入 Agent 之前先对齐 prompt
func TestPreparePromptRunsBeforeAgentRun(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	runner := &fakeRunner{}
	coord.Agent = runner
	coord.SetPromptSections(prompt.Sections{"preamble": "base"})

	if _, err := coord.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(runner.appended) != 1 {
		t.Fatalf("appended = %d, want declaration before Run", len(runner.appended))
	}
}

// TestPreparePromptDisabledWithoutBaseline 未设置基线分节时不产生条目
func TestPreparePromptDisabledWithoutBaseline(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	runner := &fakeRunner{}
	coord.Agent = runner

	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}
	if len(sm.BuildContext().Messages) != 0 {
		t.Errorf("messages = %d, want none without baseline", len(sm.BuildContext().Messages))
	}
}

// TestCompactionSnapshotTakenFromProjection 压缩时把当前重放结果作为快照写入压缩条目
func TestCompactionSnapshotTakenFromProjection(t *testing.T) {
	dir := t.TempDir()
	summaryCalls := 0
	summaryStreamFn := countingStreamFn(&summaryCalls,
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: summaryText}}, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	coord.Compactor = &compaction.Compactor{
		StreamFn: summaryStreamFn,
		Model:    llm.Model{ID: "m1"},
		Config:   compaction.Config{ContextWindow: 10000, ReserveTokens: 500, KeepRecentTokens: 3},
	}
	runner := &fakeRunner{}
	coord.Agent = runner
	coord.SetPromptSections(prompt.Sections{"preamble": "base", "cwd": "/work"})

	// 声明 system prompt 并累积可压缩历史
	if err := coord.preparePrompt(); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []llm.Message{
		textMsg("u1"),
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "a1"}}},
		textMsg("u2"),
	} {
		if err := coord.Handle(llm.MessageEndEvent{Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := coord.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	// 压缩条目携带快照，压缩后投影首条仍是 system prompt
	ctx := sm.BuildContext()
	first, ok := ctx.Messages[0].(llm.SystemMessage)
	if !ok {
		t.Fatalf("first message = %T, want SystemMessage snapshot", ctx.Messages[0])
	}
	if first.Sections["preamble"] != "base" {
		t.Errorf("snapshot = %v, want current prompt", first.Sections)
	}
}

// TestSetPromptSectionsConcurrentWithRun 并发设置基线与 Run 请求前 diff 不得构成数据竞争；
// preparePrompt 由 Run 持锁调用，因此经 Run 走真实路径
func TestSetPromptSectionsConcurrentWithRun(t *testing.T) {
	dir := t.TempDir()
	sm := New("/project", dir)
	coord := NewCoordinator(sm, nil)
	runner := &fakeRunner{}
	coord.Agent = runner
	coord.SetPromptSections(prompt.Sections{"preamble": "base", "cwd": "/old"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			coord.SetPromptSections(prompt.Sections{"preamble": "base", "cwd": "/new"})
		}
	}()
	for i := 0; i < 50; i++ {
		if _, err := coord.Run(context.Background(), "hi"); err != nil {
			t.Errorf("Run() error = %v", err)
			break
		}
	}
	<-done
}
