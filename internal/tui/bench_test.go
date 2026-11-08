package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// benchSnapshot 构造新建会话与恢复会话两种首帧快照
func benchSnapshot(messages int) Snapshot {
	snapshot := Snapshot{
		Cwd:             "/tmp/project",
		Branch:          "main",
		ModelKey:        "bench/bench-model",
		DefaultModelKey: "bench/bench-model",
		CurrentModel:    ModelOption{ID: "bench-model", Provider: "bench", Name: "Bench Model", ContextWindow: 100000, Levels: []string{"off"}},
		Models:          []ModelOption{{ID: "bench-model", Provider: "bench", Name: "Bench Model", ContextWindow: 100000, Levels: []string{"off"}}},
		Thinking:        "off",
	}
	for i := range messages {
		snapshot.Messages = append(snapshot.Messages, Message{
			ID:   fmt.Sprintf("m-%d", i),
			Role: "assistant",
			Text: strings.Repeat("response text ", 40),
		})
	}
	return snapshot
}

func BenchmarkNewModel(b *testing.B) {
	snapshot := benchSnapshot(0)
	b.ResetTimer()
	for b.Loop() {
		model := New(snapshot, Handlers{})
		model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		_ = model.View()
	}
}

func BenchmarkNewModelResume(b *testing.B) {
	snapshot := benchSnapshot(200)
	b.ResetTimer()
	for b.Loop() {
		model := New(snapshot, Handlers{})
		model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		_ = model.View()
	}
}
