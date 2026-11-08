package agent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

func TestInputHistoryAcceptedUserMessage(t *testing.T) {
	for _, projectionFailure := range []bool{false, true} {
		name := "model failure"
		if projectionFailure {
			name = "projection failure"
		}
		t.Run(name, func(t *testing.T) {
			sm, err := session.NewAt(t.TempDir(), filepath.Join(t.TempDir(), "session.jsonl"), "accepted")
			if err != nil {
				t.Fatal(err)
			}
			origin := llm.UserInputOrigin{Generation: 4, Sequence: 8, SessionID: "original-session", Text: "/skill:inspect raw"}
			failure := errors.New("injected failure")
			var accepted []llm.UserInputOrigin
			var a *Agent
			a = New(Options{
				Config: llm.AgentConfig{Model: llm.Model{ID: "test"}},
				Stream: func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
					return nil, failure
				},
				Events: func(event llm.AgentEvent) error {
					if end, ok := event.(llm.MessageEndEvent); ok && end.Message.Role() == llm.RoleUser && projectionFailure {
						return failure
					}
					return nil
				},
				Hooks: Hooks{CommitMessages: sm.AppendMessages, InputAccepted: func(got llm.UserInputOrigin) {
					// 回调发生时，消息已经发布到运行上下文。
					messages := a.Context().Messages
					if len(messages) != 1 {
						t.Errorf("accepted before context publication: %#v", messages)
					} else if user, ok := messages[0].(llm.UserMessage); !ok || llm.TextOf(user.Content) != "expanded model text" {
						t.Errorf("accepted context contains wrong user message: %#v", messages)
					}
					accepted = append(accepted, got)
				}},
			})
			_, err = a.RunMessage(context.Background(), llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded model text"}}, Origin: &origin})
			if !errors.Is(err, failure) {
				t.Fatalf("RunMessage error = %v, want injected failure", err)
			}
			if !reflect.DeepEqual(accepted, []llm.UserInputOrigin{origin}) {
				t.Fatalf("accepted = %#v, want original identity and raw text", accepted)
			}
			messages, _, err := session.ReadMessages(sm.Path(), 0)
			if err != nil {
				t.Fatal(err)
			}
			users := 0
			for _, message := range messages {
				if user, ok := message.(llm.UserMessage); ok {
					users++
					if llm.TextOf(user.Content) != "expanded model text" {
						t.Fatalf("persisted user = %#v", user)
					}
				}
			}
			if users != 1 {
				t.Fatalf("persisted user count = %d, want 1", users)
			}
		})
	}
}

func TestInputHistoryIgnoresProgrammaticInputs(t *testing.T) {
	origin := llm.UserInputOrigin{Generation: 1, Sequence: 1, SessionID: "main", Text: "hidden"}
	var accepted []llm.UserInputOrigin
	a := New(Options{Hooks: Hooks{InputAccepted: func(got llm.UserInputOrigin) { accepted = append(accepted, got) }}})
	messages := []llm.Message{
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "programmatic"}}},
		llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "notification"}}, Hidden: true, Origin: &origin},
		llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "assistant"}}},
		llm.ToolResultMessage{ToolCallID: "call", Content: []llm.Content{llm.TextContent{Text: "tool"}}},
	}
	if _, err := a.persistMessages(messages); err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 0 || !reflect.DeepEqual(a.Context().Messages, messages) {
		t.Fatalf("accepted = %#v, context = %#v", accepted, a.Context().Messages)
	}
}

func TestInputHistoryQueueRecordsOnlyAfterAdmission(t *testing.T) {
	for _, kind := range []QueueKind{QueueSteering, QueueFollowUp} {
		t.Run(kindName(kind), func(t *testing.T) {
			origin := llm.UserInputOrigin{Generation: 3, Sequence: 9, SessionID: "queued-task", Text: "raw queued"}
			payload := PendingPayload{Text: "expanded queued", Images: []ImagePart{{Data: "aW1n", MIMEType: "image/png"}}, Origin: &origin}
			var accepted []llm.UserInputOrigin
			var committed []llm.Message
			a := New(Options{Stream: doneStreamFn(new(int), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}), Hooks: Hooks{
				CommitMessages: func(messages []llm.Message) ([]string, error) {
					committed = append(committed, messages...)
					return []string{"entry"}, nil
				},
				InputAccepted: func(got llm.UserInputOrigin) { accepted = append(accepted, got) },
			}})
			if err := a.Enqueue(kind, payload); err != nil {
				t.Fatal(err)
			}
			withdrawn := a.Take(kind)
			if len(withdrawn) != 1 || withdrawn[0].Origin != &origin || len(accepted) != 0 || len(committed) != 0 {
				t.Fatalf("withdrawn = %#v, accepted = %#v, committed = %#v", withdrawn, accepted, committed)
			}
			a.Restore(kind, withdrawn)
			if kind == QueueSteering {
				if _, err := a.ContinueSteering(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := a.ContinueFollowUp(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if a.Len(kind) != 0 || !reflect.DeepEqual(accepted, []llm.UserInputOrigin{origin}) {
				t.Fatalf("queue = %d, accepted = %#v", a.Len(kind), accepted)
			}
			if len(committed) < 1 {
				t.Fatal("consumed message was not committed")
			}
			user, ok := committed[0].(llm.UserMessage)
			if !ok || user.Origin != &origin || !reflect.DeepEqual(PayloadFromMessage(user), payload) {
				t.Fatalf("committed payload = %#v, want %#v", committed[0], payload)
			}
		})
	}
}

func TestInputHistoryAcknowledgeUsesOriginalOrigin(t *testing.T) {
	for _, kind := range []QueueKind{QueueSteering, QueueFollowUp} {
		for _, textOnly := range []bool{false, true} {
			name := kindName(kind)
			if textOnly {
				name += "/text"
			} else {
				name += "/payload"
			}
			t.Run(name, func(t *testing.T) {
				original := llm.UserInputOrigin{Generation: 5, Sequence: 1, SessionID: "old-task", Text: "first raw"}
				other := llm.UserInputOrigin{Generation: 5, Sequence: 2, SessionID: "old-task", Text: "second raw"}
				retry := llm.UserInputOrigin{Generation: 6, Sequence: 1, SessionID: "new-main", Text: "retry raw"}
				failure := errors.New("commit failed")
				var accepted []llm.UserInputOrigin
				a := New(Options{Hooks: Hooks{CommitMessages: func([]llm.Message) ([]string, error) { return []string{"pending"}, failure }, InputAccepted: func(got llm.UserInputOrigin) { accepted = append(accepted, got) }}})
				for _, origin := range []*llm.UserInputOrigin{&original, &other} {
					if err := a.Enqueue(kind, PendingPayload{Text: "same", Origin: origin}); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if kind == QueueSteering {
					_, err = a.ContinueSteering(context.Background())
				} else {
					_, err = a.ContinueFollowUp(context.Background())
				}
				if !errors.Is(err, failure) || len(a.Context().Messages) != 0 || len(accepted) != 0 {
					t.Fatalf("failed admission: err=%v context=%#v accepted=%#v", err, a.Context().Messages, accepted)
				}
				var ok bool
				if textOnly {
					ok = a.AcknowledgePendingText("same")
				} else {
					ok = a.AcknowledgePending(PendingPayload{Text: "same", Origin: &retry})
				}
				if !ok || !reflect.DeepEqual(accepted, []llm.UserInputOrigin{original}) {
					t.Fatalf("acknowledged=%v accepted=%#v, want original identity", ok, accepted)
				}
				remaining := a.Take(kind)
				if len(remaining) != 1 || remaining[0].Origin != &other {
					t.Fatalf("remaining = %#v, want second equal-text input", remaining)
				}
				if a.AcknowledgePendingText("same") || len(accepted) != 1 {
					t.Fatal("acknowledgment repeated")
				}
			})
		}
	}
}
