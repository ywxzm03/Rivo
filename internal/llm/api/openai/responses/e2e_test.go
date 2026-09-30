//go:build e2e

package responses_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

type e2eProvider interface {
	Models() []llm.Model
	Stream(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.Event, error)
}

func TestReasoningE2E(t *testing.T) {
	tests := []struct {
		name         string
		keyEnv       string
		modelEnv     string
		defaultModel string
		newProvider  func(string) e2eProvider
	}{
		{
			name:         "DeepSeek",
			keyEnv:       "RIVO_E2E_DEEPSEEK_API_KEY",
			modelEnv:     "RIVO_E2E_DEEPSEEK_MODEL",
			defaultModel: deepseek.DefaultModel,
			newProvider: func(key string) e2eProvider {
				return deepseek.New(deepseek.Config{APIKey: key})
			},
		},
		{
			name:         "OpenAI",
			keyEnv:       "RIVO_E2E_OPENAI_API_KEY",
			modelEnv:     "RIVO_E2E_OPENAI_MODEL",
			defaultModel: openai.DefaultModel,
			newProvider: func(key string) e2eProvider {
				return openai.New(openai.Config{APIKey: key})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := os.Getenv(tt.keyEnv)
			if key == "" {
				t.Skipf("set %s to run this live API test", tt.keyEnv)
			}

			modelID := os.Getenv(tt.modelEnv)
			if modelID == "" {
				modelID = tt.defaultModel
			}
			provider := tt.newProvider(key)
			models := provider.Models()
			model, ok := modelByID(models, modelID)
			if !ok {
				model = models[0]
				model.ID = modelID
				model.Name = modelID
			}

			first, firstDeltas := runE2ERound(t, provider, model, llm.Context{
				Messages: []llm.Message{llm.UserMessage{Content: []llm.Content{
					llm.TextContent{Text: "What is 19 multiplied by 23? Give a concise answer."},
				}}},
			})
			if !firstDeltas {
				t.Fatal("first response emitted no non-empty thinking delta")
			}
			if !hasReplayableReasoning(first.Content) {
				t.Fatal("first response contained no replayable reasoning item")
			}
			if !hasNonEmptyText(first.Content) {
				t.Fatal("first response contained no non-empty answer")
			}

			second, _ := runE2ERound(t, provider, model, llm.Context{
				Messages: []llm.Message{
					llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "What is 19 multiplied by 23? Give a concise answer."}}},
					*first,
					llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "Now verify that result and state it."}}},
				},
			})
			if !hasNonEmptyText(second.Content) {
				t.Fatal("second response contained no non-empty answer")
			}
		})
	}
}

func runE2ERound(t *testing.T, provider e2eProvider, model llm.Model, input llm.Context) (*llm.AssistantMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	events, err := provider.Stream(ctx, model, input, llm.Options{})
	if err != nil {
		t.Fatalf("start response stream: %v", err)
	}
	var message *llm.AssistantMessage
	var hasThinkingDelta bool
	for event := range events {
		switch event.Type {
		case llm.EventThinkingDelta:
			hasThinkingDelta = hasThinkingDelta || event.Delta != ""
		case llm.EventError:
			t.Fatalf("response stream failed: %v", event.Err)
		case llm.EventDone:
			message = event.Message
		}
	}
	if message == nil {
		t.Fatal("response stream ended without an assistant result")
	}
	return message, hasThinkingDelta
}

func modelByID(models []llm.Model, id string) (llm.Model, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return llm.Model{}, false
}

func hasNonEmptyText(content []llm.Content) bool {
	for _, item := range content {
		if text, ok := item.(llm.TextContent); ok && text.Text != "" {
			return true
		}
	}
	return false
}

func hasReplayableReasoning(content []llm.Content) bool {
	for _, item := range content {
		if thinking, ok := item.(llm.ThinkingContent); ok && thinking.ThinkingSignature != "" {
			return true
		}
	}
	return false
}
