package task

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestInputHistoryTaskQueuePreservesOrigin(t *testing.T) {
	for _, followUp := range []bool{false, true} {
		name := "steering"
		if followUp {
			name = "follow-up"
		}
		t.Run(name, func(t *testing.T) {
			store := NewStore()
			id, token, err := store.Register(RegisterSpec{Type: TypeAgent, Description: "inspect", AgentType: "work"})
			if err != nil {
				t.Fatal(err)
			}
			origin := llm.UserInputOrigin{Generation: 2, Sequence: 4, SessionID: id, Text: "raw task input"}
			message := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded task input"}, llm.ImageContent{Data: "aW1n", MIMEType: "image/png"}}, Origin: &origin}
			cloned := CloneUserMessages([]llm.UserMessage{message})
			if len(cloned) != 1 || cloned[0].Origin != &origin {
				t.Fatalf("clone = %#v", cloned)
			}
			cloned[0].Content[0] = llm.TextContent{Text: "changed clone"}
			if llm.TextOf(message.Content) != "expanded task input" {
				t.Fatal("clone aliased content slice")
			}
			if err := store.EnqueueTaskMessage(id, message, followUp); err != nil {
				t.Fatal(err)
			}
			var snapshot, drained []llm.UserMessage
			if followUp {
				snapshot = store.PendingFollowUpMessages(id)
			} else {
				snapshot = store.PendingMessages(id)
			}
			if len(snapshot) != 1 || snapshot[0].Origin != &origin {
				t.Fatalf("snapshot = %#v", snapshot)
			}
			snapshot[0].Content[0] = llm.TextContent{Text: "changed snapshot"}
			if followUp {
				drained = store.DrainPendingFollowUpMessages(id, token)
			} else {
				drained = store.DrainPendingMessages(id, token)
			}
			if !reflect.DeepEqual(drained, []llm.UserMessage{message}) {
				t.Fatalf("drained = %#v, want %#v", drained, message)
			}
			if followUp {
				store.RequeuePendingFollowUpMessagesFront(id, token, drained)
				drained = store.DrainPendingFollowUpMessages(id, token)
			} else {
				store.RequeuePendingMessagesFront(id, token, drained)
				drained = store.DrainPendingMessages(id, token)
			}
			if len(drained) != 1 || drained[0].Origin != &origin || !reflect.DeepEqual(drained[0].Content, message.Content) {
				t.Fatalf("rollback lost origin/content: %#v", drained)
			}
		})
	}
}

func TestInputHistoryTaskOriginExcludedFromToolJSON(t *testing.T) {
	origin := llm.UserInputOrigin{Generation: 8, Sequence: 12, SessionID: "private-task", Text: "private raw text"}
	for _, value := range []any{AgentInput{Description: "inspect", Prompt: "model text", Origin: &origin}, AgentSpec{ID: "task", Prompt: "model text", Origin: &origin}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range [][]byte{[]byte("Origin"), []byte("origin"), []byte(origin.Text), []byte(origin.SessionID)} {
			if bytes.Contains(data, forbidden) {
				t.Fatalf("tool JSON leaked origin %q: %s", forbidden, data)
			}
		}
		if !bytes.Contains(data, []byte("model text")) {
			t.Fatalf("prompt missing: %s", data)
		}
	}
}
