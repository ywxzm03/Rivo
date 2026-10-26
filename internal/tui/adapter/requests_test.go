package adapter

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// 每个会启动 operation 的请求类型都必须映射到非空领域操作，漏映射会静默变成空操作
func TestEveryOperationRequestMapsToDomainKind(t *testing.T) {
	kinds := []tui.RequestKind{
		tui.RequestRun, tui.RequestCompact, tui.RequestModel, tui.RequestThinking,
		tui.RequestResume, tui.RequestRename, tui.RequestNew, tui.RequestReload,
		tui.RequestTree, tui.RequestClear, tui.RequestDelete,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			if got := operationKind(kind); got == "" {
				t.Fatalf("operationKind(%q) = empty, want a domain operation", kind)
			}
		})
	}
}

// 只入队的请求类型不能携带领域操作，否则会被误当成 operation 启动
func TestQueueOnlyRequestsCarryNoOperation(t *testing.T) {
	for _, kind := range []tui.RequestKind{tui.RequestSteer, tui.RequestFollowUp} {
		if !requestSpecs[kind].queueOnly {
			t.Errorf("%q must be queue-only", kind)
		}
		if requestSpecs[kind].operation != "" {
			t.Errorf("%q carries operation %q, want none", kind, requestSpecs[kind].operation)
		}
	}
}

// 恢复方式必须映射到对应的领域模式，未知值退回 conversation 语义
func TestRestoreModeMapping(t *testing.T) {
	tests := []struct {
		kind tui.RestoreKind
		want string
	}{
		{tui.RestoreContext, "context"},
		{tui.RestoreCode, "code"},
		{tui.RestoreCodeAndContext, "code_and_context"},
		{tui.RestoreKind("unknown"), "context"},
	}
	for _, tt := range tests {
		if got := string(restoreMode(tt.kind)); got != tt.want {
			t.Errorf("restoreMode(%q) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

// 编辑器载荷与领域消息必须能双向转换，图片按 base64 原样保留
func TestPayloadRoundTrip(t *testing.T) {
	payload := &tui.ComposerPayload{
		Text:   "describe ",
		Images: []tui.ImagePart{{Data: "aGk=", MIMEType: "image/png"}},
	}
	message := payloadMessage(payload)
	if message == nil || len(message.Content) != 2 {
		t.Fatalf("payloadMessage() = %+v, want text and image", message)
	}
	back := payloadFromMessage(message)
	if back == nil || back.Text != payload.Text || len(back.Images) != 1 || back.Images[0].Data != "aGk=" {
		t.Fatalf("payloadFromMessage() = %+v, want round trip", back)
	}
	if payloadMessage(nil) != nil || payloadFromMessage(nil) != nil {
		t.Error("nil payload must convert to nil, and back")
	}
	if payloadMessage(&tui.ComposerPayload{}) != nil {
		t.Error("empty payload must convert to nil message")
	}
}

// 领域操作类型必须与请求类型同名，避免映射表写错位置
func TestOperationKindsMatchRequestNames(t *testing.T) {
	if string(operationKind(tui.RequestRun)) != string(app.OperationRun) {
		t.Errorf("run maps to %q, want %q", operationKind(tui.RequestRun), app.OperationRun)
	}
	if string(operationKind(tui.RequestTree)) != string(app.OperationTree) {
		t.Errorf("tree maps to %q, want %q", operationKind(tui.RequestTree), app.OperationTree)
	}
}
