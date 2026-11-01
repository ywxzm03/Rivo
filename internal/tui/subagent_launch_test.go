package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// 发起子任务的结果句要说清"在后台运行",不能只回显内部 ID
func TestAgentToolRowLaunchFeedback(t *testing.T) {
	call := ToolCallView{
		Name:         "agent",
		Arguments:    `{"description":"Run sleep 120","subagent_type":"explore"}`,
		Result:       "Started subagent agent-123",
		Details:      `{"status":"async_launched","task_id":"agent-123","description":"Run sleep 120","subagentType":"explore","state":"running","outputFile":"/tmp/x.jsonl"}`,
		Status:       ToolSuccess,
		ArgsComplete: true,
	}
	rendered := ansi.Strip(strings.Join(renderToolRow(call, 100, false, time.Now()), "\n"))
	if strings.Contains(rendered, "agent-123") {
		t.Fatalf("internal id leaked into the row:\n%s", rendered)
	}
	if !strings.Contains(rendered, "background") {
		t.Fatalf("launch feedback does not say it runs in the background:\n%s", rendered)
	}
}

// fork 与 typed 都要给出同一类反馈,但 fork 要能看出继承了对话
func TestAgentToolRowForkLaunchFeedback(t *testing.T) {
	call := ToolCallView{
		Name:         "agent",
		Arguments:    `{"description":"Scan callers","prompt":"find callers"}`,
		Result:       "Started subagent agent-9",
		Details:      `{"status":"async_launched","task_id":"agent-9","description":"Scan callers","subagentType":"fork","state":"running","outputFile":"/tmp/y.jsonl"}`,
		Status:       ToolSuccess,
		ArgsComplete: true,
	}
	rendered := ansi.Strip(strings.Join(renderToolRow(call, 100, false, time.Now()), "\n"))
	if strings.Contains(rendered, "agent-9") {
		t.Fatalf("internal id leaked into the row:\n%s", rendered)
	}
	if !strings.Contains(rendered, "background") {
		t.Fatalf("fork launch feedback missing background hint:\n%s", rendered)
	}
}
