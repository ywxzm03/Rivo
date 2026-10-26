package task

import (
	"errors"
	"testing"
)

// Claude 的后台任务进度在运行中可见,失败终态保留错误供父 agent 和详情视图读取
func TestClaudeContractProgressAndFailureRemainObservable(t *testing.T) {
	store := NewStore()
	id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "failing work", AgentType: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.UpdateAgentProgress(id, token, func(progress *AgentProgress) {
		progress.ToolUseCount = 2
		progress.TokenCount = 17
		progress.Summary = "reading files"
	}) {
		t.Fatal("progress update was rejected")
	}
	state, ok := store.Task(id)
	if !ok {
		t.Fatal("task disappeared")
	}
	agentState := state.(*AgentState)
	if agentState.Progress == nil || agentState.Progress.ToolUseCount != 2 || agentState.Progress.TokenCount != 17 || agentState.Progress.Summary != "reading files" {
		t.Fatalf("progress = %#v", agentState.Progress)
	}

	cause := errors.New("provider unavailable")
	if !store.FailAgent(id, token, cause) {
		t.Fatal("failure transition was rejected")
	}
	state, ok = store.Task(id)
	if !ok || state.Base().Status != StatusFailed {
		t.Fatalf("failed state = %#v", state)
	}
	if got := state.(*AgentState).Error; got != cause.Error() {
		t.Fatalf("failure error = %q, want %q", got, cause)
	}
	if store.UpdateAgentProgress(id, token, func(progress *AgentProgress) { progress.ToolUseCount++ }) {
		t.Fatal("progress update changed a terminal task")
	}
}
