package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestInputHistoryOriginIsTransient(t *testing.T) {
	origin := llm.UserInputOrigin{Generation: 42, Sequence: 93, SessionID: "private-origin-session", Text: "private-original-editor-text"}
	for _, hidden := range []bool{false, true} {
		name := "visible"
		if hidden {
			name = "hidden"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			sm, err := NewAt(t.TempDir(), path, "transient")
			if err != nil {
				t.Fatal(err)
			}
			message := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded text"}, llm.ImageContent{Data: "aW1n", MIMEType: "image/png"}}, Hidden: hidden, Origin: &origin}
			if _, err := sm.AppendMessages([]llm.Message{message}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(data, []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var entry struct {
					Message json.RawMessage `json:"message"`
				}
				if err := json.Unmarshal(line, &entry); err != nil {
					t.Fatal(err)
				}
				if len(entry.Message) == 0 {
					continue
				}
				for _, forbidden := range [][]byte{[]byte("\"Origin\""), []byte("\"origin\""), []byte(origin.Text), []byte(origin.SessionID), []byte("\"Generation\""), []byte("\"Sequence\"")} {
					if bytes.Contains(entry.Message, forbidden) {
						t.Fatalf("session message leaked origin %q: %s", forbidden, entry.Message)
					}
				}
			}
			opened, err := OpenWithID(path, "transient")
			if err != nil {
				t.Fatal(err)
			}
			messages := opened.BuildContext().Messages()
			if len(messages) != 1 {
				t.Fatalf("reopened messages = %#v", messages)
			}
			got, ok := messages[0].(llm.UserMessage)
			if !ok || got.Origin != nil || got.Hidden != hidden || !reflect.DeepEqual(got.Content, message.Content) {
				t.Fatalf("reopened user = %#v", messages[0])
			}
			legacy, err := decodeMessage([]byte(`{"role":"user","content":[{"type":"text","text":"legacy"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := legacy.(llm.UserMessage); !ok || got.Origin != nil || llm.TextOf(got.Content) != "legacy" {
				t.Fatalf("legacy user = %#v", got)
			}
		})
	}
}
