package llm

import (
	"context"
	"testing"
)

type fakeProvider struct{}

func (fakeProvider) ID() string      { return "fake" }
func (fakeProvider) Models() []Model { return []Model{{ID: "m1", Provider: "fake", API: "fake-api"}} }

func (fakeProvider) Stream(ctx context.Context, model Model, c Context, opts Options) (<-chan Event, error) {
	ch := make(chan Event, 1)
	ch <- Event{Type: EventDone, Message: &AssistantMessage{StopReason: StopReasonStop}}
	close(ch)
	return ch, nil
}

func TestGetModel(t *testing.T) {
	m := NewModels()
	m.Register(fakeProvider{})

	if _, err := m.GetModel("fake", "m2"); err == nil {
		t.Error("期望不存在的模型报错")
	}
	if _, err := m.GetModel("nope", "m1"); err == nil {
		t.Error("期望未注册的厂商报错")
	}
	model, err := m.GetModel("fake", "m1")
	if err != nil || model.ID != "m1" {
		t.Errorf("GetModel = %+v, %v", model, err)
	}
}

func TestStreamFn(t *testing.T) {
	m := NewModels()
	m.Register(fakeProvider{})

	if _, err := m.StreamFn("fake", "m2"); err == nil {
		t.Error("期望不存在的模型报错")
	}
	if _, err := m.StreamFn("nope", "m1"); err == nil {
		t.Error("期望未注册的厂商报错")
	}

	fn, err := m.StreamFn("fake", "m1")
	if err != nil {
		t.Fatal(err)
	}
	events, err := fn(context.Background(), Model{ID: "m1"}, Context{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []Event
	for ev := range events {
		got = append(got, ev)
	}
	if len(got) != 1 || got[0].Type != EventDone {
		t.Errorf("got %+v, want 单个 done 事件", got)
	}
}
