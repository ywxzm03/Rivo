package adapter

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// newSkillRuntime 使用本地 SSE 服务装配真实调用链，不访问远端模型
func newSkillRuntime(t *testing.T, handler http.HandlerFunc) (*Runtime, string, string) {
	t.Helper()
	setupAdapterEnv(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	agentDir := os.Getenv("RIVO_AGENT_DIR")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := fmt.Sprintf(`{"providers":{"test":{"baseUrl":%q,"apiKey":"test-key","models":[{"id":"test-model","contextWindow":100000,"maxTokens":1000}]}}}`, server.URL)
	for name, body := range map[string]string{"models.json": models, "settings.json": `{"defaultProvider":"test","defaultModel":"test-model"}`} {
		if err := os.WriteFile(filepath.Join(agentDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(agentDir, "skills", `docs & "guide"`, "SKILL.md")
	writeManualSkill(t, path, "Original instructions")
	cfg, err := app.Load()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, cwd, "main")
	a, err := app.New(cfg, r.HandleEvent)
	if err != nil {
		t.Fatal(err)
	}
	r.Attach(a)
	dir, err := session.DefaultDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return r, path, dir
}

func writeManualSkill(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: pdf-tools\ndescription: Manual PDF instructions\ndisable-model-invocation: true\n---\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func completeSkillResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"response-1","status":"completed","output":[{"id":"message-1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Finished","annotations":[]}]}]}}`+"\n\n")
}

func TestSkillInvocationExpandsForModelAndRestoresSavedBody(t *testing.T) {
	var calls atomic.Int32
	payloads := make(chan string, 1)
	r, skillPath, dir := newSkillRuntime(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		data, _ := io.ReadAll(req.Body)
		payloads <- string(data)
		completeSkillResponse(w)
	})
	if skills := r.Snapshot().Skills; len(skills) != 1 || skills[0].Name != "pdf-tools" {
		t.Fatal("manual-only skill was omitted from completion metadata")
	}
	done := r.request(tui.Request{Kind: "run", Value: "/skill:pdf-tools\tUser request\nkeep  spacing"})().(tui.OperationDoneMsg)
	if done.Err != nil || calls.Load() != 1 {
		t.Fatalf("skill call = %+v, calls = %d", done, calls.Load())
	}
	payload := <-payloads
	if !strings.Contains(payload, "Original instructions") || !strings.Contains(payload, "User request") || strings.Contains(payload, "disable-model-invocation") {
		t.Fatal("model did not receive the expanded body without frontmatter")
	}
	messages := r.app.ContextView().Messages()
	var text string
	for _, message := range messages {
		if user, ok := message.(llm.UserMessage); ok {
			text = textContent(user.Content)
		}
	}
	parsed := skillInvocation(text)
	if parsed == nil || parsed.Location != skillPath || parsed.Arguments != "User request\nkeep  spacing" || !strings.Contains(parsed.Content, "Original instructions") {
		t.Fatalf("saved invocation = %+v", parsed)
	}
	writeManualSkill(t, skillPath, "Updated instructions")
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatal("skill invocation did not publish one session", err)
	}
	resumed, err := app.Open(files[0], r.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Attach(resumed)
	if len(r.messages) != 2 || r.messages[0].Skill == nil || !strings.Contains(r.messages[0].Skill.Content, "Original instructions") {
		t.Fatal("history projection did not restore the saved skill snapshot")
	}
}

func TestSkillFailuresDoNotStartModelOrWriteMessages(t *testing.T) {
	var calls atomic.Int32
	r, path, _ := newSkillRuntime(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); completeSkillResponse(w) })
	for _, input := range []string{"/skill:unknown", "/skill:"} {
		if done := r.request(tui.Request{Kind: "run", Value: input})().(tui.OperationDoneMsg); done.Err == nil {
			t.Fatalf("accepted invalid input %q", input)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if done := r.request(tui.Request{Kind: "run", Value: "/skill:pdf-tools"})().(tui.OperationDoneMsg); done.Err == nil {
		t.Fatal("accepted a missing skill file")
	}
	if calls.Load() != 0 || len(r.app.ContextView().Entries) != 0 || len(r.messages) != 0 {
		t.Fatal("failed expansion started a model request or submitted messages")
	}
}

func TestResourceBoundariesRefreshSkillMetadata(t *testing.T) {
	r, path, dir := newSkillRuntime(t, func(w http.ResponseWriter, _ *http.Request) { completeSkillResponse(w) })
	if done := r.request(tui.Request{Kind: "rename", Value: "Old session"})().(tui.OperationDoneMsg); done.Err != nil {
		t.Fatal(done.Err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing saved session", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	done := r.request(tui.Request{Kind: "reload"})().(tui.OperationDoneMsg)
	if done.Err != nil || len(done.Snapshot.Skills) != 0 {
		t.Fatal("reload retained a deleted skill")
	}
	writeManualSkill(t, path, "Recreated body")
	done = r.request(tui.Request{Kind: "new"})().(tui.OperationDoneMsg)
	if done.Err != nil || len(done.Snapshot.Skills) != 1 {
		t.Fatal("new session did not rediscover a skill")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	done = r.request(tui.Request{Kind: "resume", Value: files[0]})().(tui.OperationDoneMsg)
	if done.Err != nil || len(done.Snapshot.Skills) != 0 {
		t.Fatal("resume reused the previous skill directory snapshot")
	}
}

func TestSkillCommitRetryReusesExpandedInputAfterFileChanges(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	r, path, dir := newSkillRuntime(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		completeSkillResponse(w)
	})
	if done := r.request(tui.Request{Kind: "rename", Value: "Retry test"})().(tui.OperationDoneMsg); done.Err != nil {
		t.Fatal(done.Err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing seed session", err)
	}
	input := "/skill:pdf-tools explain this"
	cmd := r.request(tui.Request{Kind: "run", Value: input})
	<-started
	if err := os.Rename(files[0], files[0]+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(files[0], 0o755); err != nil {
		t.Fatal(err)
	}
	close(release)
	if done := cmd().(tui.OperationDoneMsg); done.Err == nil || r.pendingSkillInput == nil {
		t.Fatal("want a retained skill invocation after commit failure")
	}
	writeManualSkill(t, path, "Updated instructions")
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(files[0]+".saved", files[0]); err != nil {
		t.Fatal(err)
	}
	if done := r.request(tui.Request{Kind: "run", Value: input})().(tui.OperationDoneMsg); done.Err != nil || calls.Load() != 1 || r.pendingSkillInput != nil {
		t.Fatalf("retry = %+v, model calls = %d", done, calls.Load())
	}
	var users int
	for _, message := range r.app.ContextView().Messages() {
		if user, ok := message.(llm.UserMessage); ok {
			users++
			if !strings.Contains(textContent(user.Content), "Original instructions") {
				t.Fatal("retry replaced the original skill snapshot")
			}
		}
	}
	if users != 1 {
		t.Fatal("retry duplicated the user message")
	}
	if done := r.request(tui.Request{Kind: "run", Value: input})().(tui.OperationDoneMsg); done.Err != nil || calls.Load() != 2 {
		t.Fatal("a new invocation did not start a fresh run")
	}
	last := r.app.ContextView().Messages()
	encoded, _ := json.Marshal(last[len(last)-2])
	if !strings.Contains(string(encoded), "Updated instructions") {
		t.Fatal("a fresh invocation did not read the current file")
	}
}
