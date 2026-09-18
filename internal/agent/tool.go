package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func DemoTools() []llm.Tool {
	emptyObject := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	return []llm.Tool{
		{Name: "hello_world", Description: "Print Hello World", Parameters: emptyObject},
		{Name: "ping", Description: "Return pong", Parameters: emptyObject},
		{Name: "now", Description: "Return the current time", Parameters: emptyObject},
	}
}

func DemoExecute(_ context.Context, name string, _ map[string]any) (string, error) {
	switch name {
	case "hello_world":
		fmt.Println("Hello World")
		return "Hello World", nil
	case "ping":
		return "pong", nil
	case "now":
		return time.Now().Format(time.RFC3339), nil
	default:
		return "unknown tool", nil
	}
}
