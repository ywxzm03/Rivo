// Package support 提供进程级 E2E 的共享设施：脚本化 LLM、PTY 驱动、
// 隔离环境与二进制构建。只被 test/e2e 使用
package support

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ScriptedLLM 是按请求顺序返回剧本的假模型服务。
// 它用 SSE 模拟 OpenAI 协议，让二进制走真实的网络与流式解析路径
type ScriptedLLM struct {
	server *httptest.Server

	mu       sync.Mutex
	script   []Reply
	requests []map[string]any
}

// Reply 是一次模型回复的剧本
type Reply struct {
	// Text 是回复正文
	Text string
	// ToolCall 非空时回复一次工具调用而不是文本
	ToolCall *ToolCall
}

// ToolCall 描述剧本里的一次工具调用
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// NewScriptedLLM 启动假模型服务，测试结束自动关闭
func NewScriptedLLM(t *testing.T, replies ...Reply) *ScriptedLLM {
	t.Helper()
	llm := &ScriptedLLM{script: replies}
	llm.server = httptest.NewServer(http.HandlerFunc(llm.handle))
	t.Cleanup(llm.server.Close)
	return llm
}

// BaseURL 返回可写进 models.json 的地址
func (l *ScriptedLLM) BaseURL() string { return l.server.URL + "/v1" }

// Requests 返回已收到的请求体，供断言模型看到了什么上下文
func (l *ScriptedLLM) Requests() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]map[string]any, len(l.requests))
	copy(out, l.requests)
	return out
}

func (l *ScriptedLLM) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	l.mu.Lock()
	l.requests = append(l.requests, body)
	index := len(l.requests) - 1
	var reply Reply
	if index < len(l.script) {
		reply = l.script[index]
	} else if len(l.script) > 0 {
		reply = l.script[len(l.script)-1]
	}
	l.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	write := func(event string, payload map[string]any) {
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	if reply.ToolCall != nil {
		write("response.output_item.added", map[string]any{
			"type": "response.output_item.added",
			"item": map[string]any{
				"type": "function_call", "id": reply.ToolCall.ID,
				"call_id": reply.ToolCall.ID, "name": reply.ToolCall.Name,
				"arguments": mustJSON(reply.ToolCall.Arguments),
			},
		})
		write("response.completed", map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id": "resp-tool", "status": "completed",
				"output": []any{map[string]any{
					"type": "function_call", "id": reply.ToolCall.ID,
					"call_id": reply.ToolCall.ID, "name": reply.ToolCall.Name,
					"arguments": mustJSON(reply.ToolCall.Arguments),
				}},
			},
		})
		return
	}

	write("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "delta": reply.Text,
	})
	write("response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp-text", "status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": reply.Text}},
			}},
		},
	})
}

func mustJSON(value map[string]any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
