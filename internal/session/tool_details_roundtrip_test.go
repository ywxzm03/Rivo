package session

import (
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/subagent"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tool"
	"github.com/ywxzm03/Rivo/internal/tool/backgroundbash"
)

// 启动、发消息与停止结果共用 task_id,落盘重开时必须靠工具名还原成各自类型
func TestTaskDetailsRoundtripUsesToolName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-1.jsonl")
	sm, err := NewAt("/project", path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "call-1", Name: "agent"},
			llm.ToolCall{ID: "call-2", Name: "SendMessage"},
			llm.ToolCall{ID: "call-3", Name: "TaskStop"},
		}},
		llm.ToolResultMessage{
			ToolCallID: "call-1", ToolName: "agent",
			Details: taskpkg.AgentLaunched{TaskID: "agent-1", Status: "async_launched"},
		},
		llm.ToolResultMessage{
			ToolCallID: "call-2", ToolName: "SendMessage",
			Details: subagent.SendMessageResult{TaskID: "agent-1", Status: "queued"},
		},
		llm.ToolResultMessage{
			ToolCallID: "call-3", ToolName: "TaskStop",
			Details: taskpkg.StopOutput{TaskID: "agent-1", Type: taskpkg.TypeAgent},
		},
	}
	if _, err := sm.AppendMessages(messages); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithID(path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	results := toolResultsByCallID(reopened.BuildContext().Messages())
	if launched, ok := results["call-1"].(taskpkg.AgentLaunched); !ok || launched.TaskID != "agent-1" {
		t.Fatalf("call-1 details = %#v", results["call-1"])
	}
	if queued, ok := results["call-2"].(subagent.SendMessageResult); !ok || queued.TaskID != "agent-1" {
		t.Fatalf("call-2 details = %#v", results["call-2"])
	}
	if stopped, ok := results["call-3"].(taskpkg.StopOutput); !ok || stopped.TaskID != "agent-1" {
		t.Fatalf("call-3 details = %#v", results["call-3"])
	}
}

// bash 前台截断与后台启动共用工具名,靠各自标志字段区分;powershell 与 edit 也按工具名还原
func TestShellAndEditDetailsRoundtripUsesToolName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-1.jsonl")
	sm, err := NewAt("/project", path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		llm.AssistantMessage{Content: []llm.Content{
			llm.ToolCall{ID: "call-1", Name: "bash"},
			llm.ToolCall{ID: "call-2", Name: "bash"},
			llm.ToolCall{ID: "call-3", Name: "powershell"},
			llm.ToolCall{ID: "call-4", Name: "edit"},
		}},
		llm.ToolResultMessage{
			ToolCallID: "call-1", ToolName: "bash",
			Details: tool.ShellDetails{Truncation: &tool.ShellTruncation{Truncated: true, TruncatedBy: "lines"}},
		},
		llm.ToolResultMessage{
			ToolCallID: "call-2", ToolName: "bash",
			Details: backgroundbash.BackgroundOutput{TaskID: "bash-1", OutputFile: "/tmp/bash-1.log"},
		},
		llm.ToolResultMessage{
			ToolCallID: "call-3", ToolName: "powershell",
			Details: tool.ShellDetails{Truncation: &tool.ShellTruncation{Truncated: true, TruncatedBy: "bytes"}},
		},
		llm.ToolResultMessage{
			ToolCallID: "call-4", ToolName: "edit",
			Details: tool.EditDetails{Diff: "-a\n+b", Patch: "1-1"},
		},
	}
	if _, err := sm.AppendMessages(messages); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithID(path, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	results := toolResultsByCallID(reopened.BuildContext().Messages())
	foreground, ok := results["call-1"].(tool.ShellDetails)
	if !ok || foreground.Truncation == nil || foreground.Truncation.TruncatedBy != "lines" {
		t.Fatalf("call-1 details = %#v", results["call-1"])
	}
	background, ok := results["call-2"].(backgroundbash.BackgroundOutput)
	if !ok || background.TaskID != "bash-1" || background.OutputFile != "/tmp/bash-1.log" {
		t.Fatalf("call-2 details = %#v", results["call-2"])
	}
	powershell, ok := results["call-3"].(tool.ShellDetails)
	if !ok || powershell.Truncation == nil || powershell.Truncation.TruncatedBy != "bytes" {
		t.Fatalf("call-3 details = %#v", results["call-3"])
	}
	edit, ok := results["call-4"].(tool.EditDetails)
	if !ok || edit.Patch != "1-1" || edit.Diff != "-a\n+b" {
		t.Fatalf("call-4 details = %#v", results["call-4"])
	}
}

// toolResultsByCallID 收集按调用 ID 索引的工具详情
func toolResultsByCallID(messages []llm.Message) map[string]llm.ToolDetails {
	results := make(map[string]llm.ToolDetails)
	for _, message := range messages {
		result, ok := message.(llm.ToolResultMessage)
		if !ok {
			continue
		}
		results[result.ToolCallID] = result.Details
	}
	return results
}
