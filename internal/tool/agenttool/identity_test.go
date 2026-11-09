package agenttool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// 任务号同时是子会话号与文件名主体:三处必须一致,且不再叠加 agent- 前缀
func TestRunUsesTaskIDForSessionAndPaths(t *testing.T) {
	runner := testRunnerWithStream(t)
	taskID := "agent-identity-1"
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: taskID, AgentType: "work", Description: "identity", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	subagentDir := filepath.Join(runner.SessionDir, "subagents")
	wantTranscript := filepath.Join(subagentDir, taskID+".jsonl")
	if runtime.OutputFile != wantTranscript {
		t.Fatalf("transcript = %q, want %q", runtime.OutputFile, wantTranscript)
	}
	if _, err := os.Stat(filepath.Join(subagentDir, taskID+".meta.json")); err != nil {
		t.Fatalf("metadata path does not follow task ID: %v", err)
	}
	// 首轮对话才会正式发布会话文件,发布后才能读回会话头
	if _, err := runtime.Agent.RunMessage(context.Background(), llm.UserMessage{
		Content: []llm.Content{llm.TextContent{Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	sm, err := session.Open(runtime.OutputFile, runner.ParentCwd)
	if err != nil {
		t.Fatal(err)
	}
	if sm.ID() != taskID {
		t.Fatalf("session id = %q, want task id %q", sm.ID(), taskID)
	}
	if runtime.Agent.Config().SessionID != taskID {
		t.Fatalf("agent session id = %q, want task id %q", runtime.Agent.Config().SessionID, taskID)
	}
	// 旧的双前缀路径不能再出现
	if _, err := os.Stat(filepath.Join(subagentDir, "agent-"+taskID+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("legacy prefixed transcript still created: %v", err)
	}
}

// 续跑打开同一份记录:会话号不变,历史与新输入都在同一上下文里
func TestRunContinuationPreservesIdentityAndHistory(t *testing.T) {
	runner := testRunnerWithStream(t)
	taskID := "agent-continuation-1"
	first, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: taskID, AgentType: "work", Description: "continuation", Prompt: "FIRST",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Agent.RunMessage(context.Background(), llm.UserMessage{
		Content: []llm.Content{llm.TextContent{Text: "FIRST"}},
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Run(context.Background(), taskpkg.AgentSpec{
		ID: taskID, AgentType: "work", Description: "continuation", Prompt: "SECOND", Continuation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.OutputFile != first.OutputFile {
		t.Fatalf("continuation transcript = %q, want %q", resumed.OutputFile, first.OutputFile)
	}
	if _, err := resumed.Agent.RunMessage(context.Background(), llm.UserMessage{
		Content: []llm.Content{llm.TextContent{Text: "SECOND"}},
	}); err != nil {
		t.Fatal(err)
	}
	sm, err := session.Open(resumed.OutputFile, runner.ParentCwd)
	if err != nil {
		t.Fatal(err)
	}
	if sm.ID() != taskID {
		t.Fatalf("continued session id = %q, want task id %q", sm.ID(), taskID)
	}
	var texts []string
	for _, message := range sm.BuildContext().Messages() {
		user, ok := message.(llm.UserMessage)
		if !ok {
			continue
		}
		texts = append(texts, llm.TextOf(user.Content))
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "FIRST") || !strings.Contains(joined, "SECOND") {
		t.Fatalf("continuation context = %q, want both turns", joined)
	}
}

// 身份不合法时必须在挂载文件备份、启动模型之前失败,并且不改动磁盘内容
func TestRunContinuationRejectsInvalidIdentity(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "missing transcript"},
		{name: "empty file", content: ""},
		{name: "whitespace only", content: "\n \n"},
		{name: "missing header", content: `{"type":"message","id":"m1","parentId":null,"timestamp":"t"}`},
		{name: "foreign id", content: `{"type":"session","version":1,"id":"other-task","timestamp":"t","cwd":"/project"}` + "\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := testRunnerWithStream(t)
			taskID := "agent-broken-1"
			path := filepath.Join(runner.SessionDir, "subagents", taskID+".jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if testCase.name != "missing transcript" {
				if err := os.WriteFile(path, []byte(testCase.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), taskpkg.AgentSpec{
				ID: taskID, AgentType: "work", Description: "broken", Prompt: "go", Continuation: true,
			}); err == nil {
				t.Fatal("invalid continuation identity was accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("transcript mutated: before %q after %q", before, after)
			}
		})
	}
}

// 任务号直接参与文件路径,非法值必须在创建任何文件之前被拒绝
func TestRunRejectsInvalidTaskIDBeforeSideEffects(t *testing.T) {
	for _, taskID := range []string{"", "../escape", "a/b", "con", "-leading"} {
		t.Run(taskID, func(t *testing.T) {
			runner := testRunnerWithStream(t)
			if _, err := runner.Run(context.Background(), taskpkg.AgentSpec{
				ID: taskID, AgentType: "work", Description: "bad", Prompt: "go",
			}); err == nil {
				t.Fatalf("task id %q was accepted", taskID)
			}
			entries, err := os.ReadDir(filepath.Join(runner.SessionDir, "subagents"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid task id left files behind: %v", entries)
			}
		})
	}
}

// testRunnerWithStream 在标准 Runner 上装一个能返回固定回复的 stream
func testRunnerWithStream(t *testing.T) *Runner {
	t.Helper()
	runner := testRunner(t)
	runner.SetModel(llm.Model{Provider: "test", ID: "parent-model"}, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		events := make(chan llm.AssistantMessageEvent, 1)
		events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{
			Content: []llm.Content{llm.TextContent{Text: "done"}},
		}}
		close(events)
		return events, nil
	})
	return runner
}
