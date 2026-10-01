//go:build e2e

package responses_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/joho/godotenv"
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
		baseURLEnv   string
		defaultModel string
		newProvider  func(string, string) e2eProvider
	}{
		{
			name:         "DeepSeek",
			keyEnv:       "RIVO_E2E_DEEPSEEK_API_KEY",
			modelEnv:     "RIVO_E2E_DEEPSEEK_MODEL",
			baseURLEnv:   "RIVO_E2E_DEEPSEEK_BASE_URL",
			defaultModel: deepseek.DefaultModel,
			newProvider: func(key, baseURL string) e2eProvider {
				return deepseek.New(deepseek.Config{APIKey: key, BaseURL: baseURL})
			},
		},
		{
			name:         "OpenAI",
			keyEnv:       "RIVO_E2E_OPENAI_API_KEY",
			modelEnv:     "RIVO_E2E_OPENAI_MODEL",
			baseURLEnv:   "RIVO_E2E_OPENAI_BASE_URL",
			defaultModel: openai.DefaultModel,
			newProvider: func(key, baseURL string) e2eProvider {
				return openai.New(openai.Config{APIKey: key, BaseURL: baseURL})
			},
		},
	}

	if err := loadE2EEnv(); err != nil && !os.IsNotExist(err) {
		t.Fatalf("load repository .env: %v", err)
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
			provider := tt.newProvider(key, os.Getenv(tt.baseURLEnv))
			models := provider.Models()
			model, ok := modelByID(models, modelID)
			if !ok {
				model = models[0]
				model.ID = modelID
				model.Name = modelID
			}

			tool := e2eDoubleTool{}
			userMessage := llm.UserMessage{Content: []llm.Content{
				llm.TextContent{Text: "Use the double_number tool to double 21."},
			}}
			first, err := runE2ERound(t, provider, model, llm.Context{
				SystemPrompt: "You are a helpful assistant. Use the requested tool, then answer concisely.",
				Messages:     []llm.Message{userMessage},
				Tools:        []llm.Tool{tool},
			})
			if err != nil {
				t.Fatalf("first response: %v", err)
			}
			if !hasReplayableReasoning(first.Content) {
				t.Fatal("first response contained no replayable reasoning item")
			}

			messages := []llm.Message{userMessage, *first}
			if call, ok := findToolCall(first.Content); ok {
				messages = append(messages, llm.ToolResultMessage{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					Content:    []llm.Content{llm.TextContent{Text: "42"}},
				})
			}
			messages = append(messages, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "Say hello to confirm you can continue."}}})
			second, err := runE2ERound(t, provider, model, llm.Context{
				SystemPrompt: "You are a helpful assistant.",
				Messages:     messages,
				Tools:        []llm.Tool{tool},
			})
			if err != nil {
				t.Fatalf("second response: %v", err)
			}
			if !hasNonEmptyText(second.Content) {
				t.Fatal("second response contained no non-empty answer")
			}
		})
	}
}

func loadE2EEnv() error {
	_, source, _, _ := runtime.Caller(0)
	envPath := filepath.Join(filepath.Dir(source), "../../../../../.env")
	return godotenv.Load(envPath)
}

func runE2ERound(t *testing.T, provider e2eProvider, model llm.Model, input llm.Context) (*llm.AssistantMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	events, err := provider.Stream(ctx, model, input, llm.Options{})
	if err != nil {
		return nil, err
	}
	var message *llm.AssistantMessage
	var streamErr error
	for event := range events {
		switch event.Type {
		case llm.EventError:
			streamErr = event.Err
		case llm.EventDone:
			message = event.Message
		}
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if message == nil {
		return nil, errors.New("response stream ended without an assistant result")
	}
	return message, nil
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

func findToolCall(content []llm.Content) (llm.ToolCall, bool) {
	for _, item := range content {
		if call, ok := item.(llm.ToolCall); ok {
			return call, true
		}
	}
	return llm.ToolCall{}, false
}

type e2eDoubleTool struct{}

func (e2eDoubleTool) Name() string        { return "double_number" }
func (e2eDoubleTool) Description() string { return "Doubles a number and returns the result" }
func (e2eDoubleTool) ExecutionMode() llm.ToolExecutionMode {
	return llm.ToolExecutionSequential
}
func (e2eDoubleTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "number", "description": "A number to double"},
		},
		"required":             []string{"value"},
		"additionalProperties": false,
	}
}
func (e2eDoubleTool) Execute(context.Context, map[string]any) (llm.ToolResult, error) {
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "42"}}}, nil
}
