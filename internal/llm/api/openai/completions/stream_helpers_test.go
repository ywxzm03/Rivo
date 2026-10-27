package completions

import (
	"context"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// rawInt 必须接受三种数值类型，其余类型与缺失键返回 0
func TestRawInt(t *testing.T) {
	values := map[string]any{
		"float":  float64(3),
		"int":    4,
		"int64":  int64(5),
		"string": "6",
	}
	if got := rawInt(values, "float"); got != 3 {
		t.Errorf("rawInt(float) = %d, want 3", got)
	}
	if got := rawInt(values, "int"); got != 4 {
		t.Errorf("rawInt(int) = %d, want 4", got)
	}
	if got := rawInt(values, "int64"); got != 5 {
		t.Errorf("rawInt(int64) = %d, want 5", got)
	}
	if got := rawInt(values, "string"); got != 0 {
		t.Errorf("rawInt(string) = %d, want 0", got)
	}
	if got := rawInt(values, "missing"); got != 0 {
		t.Errorf("rawInt(missing) = %d, want 0", got)
	}
}

// 停止原因映射必须覆盖协议里的每一种取值
func TestMapStopReason(t *testing.T) {
	tests := []struct {
		reason string
		want   llm.StopReason
	}{
		{"tool_calls", llm.StopReasonToolUse},
		{"function_call", llm.StopReasonToolUse},
		{"length", llm.StopReasonLength},
		{"content_filter", llm.StopReasonError},
		{"stop", llm.StopReasonStop},
		{"", llm.StopReasonStop},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := mapStopReason(tt.reason); got != tt.want {
				t.Errorf("mapStopReason(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}

// 上下文取消与超时都算 aborted，其余算 error
func TestClassifyReason(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := llm.StopReasonForError(canceled, nil); got != llm.StopReasonAborted {
		t.Errorf("llm.StopReasonForError(canceled, nil) = %q, want aborted", got)
	}

	expired, cancel2 := context.WithTimeout(context.Background(), 0)
	defer cancel2()
	<-expired.Done()
	if got := llm.StopReasonForError(expired, nil); got != llm.StopReasonAborted {
		t.Errorf("classifyReason(deadline) = %q, want aborted", got)
	}

	if got := llm.StopReasonForError(context.Background(), nil); got != llm.StopReasonError {
		t.Errorf("classifyReason(live) = %q, want error", got)
	}
}

// 空参数串是合法空对象，坏 JSON 必须报错而不是静默成空 map
func TestDecodeArguments(t *testing.T) {
	if got, err := decodeArguments(""); err != nil || len(got) != 0 {
		t.Errorf("decodeArguments(empty) = %v, %v; want empty map and no error", got, err)
	}
	if got, err := decodeArguments(`{"path":"a.go"}`); err != nil || got["path"] != "a.go" {
		t.Errorf("decodeArguments(json) = %v, %v; want the parsed path", got, err)
	}
	if _, err := decodeArguments("not json"); err == nil {
		t.Error("decodeArguments(bad json) returned no error")
	}
}

// 空 choices 与缺失 delta 都返回空串
func TestRawDeltaString(t *testing.T) {
	if got := rawDeltaString(map[string]any{}, "content"); got != "" {
		t.Errorf("rawDeltaString(no choices) = %q, want empty", got)
	}
	raw := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "hi"}}}}
	if got := rawDeltaString(raw, "content"); got != "hi" {
		t.Errorf("rawDeltaString(content) = %q, want hi", got)
	}
	if got := rawDeltaString(raw, "reasoning"); got != "" {
		t.Errorf("rawDeltaString(missing field) = %q, want empty", got)
	}
}
