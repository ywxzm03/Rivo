//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/test/e2e/support"
)

const scriptedProvider = "scripted"
const scriptedModel = "scripted-model"

// chat 旅程：启动二进制 → 提问 → 剧本返回文本 → 屏幕出现该文本 → 退出码 0
func TestChatFlow(t *testing.T) {
	env := support.NewEnv(t)
	llm := support.NewScriptedLLM(t, support.Reply{Text: "pong from the scripted model"})
	env.WriteModels(t, llm.BaseURL(), scriptedProvider, scriptedModel)

	process := support.StartBinary(t, env, "--print", "ping")
	process.WaitFor(t, "pong from the scripted model", 30*time.Second)
	if code := process.Wait(t, 30*time.Second); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	requests := llm.Requests()
	if len(requests) == 0 {
		t.Fatal("the scripted model received no request")
	}
	// 用户输入必须真的进入请求上下文
	if !containsUserText(requests[0], "ping") {
		t.Errorf("first request does not carry the user prompt: %v", requests[0])
	}
}

// session 旅程：跑一轮 → 退出 → --continue 重启 → 恢复上一轮内容
func TestSessionFlow(t *testing.T) {
	env := support.NewEnv(t)
	llm := support.NewScriptedLLM(t, support.Reply{Text: "first answer"}, support.Reply{Text: "second answer"})
	env.WriteModels(t, llm.BaseURL(), scriptedProvider, scriptedModel)

	first := support.StartBinary(t, env, "--print", "remember me")
	first.WaitFor(t, "first answer", 30*time.Second)
	if code := first.Wait(t, 30*time.Second); code != 0 {
		t.Fatalf("first run exit code = %d, want 0", code)
	}

	second := support.StartBinary(t, env, "--continue", "--print", "what did I say")
	second.WaitFor(t, "second answer", 30*time.Second)
	if code := second.Wait(t, 30*time.Second); code != 0 {
		t.Fatalf("second run exit code = %d, want 0", code)
	}

	// 第二轮请求必须带着上一轮的用户输入，证明会话被恢复
	requests := llm.Requests()
	if len(requests) < 2 {
		t.Fatalf("requests = %d, want at least 2", len(requests))
	}
	if !containsUserText(requests[len(requests)-1], "remember me") {
		t.Errorf("continued request lost the previous turn: %v", requests[len(requests)-1])
	}
}

// subagent 旅程：主 agent 发起工具调用 → 子 agent 执行 → 主界面出现结果
func TestSubagentFlow(t *testing.T) {
	env := support.NewEnv(t)
	llm := support.NewScriptedLLM(t,
		support.Reply{ToolCall: &support.ToolCall{
			ID:        "call-1",
			Name:      "agent",
			Arguments: map[string]any{"description": "inspect", "prompt": "say hi", "subagent_type": "explore"},
		}},
		support.Reply{Text: "subagent reported back"},
		support.Reply{Text: "final answer with the subagent result"},
	)
	env.WriteModels(t, llm.BaseURL(), scriptedProvider, scriptedModel)

	process := support.StartBinary(t, env, "--print", "delegate this")
	process.WaitFor(t, "final answer", 45*time.Second)
	if code := process.Wait(t, 45*time.Second); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// containsUserText 在请求体的 input 里查找用户文本
func containsUserText(request map[string]any, want string) bool {
	input, ok := request["input"]
	if !ok {
		return false
	}
	return deepContains(input, want)
}

func deepContains(value any, want string) bool {
	switch value := value.(type) {
	case string:
		return strings.Contains(value, want)
	case []any:
		for _, item := range value {
			if deepContains(item, want) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if deepContains(item, want) {
				return true
			}
		}
	}
	return false
}
