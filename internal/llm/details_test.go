package llm

import (
	"encoding/json"
	"testing"
)

type detailsAlpha struct {
	TaskID string `json:"task_id"`
}

func (detailsAlpha) ToolDetails() {}

type detailsBeta struct {
	TaskID string `json:"task_id"`
}

func (detailsBeta) ToolDetails() {}

// 同名标志字段只能还原到注册它的那个工具,不能跨工具串型
func TestDecodeToolDetailsRespectsToolBoundary(t *testing.T) {
	RegisterToolDetails[detailsAlpha]("test-alpha", "task_id")
	RegisterToolDetails[detailsBeta]("test-beta", "task_id")
	raw := json.RawMessage(`{"task_id":"agent-1"}`)

	alpha, err := DecodeToolDetails("test-alpha", raw)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := alpha.(detailsAlpha); !ok || value.TaskID != "agent-1" {
		t.Fatalf("alpha details = %#v", alpha)
	}
	beta, err := DecodeToolDetails("test-beta", raw)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := beta.(detailsBeta); !ok || value.TaskID != "agent-1" {
		t.Fatalf("beta details = %#v", beta)
	}
	// 未登记的工具、空详情、无匹配标志字段都按无详情处理
	for _, probe := range []struct {
		tool string
		raw  string
	}{
		{tool: "test-unknown", raw: `{"task_id":"agent-1"}`},
		{tool: "test-alpha", raw: ``},
		{tool: "test-alpha", raw: `{"other":"x"}`},
		{tool: "test-alpha", raw: `null`},
	} {
		details, err := DecodeToolDetails(probe.tool, json.RawMessage(probe.raw))
		if err != nil {
			t.Fatalf("%s/%s: %v", probe.tool, probe.raw, err)
		}
		if details != nil {
			t.Fatalf("%s/%s decoded to %#v, want nil", probe.tool, probe.raw, details)
		}
	}
	// 匹配到类型后字段类型非法仍然报错,不能静默降级
	if _, err := DecodeToolDetails("test-alpha", json.RawMessage(`{"task_id":42}`)); err == nil {
		t.Fatal("invalid field type was accepted")
	}
}
