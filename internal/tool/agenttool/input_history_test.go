package agenttool

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/subagent"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

func historyDoneStream(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
	events := make(chan llm.AssistantMessageEvent, 1)
	events <- llm.AssistantMessageDoneEvent{Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}, StopReason: llm.StopReasonStop}}
	close(events)
	return events, nil
}

func historyWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not finish")
	}
}

func historyTranscriptUsers(t *testing.T, path string) []llm.UserMessage {
	t.Helper()
	messages, _, err := session.ReadMessages(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var users []llm.UserMessage
	for _, message := range messages {
		if user, ok := message.(llm.UserMessage); ok {
			users = append(users, user)
		}
	}
	return users
}

func TestInputHistoryContinuationPendingCommit(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		name := "success"
		if failCommit {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			runner := testRunner(t)
			id := "pending-child"
			path := transcriptPathFor(runner.SessionDir, id)
			sm, err := session.NewAt(runner.ParentCwd, path, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := sm.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "inherited old input"}}}); err != nil {
				t.Fatal(err)
			}
			origin := llm.UserInputOrigin{Generation: 2, Sequence: 1, SessionID: id, Text: "raw pending"}
			hidden := llm.UserInputOrigin{Generation: 2, Sequence: 2, SessionID: id, Text: "hidden pending"}
			pending := []llm.UserMessage{
				{Content: []llm.Content{llm.TextContent{Text: "expanded pending"}}, Origin: &origin},
				{Content: []llm.Content{llm.TextContent{Text: "hidden notification"}}, Hidden: true, Origin: &hidden},
				{Content: []llm.Content{llm.TextContent{Text: "programmatic pending"}}},
			}
			if failCommit {
				// 真实 session 编码失败，使整个候选批次无法提交。
				pending[0].Content = append(pending[0].Content, llm.ToolCall{ID: "invalid", Name: "invalid", Arguments: map[string]any{"unsupported": make(chan int)}})
			}
			var accepted []llm.UserInputOrigin
			runner.InputAccepted = func(got llm.UserInputOrigin) { accepted = append(accepted, got) }
			runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{ID: id, AgentType: "work", Continuation: true, PendingMessages: pending})
			users := historyTranscriptUsers(t, path)
			if failCommit {
				if err == nil || runtime != nil || len(accepted) != 0 || len(users) != 1 || llm.TextOf(users[0].Content) != "inherited old input" {
					t.Fatalf("failed commit: runtime=%#v err=%v accepted=%#v users=%#v", runtime, err, accepted, users)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(accepted, []llm.UserInputOrigin{origin}) {
				t.Fatalf("accepted = %#v, want only visible pending origin", accepted)
			}
			if len(users) != 4 || llm.TextOf(users[1].Content) != "expanded pending" || !users[2].Hidden || llm.TextOf(users[3].Content) != "programmatic pending" {
				t.Fatalf("persisted users = %#v", users)
			}
			if len(runtime.Agent.Context().Messages) != 1+len(pending) {
				t.Fatalf("pending context = %#v", runtime.Agent.Context().Messages)
			}
		})
	}
}

func TestInputHistorySubagentQueueAndContinue(t *testing.T) {
	for _, followUp := range []bool{false, true} {
		name := "steering"
		if followUp {
			name = "follow-up"
		}
		t.Run(name, func(t *testing.T) {
			runner := testRunner(t)
			started, release := make(chan struct{}), make(chan struct{})
			calls := 0
			runner.SetModel(llm.Model{ID: "test", Provider: "test"}, func(ctx context.Context, model llm.Model, messages llm.Context, opts llm.Options) (<-chan llm.AssistantMessageEvent, error) {
				calls++
				if calls == 1 {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return historyDoneStream(ctx, model, messages, opts)
			})
			accepted := make(chan llm.UserInputOrigin, 8)
			runner.InputAccepted = func(origin llm.UserInputOrigin) { accepted <- origin }
			runtimes := make(chan *subagent.AgentRuntime, 4)
			manager := subagent.NewManager(runner.TaskStore, func(ctx context.Context, spec taskpkg.AgentSpec) (*subagent.AgentRuntime, error) {
				runtime, err := runner.Run(ctx, spec)
				if err == nil {
					runtimes <- runtime
				}
				return runtime, err
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			launched, err := manager.Start(ctx, taskpkg.AgentInput{Description: "inspect", Prompt: "tool initial"})
			if err != nil {
				t.Fatal(err)
			}
			runtime := <-runtimes
			defer func() {
				if runtime.Cancel != nil {
					runtime.Cancel()
				}
			}()
			historyWait(t, started)
			origin := llm.UserInputOrigin{Generation: 6, Sequence: 1, SessionID: launched.TaskID, Text: "raw child queue"}
			message := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded child queue"}}, Origin: &origin}
			if err := runner.TaskStore.EnqueueTaskMessage(launched.TaskID, message, followUp); err != nil {
				t.Fatal(err)
			}
			if users := historyTranscriptUsers(t, runtime.OutputFile); len(users) != 1 {
				t.Fatalf("unconsumed queue persisted: %#v", users)
			}
			select {
			case got := <-accepted:
				t.Fatalf("unconsumed input accepted: %#v", got)
			default:
			}
			close(release)
			historyWait(t, runtime.DoneChan)
			if followUp {
				// Manager 只转移队列；显式驱动既有后继入口才算实际消费。
				if runtime.Agent.Len(agent.QueueFollowUp) != 1 {
					t.Fatalf("transferred follow-up queue = %d", runtime.Agent.Len(agent.QueueFollowUp))
				}
				if users := historyTranscriptUsers(t, runtime.OutputFile); len(users) != 1 {
					t.Fatalf("unconsumed follow-up persisted: %#v", users)
				}
				select {
				case got := <-accepted:
					t.Fatalf("unconsumed follow-up accepted: %#v", got)
				default:
				}
				if _, err := runtime.Agent.ContinueFollowUp(ctx); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case got := <-accepted:
				if got != origin {
					t.Fatalf("queue origin = %#v", got)
				}
			default:
				t.Fatal("consumed input was not accepted")
			}
			users := historyTranscriptUsers(t, runtime.OutputFile)
			if len(users) != 2 || llm.TextOf(users[1].Content) != "expanded child queue" {
				t.Fatalf("consumed users = %#v", users)
			}
			continuation := llm.UserInputOrigin{Generation: 6, Sequence: 2, SessionID: launched.TaskID, Text: "raw continuation"}
			if _, err := manager.Continue(ctx, launched.TaskID, "model continuation", &continuation); err != nil {
				t.Fatal(err)
			}
			continued := <-runtimes
			historyWait(t, continued.DoneChan)
			select {
			case got := <-accepted:
				if got != continuation {
					t.Fatalf("continue origin = %#v", got)
				}
			default:
				t.Fatal("continuation was not accepted")
			}
			users = historyTranscriptUsers(t, runtime.OutputFile)
			if len(users) != 3 || llm.TextOf(users[2].Content) != "model continuation" {
				t.Fatalf("continuation users = %#v", users)
			}
			if _, err := manager.Continue(ctx, launched.TaskID, "tool continuation", nil); err != nil {
				t.Fatal(err)
			}
			automatic := <-runtimes
			historyWait(t, automatic.DoneChan)
			users = historyTranscriptUsers(t, runtime.OutputFile)
			if len(users) != 4 || llm.TextOf(users[3].Content) != "tool continuation" {
				t.Fatalf("automatic continuation users = %#v", users)
			}
			select {
			case got := <-accepted:
				t.Fatalf("extra accepted input: %#v", got)
			default:
			}
		})
	}
}

func TestInputHistorySubagentModelFailureKeepsAdmission(t *testing.T) {
	runner := testRunner(t)
	failure := errors.New("model unavailable")
	runner.SetModel(llm.Model{ID: "test"}, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
		return nil, failure
	})
	path := transcriptPathFor(runner.SessionDir, "failed-child")
	sm, err := session.NewAt(runner.ParentCwd, path, "failed-child")
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.AppendMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "inherited old input"}}}); err != nil {
		t.Fatal(err)
	}
	origin := llm.UserInputOrigin{Generation: 9, Sequence: 1, SessionID: "failed-child", Text: "raw continuation"}
	var accepted []llm.UserInputOrigin
	runner.InputAccepted = func(got llm.UserInputOrigin) { accepted = append(accepted, got) }
	runtime, err := runner.Run(context.Background(), taskpkg.AgentSpec{ID: "failed-child", AgentType: "work", Continuation: true, Origin: &origin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(context.Background(), "model continuation"); !errors.Is(err, failure) {
		t.Fatalf("run error = %v", err)
	}
	users := historyTranscriptUsers(t, path)
	if len(users) != 2 || llm.TextOf(users[0].Content) != "inherited old input" || llm.TextOf(users[1].Content) != "model continuation" || !reflect.DeepEqual(accepted, []llm.UserInputOrigin{origin}) {
		t.Fatalf("model failure: users=%#v accepted=%#v", users, accepted)
	}
}
