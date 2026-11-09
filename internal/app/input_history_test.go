package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
)

type inputHistoryRow struct {
	SessionID string `json:"session_id"`
	Timestamp int64  `json:"ts"`
	Text      string `json:"text"`
}

func readInputHistoryRows(t *testing.T, path string) []inputHistoryRow {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		t.Fatal("history has an unterminated row")
	}
	var rows []inputHistoryRow
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var row inputHistoryRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.SessionID == "" || row.Timestamp < 0 {
			t.Fatalf("invalid row: %#v", row)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestInputHistoryServiceIdentityAndCloseDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	history := NewInputHistory(path, nil)
	t.Cleanup(func() { history.Close() })
	first := llm.UserInputOrigin{Generation: 2, Sequence: 7, SessionID: "old-task", Text: "same\n输入"}
	second := first
	second.Sequence++
	for _, origin := range []llm.UserInputOrigin{first, first, second, first} {
		if err := history.Accept(origin); err != nil {
			t.Fatal(err)
		}
	}
	if failures := history.Close(); len(failures) != 0 {
		t.Fatalf("Close errors = %v", failures)
	}
	rows := readInputHistoryRows(t, path)
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want two distinct inputs", rows)
	}
	for _, row := range rows {
		if row.SessionID != first.SessionID || row.Text != first.Text {
			t.Fatalf("row = %#v, want original attribution", row)
		}
	}
	rejected := llm.UserInputOrigin{Generation: 3, Sequence: 1, SessionID: "new", Text: "after close"}
	rejection := history.Accept(rejected)
	if rejection == nil {
		t.Fatal("Accept after Close succeeded")
	}
	if failures := history.Close(); len(failures) != 1 || !errors.Is(failures[0], rejection) {
		t.Fatalf("unreported closed-service rejection = %v, want %v", failures, rejection)
	}
	history.Acknowledge(rejected)
	if failures := history.Close(); len(failures) != 0 {
		t.Fatalf("acknowledged rejection returned again: %v", failures)
	}
	if got := readInputHistoryRows(t, path); !reflect.DeepEqual(got, rows) {
		t.Fatalf("closed history changed: %#v", got)
	}
}

func TestInputHistoryUnreportedFailureAtExit(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		name := "unreported"
		if acknowledged {
			name = "acknowledged"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			type failure struct {
				origin llm.UserInputOrigin
				err    error
			}
			notifications := make(chan failure, 2)
			history := NewInputHistory(path, func(origin llm.UserInputOrigin, err error) { notifications <- failure{origin, err} })
			t.Cleanup(func() { history.Close() })
			origin := llm.UserInputOrigin{Generation: 8, Sequence: 12, SessionID: "old-child", Text: "raw text"}
			if err := history.Accept(origin); err != nil {
				t.Fatalf("enqueue = %v, want deferred disk failure", err)
			}
			history.Barrier()
			var saved failure
			select {
			case saved = <-notifications:
			case <-time.After(3 * time.Second):
				t.Fatal("disk failure was not reported")
			}
			if saved.origin != origin || saved.err == nil {
				t.Fatalf("notification = %#v", saved)
			}
			// 展示确认不改变身份去重；磁盘恢复后模型重试也不能重复追加。
			if acknowledged {
				history.Acknowledge(origin)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := history.Accept(origin); err != nil {
				t.Fatal(err)
			}
			history.Barrier()
			if rows := readInputHistoryRows(t, path); len(rows) != 0 {
				t.Fatalf("failed identity retried: %#v", rows)
			}
			failures := history.Close()
			if acknowledged {
				if len(failures) != 0 {
					t.Fatalf("acknowledged errors returned: %v", failures)
				}
			} else if len(failures) != 1 || !errors.Is(failures[0], saved.err) {
				t.Fatalf("exit errors = %v, want original disk error %v", failures, saved.err)
			}
		})
	}
}

func TestInputHistoryBorrowerCloseKeepsServiceUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	history := NewInputHistory(path, nil)
	defer history.Close()
	a := operationApp(t, countingStreamFn(new(int), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}), nil)
	a.BindInputHistory(history)
	a.Close()
	origin := llm.UserInputOrigin{Generation: 1, Sequence: 1, SessionID: "old-child", Text: "late accepted"}
	if err := history.Accept(origin); err != nil {
		t.Fatalf("borrower closed shared service: %v", err)
	}
	history.Barrier()
	rows := readInputHistoryRows(t, path)
	if len(rows) != 1 || rows[0].SessionID != origin.SessionID || rows[0].Text != origin.Text {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestInputHistoryRetryPendingOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	history := NewInputHistory(path, nil)
	defer history.Close()
	a := operationApp(t, countingStreamFn(new(int), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}), nil)
	defer a.Close()
	a.BindInputHistory(history)
	if err := a.session.AppendMessage(textMsg("seed")); err != nil {
		t.Fatal(err)
	}
	original := llm.UserInputOrigin{Generation: 2, Sequence: 1, SessionID: a.session.ID(), Text: "/skill:inspect raw"}
	message := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded content"}}, Origin: &original}
	// 先对齐 system prompt，故障必须落在用户消息提交而非请求准备。
	if err := a.coordinator.preparePrompt(); err != nil {
		t.Fatal(err)
	}
	restore := blockSessionWrites(t, a)
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Message: &message})
	if err != nil {
		t.Fatal(err)
	}
	var commitErr *session.CommitError
	if result := waitOperation(t, op); !errors.As(result.Err, &commitErr) {
		t.Fatalf("result error = %v, want commit failure", result.Err)
	}
	history.Barrier()
	if rows := readInputHistoryRows(t, path); len(rows) != 0 {
		t.Fatalf("failed commit recorded: %#v", rows)
	}
	if !a.session.HasPending() {
		t.Fatal("failed message missing from pending commit")
	}
	restore()
	fresh := original
	fresh.Generation++
	fresh.Sequence++
	fresh.SessionID = "new-identity-must-not-replace-original"
	fresh.Text = "retry raw"
	retryMessage := message
	retryMessage.Origin = &fresh
	op, err = a.Start(context.Background(), OperationRequest{Kind: OperationRun, Message: &retryMessage})
	if err != nil {
		t.Fatal(err)
	}
	if result := waitOperation(t, op); result.Err != nil || !result.Replayed {
		t.Fatalf("retry result = %#v", result)
	}
	history.Barrier()
	rows := readInputHistoryRows(t, path)
	if len(rows) != 1 || rows[0].SessionID != original.SessionID || rows[0].Text != original.Text {
		t.Fatalf("retried history = %#v", rows)
	}
	messages, _, err := session.ReadMessages(a.session.Path(), 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range messages {
		if user, ok := item.(llm.UserMessage); ok && llm.TextOf(user.Content) == "expanded content" {
			count++
		}
	}
	if count != 1 || a.session.HasPending() {
		t.Fatalf("persisted input count = %d, pending = %v", count, a.session.HasPending())
	}
}

func TestInputHistoryQueueFileWaitsForConsumption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	history := NewInputHistory(path, nil)
	defer history.Close()
	a := operationApp(t, countingStreamFn(new(int), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}), nil)
	defer a.Close()
	a.BindInputHistory(history)
	for _, kind := range []agent.QueueKind{agent.QueueSteering, agent.QueueFollowUp} {
		origin := llm.UserInputOrigin{Generation: 1, Sequence: uint64(kind) + 1, SessionID: a.session.ID(), Text: "raw queued"}
		if err := a.Enqueue(kind, agent.PendingPayload{Text: "expanded queued", Origin: &origin}); err != nil {
			t.Fatal(err)
		}
	}
	history.Barrier()
	if rows := readInputHistoryRows(t, path); len(rows) != 0 {
		t.Fatalf("unconsumed queue recorded: %#v", rows)
	}
	withdrawn := a.agent.Take(agent.QueueFollowUp)
	if len(withdrawn) != 1 {
		t.Fatalf("withdrawn = %#v", withdrawn)
	}
	if _, err := a.Run(context.Background(), "programmatic trigger"); err != nil {
		t.Fatal(err)
	}
	history.Barrier()
	rows := readInputHistoryRows(t, path)
	if len(rows) != 1 || rows[0].Text != "raw queued" {
		t.Fatalf("consumed rows = %#v", rows)
	}
	count := 0
	for _, item := range a.ContextView().Messages() {
		if user, ok := item.(llm.UserMessage); ok && llm.TextOf(user.Content) == "expanded queued" {
			count++
		}
	}
	if count != 1 || a.agent.Len(agent.QueueSteering) != 0 {
		t.Fatalf("consumed messages=%d, queue=%d", count, a.agent.Len(agent.QueueSteering))
	}
}

func TestInputHistoryAdmissionPersistsDespiteDownstreamFailure(t *testing.T) {
	for _, projection := range []bool{false, true} {
		name := "model"
		if projection {
			name = "projection"
		}
		t.Run(name, func(t *testing.T) {
			failure := errors.New("downstream failure")
			path := filepath.Join(t.TempDir(), "history.jsonl")
			history := NewInputHistory(path, nil)
			defer history.Close()
			a := operationApp(t, func(context.Context, llm.Model, llm.Context, llm.Options) (<-chan llm.AssistantMessageEvent, error) {
				return nil, failure
			}, func(event llm.AgentEvent) error {
				if end, ok := event.(llm.MessageEndEvent); ok && end.Message.Role() == llm.RoleUser && projection {
					return failure
				}
				return nil
			})
			defer a.Close()
			a.BindInputHistory(history)
			origin := llm.UserInputOrigin{Generation: 4, Sequence: 1, SessionID: "original-main", Text: "/skill:inspect raw"}
			message := llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded text"}}, Origin: &origin}
			op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Message: &message})
			if err != nil {
				t.Fatal(err)
			}
			if result := waitOperation(t, op); !errors.Is(result.Err, failure) {
				t.Fatalf("result error = %v", result.Err)
			}
			history.Barrier()
			rows := readInputHistoryRows(t, path)
			if len(rows) != 1 || rows[0].Text != origin.Text || rows[0].SessionID != origin.SessionID {
				t.Fatalf("accepted rows = %#v", rows)
			}
			messages, _, err := session.ReadMessages(a.session.Path(), 0)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, item := range messages {
				if user, ok := item.(llm.UserMessage); ok && llm.TextOf(user.Content) == "expanded text" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("persisted user count = %d", count)
			}
		})
	}
}

func TestInputHistoryServiceKeyIncludesGenerationAndSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	history := NewInputHistory(path, nil)
	defer history.Close()
	origins := []llm.UserInputOrigin{
		{Generation: 1, Sequence: 1, SessionID: "main", Text: "same"},
		{Generation: 2, Sequence: 1, SessionID: "main", Text: "same"},
		{Generation: 2, Sequence: 1, SessionID: "child", Text: "same"},
	}
	for _, origin := range origins {
		if err := history.Accept(origin); err != nil {
			t.Fatal(err)
		}
	}
	history.Barrier()
	rows := readInputHistoryRows(t, path)
	if len(rows) != len(origins) {
		t.Fatalf("rows = %#v", rows)
	}
	for i, origin := range origins {
		if rows[i].SessionID != origin.SessionID || rows[i].Text != origin.Text {
			t.Fatalf("row %d = %#v", i, rows[i])
		}
	}
}

func TestInputHistoryQueueRetryPendingOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	history := NewInputHistory(path, nil)
	defer history.Close()
	turnEnded, proceed := make(chan struct{}), make(chan struct{})
	firstTurn := true
	a := operationApp(t, countingStreamFn(new(int), llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "done"}}}), func(event llm.AgentEvent) error {
		if _, ok := event.(llm.TurnEndEvent); ok && firstTurn {
			firstTurn = false
			close(turnEnded)
			select {
			case <-proceed:
			case <-time.After(3 * time.Second):
				return errors.New("test did not release turn")
			}
		}
		return nil
	})
	defer a.Close()
	a.BindInputHistory(history)
	if err := a.session.AppendMessage(textMsg("seed")); err != nil {
		t.Fatal(err)
	}
	op, err := a.Start(context.Background(), OperationRequest{Kind: OperationRun, Value: "programmatic initial"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-turnEnded:
	case <-time.After(3 * time.Second):
		t.Fatal("initial turn did not finish")
	}
	origin := llm.UserInputOrigin{Generation: 3, Sequence: 2, SessionID: "original-queued-session", Text: "raw queued"}
	if err := a.EnqueueSteeringMessage(llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "expanded queued"}}, Origin: &origin}); err != nil {
		t.Fatal(err)
	}
	restore := blockSessionWrites(t, a)
	close(proceed)
	var commitErr *session.CommitError
	if result := waitOperation(t, op); !errors.As(result.Err, &commitErr) {
		t.Fatalf("queue failure = %v", result.Err)
	}
	history.Barrier()
	if rows := readInputHistoryRows(t, path); len(rows) != 0 {
		t.Fatalf("failed queue recorded: %#v", rows)
	}
	if a.agent.Len(agent.QueueSteering) != 1 || !a.session.HasPending() {
		t.Fatal("failed queue was not retained for retry")
	}
	restore()
	if _, err := a.Run(context.Background(), "programmatic next"); err != nil {
		t.Fatal(err)
	}
	history.Barrier()
	rows := readInputHistoryRows(t, path)
	if len(rows) != 1 || rows[0].SessionID != origin.SessionID || rows[0].Text != origin.Text {
		t.Fatalf("retry rows = %#v", rows)
	}
	messages, _, err := session.ReadMessages(a.session.Path(), 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range messages {
		if user, ok := item.(llm.UserMessage); ok && llm.TextOf(user.Content) == "expanded queued" {
			count++
		}
	}
	if count != 1 || a.agent.Len(agent.QueueSteering) != 0 || a.session.HasPending() {
		t.Fatalf("retry count=%d queue=%d pending=%v", count, a.agent.Len(agent.QueueSteering), a.session.HasPending())
	}
}

func TestInputHistorySynchronousFailureWaitsForAcknowledgment(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		name := "unreported"
		if acknowledged {
			name = "acknowledged"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			notifications := make(chan error, 1)
			history := NewInputHistory(path, func(_ llm.UserInputOrigin, err error) { notifications <- err })
			defer history.Close()
			origin := llm.UserInputOrigin{Generation: 1, Sequence: 1, Text: "missing receiving session"}
			err := history.Accept(origin)
			if err == nil {
				t.Fatal("invalid origin was admitted")
			}
			select {
			case got := <-notifications:
				t.Fatalf("synchronous failure invoked notify: %v", got)
			default:
			}
			if acknowledged {
				history.Acknowledge(origin)
			}
			failures := history.Close()
			if acknowledged {
				if len(failures) != 0 {
					t.Fatalf("acknowledged failure returned: %v", failures)
				}
			} else if len(failures) != 1 || !errors.Is(failures[0], err) {
				t.Fatalf("exit errors = %v, want %v", failures, err)
			}
			if rows := readInputHistoryRows(t, path); len(rows) != 0 {
				t.Fatalf("invalid input persisted: %#v", rows)
			}
		})
	}
}
