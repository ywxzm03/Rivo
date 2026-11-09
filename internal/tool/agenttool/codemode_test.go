package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	toolpkg "github.com/ywxzm03/Rivo/internal/tool"
	"github.com/ywxzm03/Rivo/internal/tool/codemode"
)

func runChildCodemode(t *testing.T, sm *session.SessionManager, tools []llm.Tool, code string) llm.ToolResultMessage {
	t.Helper()
	turn := 0
	child := agent.New(agent.Options{
		Config: llm.AgentConfig{SessionID: sm.ID()}, Context: llm.Context{Tools: tools, Messages: sm.BuildContext().Messages()}, WorkingDir: sm.Cwd(),
		Hooks: agent.Hooks{CommitMessages: func(messages []llm.Message) ([]string, error) {
			ids, err := sm.AppendMessages(messages)
			if err != nil {
				return ids, err
			}
			for i, message := range messages {
				if _, ok := message.(llm.UserMessage); ok {
					if err := sm.MakeFileHistorySnapshot(ids[i]); err != nil {
						return ids, err
					}
				}
			}
			return ids, nil
		}},
		Stream: func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
			turn++
			message := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}
			if turn == 1 {
				message = &llm.AssistantMessage{Content: []llm.Content{llm.ToolCall{ID: "script", Name: "codemode", Arguments: map[string]any{"code": code}}}, StopReason: llm.StopReasonToolUse}
			}
			events := make(chan llm.AssistantMessageEvent, 1)
			events <- llm.AssistantMessageDoneEvent{Message: message}
			close(events)
			return events, nil
		},
	})
	if _, err := child.Run(context.Background(), "run script"); err != nil {
		t.Fatal(err)
	}
	messages := child.Context().Messages
	for i := len(messages) - 1; i >= 0; i-- {
		if result, ok := messages[i].(llm.ToolResultMessage); ok && result.ToolName == "codemode" {
			return result
		}
	}
	t.Fatal("missing codemode result")
	return llm.ToolResultMessage{}
}

func childCodemodeBody(t *testing.T, result llm.ToolResultMessage) string {
	t.Helper()
	if result.IsError || len(result.Content) < 2 {
		t.Fatalf("script failed or missing output: %+v", result)
	}
	return llm.TextOf(result.Content[1:])
}

func TestCodemodeChildIsolation(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	parent := session.New(cwd, t.TempDir())
	parent.AttachFileHistory(agentDir)
	parentTools := []llm.Tool{codemode.New(parent), &toolpkg.Write{History: toolpkg.Tracker(parent.FileHistory())}, &toolpkg.Edit{History: toolpkg.Tracker(parent.FileHistory())}}
	runChildCodemode(t, parent, parentTools, `store("value", "parent");`)
	child := session.New(cwd, t.TempDir())
	child.AttachFileHistory(agentDir)
	// fork 复制父消息，但复制的 StoreWrites 仍属于父 SessionID
	if _, err := child.AppendMessages(buildForkMessages(parent.BuildContext().Messages(), "continue", "", cwd)); err != nil {
		t.Fatal(err)
	}
	tools := childTools(parentTools, true, child, taskpkg.NewStore())
	got := runChildCodemode(t, child, tools, `text(load("value") === undefined); store("value", "child");`)
	if body := childCodemodeBody(t, got); body != "true" {
		t.Fatalf("fork inherited parent store: %q", body)
	}
	got = runChildCodemode(t, parent, parentTools, `text(load("value"));`)
	if body := childCodemodeBody(t, got); body != "parent" {
		t.Fatalf("child changed parent store: %q", body)
	}
	reopened, err := session.Open(child.Path(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	reopened.AttachFileHistory(agentDir)
	tools = childTools(parentTools, true, reopened, taskpkg.NewStore())
	got = runChildCodemode(t, reopened, tools, `text(load("value"));`)
	if body := childCodemodeBody(t, got); body != "child" {
		t.Fatalf("continuation lost child store: %q", body)
	}
}

func TestCodemodeReadOnlyChildUsesFinalToolSet(t *testing.T) {
	for _, agentType := range []string{"explore", "plan"} {
		t.Run(agentType, func(t *testing.T) {
			parent := session.New(t.TempDir(), t.TempDir())
			runner := testRunner(t)
			runner.ParentTools = []llm.Tool{codemode.New(parent), &toolpkg.Read{}, &toolpkg.Write{}, &toolpkg.Edit{}, New(nil)}
			runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{ID: "readonly-" + agentType, AgentType: agentType, Prompt: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Agent.Hooks().CommitMessages([]llm.Message{llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "inspect"}}}}); err != nil {
				t.Fatal(err)
			}
			child, err := session.Open(runtime.OutputFile, runner.ParentCwd)
			if err != nil {
				t.Fatal(err)
			}
			tools := runtime.Agent.Context().Tools
			result := runChildCodemode(t, child, tools, `
const forbidden = ["write", "edit", "agent", "codemode"];
text({visible: ALL_TOOLS.map(t => t.name).sort(), blocked: forbidden.every(n => typeof tools[n] === "undefined"), described: describeTool("read").name});`)
			var got struct {
				Visible   []string `json:"visible"`
				Blocked   bool     `json:"blocked"`
				Described string   `json:"described"`
			}
			if err := json.Unmarshal([]byte(childCodemodeBody(t, result)), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Visible) != 1 || got.Visible[0] != "read" || !got.Blocked || got.Described != "read" {
				t.Fatalf("readonly script capabilities = %+v", got)
			}
		})
	}
}

func TestCodemodeChildWritesTrackOnlyChildTranscript(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	parent, child := session.New(cwd, t.TempDir()), session.New(cwd, t.TempDir())
	parent.AttachFileHistory(agentDir)
	child.AttachFileHistory(agentDir)
	path := filepath.Join(cwd, "tracked.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := childTools([]llm.Tool{codemode.New(parent), &toolpkg.Write{History: toolpkg.Tracker(parent.FileHistory())}, &toolpkg.Edit{History: toolpkg.Tracker(parent.FileHistory())}}, true, child, taskpkg.NewStore())
	result := runChildCodemode(t, child, tools, `await tools.write({path:"tracked.txt",content:"written"}); await tools.edit({path:"tracked.txt",edits:[{oldText:"written",newText:"edited"}]}); text("complete");`)
	if body := childCodemodeBody(t, result); body != "complete" {
		t.Fatalf("output = %q", body)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "edited" {
		t.Fatalf("file = %q, want edited", data)
	}
	if snapshots := parent.FileHistory().Snapshots(); len(snapshots) != 0 {
		t.Fatalf("child polluted parent backups: %+v", snapshots)
	}
	snapshots := child.FileHistory().Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("child snapshots = %+v, want one user turn", snapshots)
	}
	if _, ok := snapshots[0].TrackedFileBackups[path]; !ok {
		t.Fatalf("child backup missing %q: %+v", path, snapshots[0])
	}
}
