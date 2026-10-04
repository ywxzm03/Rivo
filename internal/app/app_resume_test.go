package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
	"github.com/ywxzm03/Rivo/internal/session"
)

// newSessionFileWithModel 在 dir 下创建一个已落盘、带模型记录的会话，返回文件路径
func newSessionFileWithModel(t *testing.T, dir, provider, modelID, userText string) string {
	t.Helper()
	m := session.New("/tmp/proj", dir)
	if err := m.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: userText}}}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	if err := m.AppendModelChange(provider, modelID); err != nil {
		t.Fatalf("AppendModelChange() error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir(%s) = %v, %d entries, want one session file", dir, err, 1)
	}
	return filepath.Join(dir, entries[0].Name())
}

func TestOpenRestoresProjectionAndModel(t *testing.T) {
	dir := t.TempDir()
	path := newSessionFileWithModel(t, dir, openai.ProviderID, openai.DefaultModel, "hello resume")

	a, err := Open(path, configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if a.ModelFallbackMessage() != "" {
		t.Errorf("ModelFallbackMessage() = %q, want empty", a.ModelFallbackMessage())
	}
	// 1.投影完整重建：消息与模型记录都来自会话文件
	ctx := a.session.BuildContext()
	if len(ctx.Messages) != 1 {
		t.Fatalf("projection messages = %d, want 1", len(ctx.Messages))
	}
	if ctx.Provider != openai.ProviderID || ctx.Model != openai.DefaultModel {
		t.Errorf("projection model = %s/%s, want %s/%s", ctx.Provider, ctx.Model, openai.ProviderID, openai.DefaultModel)
	}
	// 2.运行链按投影模型装配：agent 模型即投影模型
	if a.agent.AgentConfig.Model.ID != openai.DefaultModel {
		t.Errorf("agent model = %q, want %q", a.agent.AgentConfig.Model.ID, openai.DefaultModel)
	}
	// 2.投影消息已注入 Agent 内存上下文，恢复的会话真正接上历史
	if len(a.agent.AgentContext.Messages) != 1 {
		t.Fatalf("agent messages = %d, want 1", len(a.agent.AgentContext.Messages))
	}
	if u, ok := a.agent.AgentContext.Messages[0].(llm.UserMessage); !ok {
		t.Errorf("agent message[0] type = %T, want UserMessage", a.agent.AgentContext.Messages[0])
	} else {
		if text, ok := u.Content[0].(llm.TextContent); !ok || text.Text != "hello resume" {
			t.Errorf("agent message[0] = %+v, want the restored user message", u)
		}
	}
	// 3.恢复后的会话即时落盘，继续追加写入同一文件
	if err := a.session.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "after resume"}}}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if !strings.Contains(string(data), "after resume") {
		t.Errorf("session file does not contain the appended message; writes went elsewhere")
	}
}

func TestOpenFallsBackWhenModelUnknown(t *testing.T) {
	dir := t.TempDir()
	path := newSessionFileWithModel(t, dir, openai.ProviderID, "no-such-model", "hello")

	a, err := Open(path, configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// 1.回退到 env 默认模型并记录 pi 式警告
	want := "Could not restore model " + openai.ProviderID + "/no-such-model. Using " + deepseek.ProviderID + "/" + deepseek.DefaultModel
	if a.ModelFallbackMessage() != want {
		t.Errorf("ModelFallbackMessage() = %q, want %q", a.ModelFallbackMessage(), want)
	}
	if a.agent.AgentConfig.Model.ID != deepseek.DefaultModel {
		t.Errorf("agent model = %q, want fallback %q", a.agent.AgentConfig.Model.ID, deepseek.DefaultModel)
	}
	// 2.模型回退不影响历史上下文注入
	if len(a.agent.AgentContext.Messages) != 1 {
		t.Fatalf("agent messages = %d, want 1", len(a.agent.AgentContext.Messages))
	}
	// 3.会话内容不受模型回退影响
	if ctx := a.session.BuildContext(); len(ctx.Messages) != 1 {
		t.Fatalf("projection messages = %d, want 1", len(ctx.Messages))
	}
}

func TestOpenFallsBackWhenProviderUnknown(t *testing.T) {
	dir := t.TempDir()
	path := newSessionFileWithModel(t, dir, "no-such-provider", "some-model", "hello")

	a, err := Open(path, configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !strings.Contains(a.ModelFallbackMessage(), "Could not restore model no-such-provider/some-model") {
		t.Errorf("ModelFallbackMessage() = %q, want unknown provider warning", a.ModelFallbackMessage())
	}
}

func TestOpenWithoutModelRecordUsesEnvConfig(t *testing.T) {
	dir := t.TempDir()
	path := newSessionFileWithModel(t, dir, "", "", "hello")

	a, err := Open(path, configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if a.ModelFallbackMessage() != "" {
		t.Errorf("ModelFallbackMessage() = %q, want empty", a.ModelFallbackMessage())
	}
	if a.agent.AgentConfig.Model.ID != deepseek.DefaultModel {
		t.Errorf("agent model = %q, want env default %q", a.agent.AgentConfig.Model.ID, deepseek.DefaultModel)
	}
	// 无模型记录的会话（仅有 user 消息）历史同样注入
	if len(a.agent.AgentContext.Messages) != 1 {
		t.Fatalf("agent messages = %d, want 1", len(a.agent.AgentContext.Messages))
	}
}

func TestOpenFallsBackToDefaultModelWhenEnvModelEmpty(t *testing.T) {
	dir := t.TempDir()
	path := newSessionFileWithModel(t, dir, openai.ProviderID, "no-such-model", "hello")

	// env 未配 MODEL 时回退装配用厂商默认模型，而不是拿空 modelID 去匹配失败
	a, err := Open(path, configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if a.agent.AgentConfig.Model.ID != deepseek.DefaultModel {
		t.Errorf("agent model = %q, want default %q", a.agent.AgentConfig.Model.ID, deepseek.DefaultModel)
	}
	if !strings.Contains(a.ModelFallbackMessage(), "Using "+deepseek.ProviderID+"/"+deepseek.DefaultModel) {
		t.Errorf("ModelFallbackMessage() = %q, want it to name the default model", a.ModelFallbackMessage())
	}
}

func TestOpenErrorsWhenFallbackModelUnknown(t *testing.T) {
	dir := t.TempDir()
	path := newSessionFileWithModel(t, dir, openai.ProviderID, "no-such-model", "hello")

	// env 默认模型也解析不到时按启动失败处理
	_, err := Open(path, configWithBuiltin(deepseek.ProviderID, "also-missing"), nil)
	if err == nil {
		t.Fatal("Open() error = nil, want unknown model error")
	}
	if !strings.Contains(err.Error(), "also-missing") {
		t.Errorf("Open() error = %q, want it to name the fallback model", err)
	}
}

func TestOpenErrorsOnMissingFile(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "not-exist.jsonl"), Config{}, nil)
	if err == nil {
		t.Fatal("Open() error = nil, want read error")
	}
}
