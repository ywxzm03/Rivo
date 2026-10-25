package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

func testRunner(t *testing.T) *Runner {
	t.Helper()
	return &Runner{
		ParentCwd:   t.TempDir(),
		SessionDir:  t.TempDir(),
		AgentDir:    t.TempDir(),
		ParentModel: llm.Model{Provider: "test", ID: "parent-model"},
		Thinking:    llm.ThinkingLevelMedium,
		TaskStore:   taskpkg.NewStore(),
	}
}

func TestRunRejectsMissingSessionDir(t *testing.T) {
	runner := testRunner(t)
	runner.SessionDir = ""
	if _, err := runner.Run(context.Background(), taskpkg.AgentSpec{ID: "agent-1", AgentType: "work"}); err == nil {
		t.Fatal("expected an error without a session directory")
	}
}

func TestRunWritesTranscriptAndMetadata(t *testing.T) {
	runner := testRunner(t)
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: "agent-1", AgentType: "work", Description: "do work", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime.Cancel != nil {
			runtime.Cancel()
		}
	}()
	// 1.transcript 路径落在 sessionDir/subagents 下
	want := filepath.Join(runner.SessionDir, "subagents", "agent-agent-1.jsonl")
	if runtime.OutputFile != want {
		t.Fatalf("output file = %q, want %q", runtime.OutputFile, want)
	}
	// 2.元数据记录 agentType 与 description,供 eviction 后恢复
	data, err := os.ReadFile(filepath.Join(runner.SessionDir, "subagents", "agent-agent-1.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.AgentType != "work" || meta.Description != "do work" {
		t.Fatalf("metadata = %#v", meta)
	}
	// 3.runtime 必须带着可用的 agent 与 Done 通道;Cancel 由 Manager 注入
	if runtime.Agent == nil || runtime.Done == nil {
		t.Fatalf("runtime = %#v", runtime)
	}
	if runtime.Agent.WorkingDir != runner.ParentCwd {
		t.Fatalf("agent working dir = %q, want %q", runtime.Agent.WorkingDir, runner.ParentCwd)
	}
}

func TestRunContinuationRequiresExistingTranscript(t *testing.T) {
	runner := testRunner(t)
	_, err := runner.Run(context.Background(), taskpkg.AgentSpec{ID: "agent-x", AgentType: "work", Continuation: true})
	if err == nil {
		t.Fatal("continuation without a transcript was accepted")
	}
}

func TestRunAppliesBuiltinPromptForEachType(t *testing.T) {
	for _, agentType := range []string{"explore", "plan", "work"} {
		runner := testRunner(t)
		runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{
			ID: "agent-" + agentType, AgentType: agentType, Description: agentType, Prompt: "go",
		})
		if err != nil {
			t.Fatalf("%s: %v", agentType, err)
		}
		// 1.内置类型必须注入对应 prompt,而不是通用兜底
		system := llm.CurrentSystemMessage(runtime.Agent.AgentContext.Messages)
		if system == nil {
			t.Fatalf("%s: no system message", agentType)
		}
		instructions := system.Sections["instructions"]
		// 有基础 instructions 时用 <agent-instructions> 追加,否则整体包在 <instructions> 里
		if !containsFold(instructions, "<instructions>") {
			t.Fatalf("%s: instructions block missing: %q", agentType, instructions)
		}
		want := map[string]string{"explore": "file search specialist", "plan": "software architect", "work": "use the tools available"}[agentType]
		if !containsFold(instructions, want) {
			t.Fatalf("%s: instructions do not contain %q", agentType, want)
		}
		if runtime.Cancel != nil {
			runtime.Cancel()
		}
	}
}

func TestRunExploreAndPlanOmitContextInstructions(t *testing.T) {
	runner := testRunner(t)
	// 项目里放一个 AGENTS.md,explore/plan 不应加载它
	if err := os.WriteFile(filepath.Join(runner.ParentCwd, "AGENTS.md"), []byte("PROJECT_RULE_MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{ID: "agent-1", AgentType: "explore", Description: "d", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime.Cancel != nil {
			if runtime.Cancel != nil {
				runtime.Cancel()
			}
		}
	}()
	system := llm.CurrentSystemMessage(runtime.Agent.AgentContext.Messages)
	if system == nil {
		t.Fatal("no system message")
	}
	for _, section := range system.Sections {
		if containsFold(section, "PROJECT_RULE_MARKER") {
			t.Fatal("explore agent inherited project context files")
		}
	}
}

func TestRunWorkLoadsProjectContext(t *testing.T) {
	runner := testRunner(t)
	if err := os.WriteFile(filepath.Join(runner.ParentCwd, "AGENTS.md"), []byte("PROJECT_RULE_MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{ID: "agent-2", AgentType: "work", Description: "d", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime.Cancel != nil {
			if runtime.Cancel != nil {
				runtime.Cancel()
			}
		}
	}()
	// work 按正常规则加载项目上下文
	system := llm.CurrentSystemMessage(runtime.Agent.AgentContext.Messages)
	if system == nil {
		t.Fatal("no system message")
	}
	found := false
	for _, section := range system.Sections {
		if containsFold(section, "PROJECT_RULE_MARKER") {
			found = true
		}
	}
	if !found {
		t.Fatal("work agent did not inherit project context files")
	}
}

func TestRunForkReusesParentConfigAndTools(t *testing.T) {
	runner := testRunner(t)
	parentCfg := llm.AgentConfig{Model: llm.Model{Provider: "p", ID: "parent"}, ThinkingLevel: llm.ThinkingLevelHigh, SessionID: "parent-session"}
	parentCtx := &llm.Context{Messages: []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "parent request"}}},
	}}
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: "agent-fork", AgentType: "fork", Description: "fork", Prompt: "carry on",
		ParentContext: parentCtx, ParentConfig: &parentCfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime.Cancel != nil {
			if runtime.Cancel != nil {
				runtime.Cancel()
			}
		}
	}()
	// 1.fork 继承父 model 与 thinking
	if runtime.Agent.AgentConfig.Model.ID != "parent" || runtime.Agent.AgentConfig.ThinkingLevel != llm.ThinkingLevelHigh {
		t.Fatalf("fork config = %#v", runtime.Agent.AgentConfig)
	}
	// 2.父消息前缀被保留,末尾是 fork 指令
	messages := runtime.Agent.AgentContext.Messages
	if len(messages) < 2 {
		t.Fatalf("fork messages = %d", len(messages))
	}
	if !containsFold(messages[0].(llm.UserMessage).Content[0].(llm.TextContent).Text, "parent request") {
		t.Fatal("fork did not inherit the parent transcript prefix")
	}
	last := messages[len(messages)-1].(llm.UserMessage).Content[0].(llm.TextContent).Text
	if !containsFold(last, "Your directive: carry on") {
		t.Fatalf("fork directive = %q", last)
	}
	// 3.fork 使用独立的 session ID
	if runtime.Agent.AgentConfig.SessionID == "parent-session" {
		t.Fatal("fork reused the parent session ID")
	}
}

func TestRunIgnoresParentModelWhenExplicit(t *testing.T) {
	runner := testRunner(t)
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: "agent-3", AgentType: "work", Description: "d", Prompt: "p",
		Model: llm.Model{Provider: "explicit", ID: "chosen"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime.Cancel != nil {
			if runtime.Cancel != nil {
				runtime.Cancel()
			}
		}
	}()
	// 显式指定的 model 必须覆盖父 model
	if got := runtime.Agent.AgentConfig.Model; got.Provider != "explicit" || got.ID != "chosen" {
		t.Fatalf("model = %#v, want explicit/chosen", got)
	}
}

func TestRunInstallsWorktreeCleanupForNewWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	runner := testRunner(t)
	initRepo(t, runner.ParentCwd)
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: "agent-wt", AgentType: "work", Description: "isolated", Prompt: "go", Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime.Cancel != nil {
			runtime.Cancel()
		}
		_ = exec.Command("git", "-C", runner.ParentCwd, "worktree", "remove", "--force", runtime.WorktreePath).Run()
	}()
	// 1.子 agent 自己创建的 worktree 必须带回收钩子,且元数据标明归属
	if runtime.WorktreePath == "" || runtime.WorktreeCleanup == nil {
		t.Fatalf("runtime = %#v, want cleanup hook", runtime)
	}
	data, err := os.ReadFile(filepath.Join(runner.SessionDir, "subagents", "agent-agent-wt.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.WorktreeOwned || meta.WorktreePath != runtime.WorktreePath {
		t.Fatalf("metadata = %#v, want owned worktree", meta)
	}
}

func TestRunSkipsWorktreeCleanupForResumedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	runner := testRunner(t)
	initRepo(t, runner.ParentCwd)
	first, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: "agent-wt", AgentType: "work", Description: "isolated", Prompt: "go", Worktree: true,
		InitialMessages: []llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "go"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cancel != nil {
		first.Cancel()
	}
	defer func() {
		_ = exec.Command("git", "-C", runner.ParentCwd, "worktree", "remove", "--force", first.WorktreePath).Run()
	}()
	// 1.恢复沿用同一个 worktree:即使元数据标明它由子 agent 创建,也不能装回收钩子
	resumed, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: "agent-wt", AgentType: "work", Description: "isolated", Prompt: "continue",
		Worktree: true, WorktreePath: first.WorktreePath, Continuation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if resumed.Cancel != nil {
			resumed.Cancel()
		}
	}()
	if resumed.WorktreePath != first.WorktreePath {
		t.Fatalf("resumed worktree = %q, want %q", resumed.WorktreePath, first.WorktreePath)
	}
	if resumed.WorktreeCleanup != nil {
		t.Fatal("resumed worktree must be kept until cleaned manually")
	}
}
