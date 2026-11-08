package adapter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/provider"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// 会话切换后,新 App 的通知唤醒与后台结束兜底都必须照常工作
func TestNotificationPathSurvivesSessionSwitch(t *testing.T) {
	setupAdapterEnv(t)
	// 通知会启动模型 run，用本地终态错误隔离远端连接和重试时延
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "notification model fixture", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	agentDir := os.Getenv("RIVO_AGENT_DIR")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := fmt.Sprintf(`{"providers":{"fixture":{"baseUrl":%q,"api":"openai-responses","apiKey":"test-key","models":[{"id":"fixture","contextWindow":100000,"maxTokens":1000}]}}}`, server.URL)
	for name, body := range map[string]string{"models.json": models, "settings.json": `{"defaultProvider":"fixture","defaultModel":"fixture"}`} {
		if err := os.WriteFile(filepath.Join(agentDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, first.Cwd(), "")
	r.Attach(first)
	t.Cleanup(func() { r.app.Close() })

	ended := make(chan struct{}, 1)
	r.mu.Lock()
	r.send = func(msg tea.Msg) {
		if end, ok := msg.(tui.BackgroundRunMsg); ok && !end.Running {
			select {
			case ended <- struct{}{}:
			default:
			}
		}
	}
	r.mu.Unlock()

	done := r.request(tui.Request{Kind: "new"})().(tui.OperationDoneMsg)
	if done.Err != nil {
		t.Fatalf("new session failed: %v", done.Err)
	}
	second := r.app
	if second == first {
		t.Fatal("session did not switch to a new app")
	}

	// 在新会话里投递一条终态通知,后台 run 结束后必须发出忙碌态复位
	store := second.SubagentStore()
	if store == nil {
		t.Fatal("subagent store is unavailable")
	}
	taskID, token, err := store.Register(taskpkg.RegisterSpec{Type: taskpkg.TypeAgent, Description: "job", AgentType: "explore"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.CompleteAgent(taskID, token, &taskpkg.AgentResult{AgentID: taskID}) {
		t.Fatal("task did not transition to completed")
	}
	if !store.EnqueueNotification(taskID, token, taskpkg.Notification{TaskID: taskID, Status: taskpkg.StatusCompleted, Summary: "job"}) {
		t.Fatal("notification was not enqueued")
	}

	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("no background end message after session switch")
	}
}
