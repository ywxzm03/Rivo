package inputhistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const historyTestTimeout = 10 * time.Second

func historyJSONLine(t *testing.T, record Record) []byte {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func writeHistoryRecords(t *testing.T, path string, records ...Record) []byte {
	t.Helper()
	var data []byte
	for _, record := range records {
		data = append(data, historyJSONLine(t, record)...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func historyFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func historyFileRecords(t *testing.T, path string) []Record {
	t.Helper()
	data := historyFileBytes(t, path)
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] != '\n' {
		t.Fatalf("history has uncommitted tail: %q", data)
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	records := make([]Record, 0, len(lines))
	for i, line := range lines {
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("history row %d is not complete JSON: %v", i, err)
		}
		records = append(records, record)
	}
	return records
}

func historySnapshot(t *testing.T, store *Store) *View {
	t.Helper()
	view, issues, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if view == nil || issues != (ScanIssues{}) {
		t.Fatalf("Snapshot() = %v, %+v, want a clean view", view, issues)
	}
	return view
}

func assertHistoryView(t *testing.T, store *Store, view *View, want []Record) {
	t.Helper()
	if view.Count() != len(want) {
		t.Fatalf("Count() = %d, want %d", view.Count(), len(want))
	}
	for offset, expected := range want {
		got, err := store.Read(context.Background(), view, offset)
		if err != nil || got != expected {
			t.Fatalf("Read(%d) = %+v, %v, want %+v", offset, got, err, expected)
		}
	}
}

func waitHistorySignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(historyTestTimeout):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitHistoryCompletion(t *testing.T, completed <-chan error) error {
	t.Helper()
	select {
	case err := <-completed:
		return err
	case <-time.After(historyTestTimeout):
		t.Fatal("append completion timed out")
		return nil
	}
}

func appendHistoryRecord(t *testing.T, store *Store, record Record) {
	t.Helper()
	completed := make(chan error, 2)
	if err := store.Append(record, func(err error) { completed <- err }); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := waitHistoryCompletion(t, completed); err != nil {
		t.Fatalf("append completion error = %v", err)
	}
	store.Barrier()
	select {
	case err := <-completed:
		t.Fatalf("append completed more than once: %v", err)
	default:
	}
}

func TestSnapshotAndReadText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	want := []Record{
		{SessionID: "main-A", Timestamp: 100, Text: "first"},
		{SessionID: "task-B", Timestamp: 1, Text: "第一行\n第二行\r\nemoji: 🦜\t\"quoted\"\\tail"},
	}
	writer := New(path)
	for _, record := range want {
		appendHistoryRecord(t, writer, record)
	}
	writer.Close()
	reader := New(path)
	defer reader.Close()
	assertHistoryView(t, reader, historySnapshot(t, reader), want)
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted records = %+v, want %+v", got, want)
	}
}

func TestSnapshotOfMissingOrEmptyFileDoesNotCreateHistory(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "missing"
		if existing {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			if existing {
				writeHistoryRecords(t, path)
			}
			store := New(path)
			defer store.Close()
			assertHistoryView(t, store, historySnapshot(t, store), nil)
			_, err := os.Stat(path)
			if !existing && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Snapshot created missing history: stat error = %v", err)
			}
			if existing && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSnapshotSkipsMalformedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	first := Record{SessionID: "a", Timestamp: 4, Text: "有效前缀"}
	zero := Record{SessionID: "b", Timestamp: 0, Text: "epoch"}
	spaces := Record{SessionID: "c", Timestamp: 2, Text: " \t "}
	last := Record{SessionID: "d", Timestamp: 1, Text: "末行"}
	malformed := []string{
		`{broken`, `null`, `{}`, `{"session_id":"s","text":"missing timestamp"}`,
		`{"session_id":"s","ts":null,"text":"null timestamp"}`,
		`{"session_id":"s","ts":-1,"text":"negative"}`,
		`{"session_id":"s","ts":"0","text":"wrong timestamp type"}`,
		`{"session_id":"s","ts":0.5,"text":"fraction"}`,
		`{"session_id":"s","ts":1}`, `{"session_id":"s","ts":1,"text":null}`,
		`{"session_id":"s","ts":1,"text":23}`,
		`{"ts":1,"text":"missing session"}`, `{"session_id":null,"ts":1,"text":"null session"}`,
		`{"session_id":"","ts":1,"text":"empty session"}`,
	}
	var data bytes.Buffer
	data.Write(historyJSONLine(t, first))
	for _, line := range malformed {
		data.WriteString(line + "\n")
	}
	data.WriteString("\n \t\r\n")
	data.WriteString("{\"session_id\":\"s\",\"ts\":1,\"text\":\"\"}\n")
	data.Write(historyJSONLine(t, zero))
	data.Write(historyJSONLine(t, spaces))
	data.Write(historyJSONLine(t, last))
	tail := "{\"session_id\":\"unfinished 中文"
	data.WriteString(tail)
	original := append([]byte(nil), data.Bytes()...)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(path)
	defer store.Close()
	view, issues, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantIssues := ScanIssues{MalformedRows: len(malformed), EmptyRows: 3, UnterminatedTailBytes: int64(len(tail))}
	if issues != wantIssues {
		t.Fatalf("ScanIssues = %+v, want %+v", issues, wantIssues)
	}
	assertHistoryView(t, store, view, []Record{first, zero, spaces, last})
	if got := historyFileBytes(t, path); !bytes.Equal(got, original) {
		t.Fatal("Snapshot or Read rewrote malformed rows or uncommitted tail")
	}
}

func TestSnapshotSupportsLargeRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	record := Record{SessionID: "large", Timestamp: 0, Text: strings.Repeat("粘贴文本🦜\n", 20000)}
	store := New(path)
	defer store.Close()
	appendHistoryRecord(t, store, record)
	assertHistoryView(t, store, historySnapshot(t, store), []Record{record})
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, []Record{record}) {
		t.Fatal("large multiline record was truncated or changed on disk")
	}
}

func TestViewExcludesLaterAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	first := Record{SessionID: "one", Timestamp: 10, Text: "A"}
	second := Record{SessionID: "two", Timestamp: 1, Text: "B"}
	writeHistoryRecords(t, path, first)
	store := New(path)
	defer store.Close()
	view := historySnapshot(t, store)
	other := New(path)
	appendHistoryRecord(t, other, second)
	other.Close()
	assertHistoryView(t, store, view, []Record{first})
	if _, err := store.Read(context.Background(), view, 1); err == nil {
		t.Fatal("old View exposed a later append")
	}
	assertHistoryView(t, store, historySnapshot(t, store), []Record{first, second})
}

func TestReadRejectsChangedFile(t *testing.T) {
	for _, change := range []string{"replace", "truncate", "same-inode-equal-length-rewrite"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			first := Record{SessionID: "s", Timestamp: 1, Text: "AAAA"}
			last := Record{SessionID: "s", Timestamp: 2, Text: "last"}
			original := writeHistoryRecords(t, path, first, last)
			store := New(path)
			defer store.Close()
			view := historySnapshot(t, store)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "replace":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				writeHistoryRecords(t, path, first, last)
			case "truncate":
				if err := os.Truncate(path, int64(len(historyJSONLine(t, first)))); err != nil {
					t.Fatal(err)
				}
			case "same-inode-equal-length-rewrite":
				rewritten := bytes.Replace(original, []byte("AAAA"), []byte("BBBB"), 1)
				if err := os.WriteFile(path, rewritten, 0o600); err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(before, after) || before.Size() != after.Size() {
					t.Fatal("fixture must rewrite the same inode without changing file length")
				}
			}
			if got, err := store.Read(context.Background(), view, 0); !errors.Is(err, ErrViewChanged) {
				t.Fatalf("Read(changed View) = %+v, %v, want ErrViewChanged", got, err)
			}
		})
	}
}

func TestReadRejectsInvalidOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	record := Record{SessionID: "s", Timestamp: 0, Text: "keep"}
	original := writeHistoryRecords(t, path, record)
	store := New(path)
	defer store.Close()
	view := historySnapshot(t, store)
	for _, offset := range []int{-1, view.Count(), view.Count() + 100} {
		if _, err := store.Read(context.Background(), view, offset); err == nil {
			t.Errorf("Read(%d) returned no error", offset)
		}
	}
	if _, err := store.Read(context.Background(), nil, 0); err == nil {
		t.Error("Read(nil View) returned no error")
	}
	assertHistoryView(t, store, view, []Record{record})
	if !bytes.Equal(historyFileBytes(t, path), original) {
		t.Fatal("invalid Read modified the file")
	}
}

func TestAppendPreservesDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store := New(path)
	defer store.Close()
	record := Record{SessionID: "same", Timestamp: 0, Text: "重复\ntext"}
	appendHistoryRecord(t, store, record)
	appendHistoryRecord(t, store, record)
	want := []Record{record, record}
	assertHistoryView(t, store, historySnapshot(t, store), want)
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("duplicate records = %+v, want %+v", got, want)
	}
}

func TestAppendRepairsUnterminatedTail(t *testing.T) {
	for _, tail := range []string{`{"session_id":"partial`, `{"session_id":"s","ts":0,"text":"valid JSON without newline"}`, "残尾"} {
		t.Run(tail, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			first := Record{SessionID: "old", Timestamp: 0, Text: "committed"}
			last := Record{SessionID: "new", Timestamp: 1, Text: "appended\n中文"}
			prefix := append(historyJSONLine(t, first), []byte("{broken-but-committed}\n")...)
			if err := os.WriteFile(path, append(append([]byte(nil), prefix...), []byte(tail)...), 0o600); err != nil {
				t.Fatal(err)
			}
			store := New(path)
			defer store.Close()
			view, issues, err := store.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wantIssues := ScanIssues{MalformedRows: 1, UnterminatedTailBytes: int64(len(tail))}
			if issues != wantIssues {
				t.Fatalf("ScanIssues = %+v, want %+v", issues, wantIssues)
			}
			appendHistoryRecord(t, store, last)
			assertHistoryView(t, store, view, []Record{first})
			wantBytes := append(append([]byte(nil), prefix...), historyJSONLine(t, last)...)
			if got := historyFileBytes(t, path); !bytes.Equal(got, wantBytes) {
				t.Fatalf("repair changed committed prefix or retained tail: got %q, want %q", got, wantBytes)
			}
			newView, newIssues, err := store.Snapshot(context.Background())
			if err != nil || newIssues != (ScanIssues{MalformedRows: 1}) {
				t.Fatalf("Snapshot after repair = %+v, %v", newIssues, err)
			}
			assertHistoryView(t, store, newView, []Record{first, last})
		})
	}
}

func TestFileErrorsAreReported(t *testing.T) {
	t.Run("history-is-directory", func(t *testing.T) {
		path := t.TempDir()
		store := New(path)
		defer store.Close()
		if _, _, err := store.Snapshot(context.Background()); err == nil {
			t.Error("Snapshot(directory) returned no error")
		}
		completed := make(chan error, 2)
		if err := store.Append(Record{SessionID: "s", Timestamp: 0, Text: "fail"}, func(err error) { completed <- err }); err != nil {
			t.Fatalf("valid Append should enqueue before I/O: %v", err)
		}
		if err := waitHistoryCompletion(t, completed); err == nil {
			t.Fatal("directory append completed successfully")
		}
		store.Close()
		select {
		case err := <-completed:
			t.Fatalf("failed append completed more than once: %v", err)
		default:
		}
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("failed append changed directory: entries = %v, error = %v", entries, err)
		}
	})
	t.Run("parent-is-regular-file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "parent")
		if err := os.WriteFile(parent, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := New(filepath.Join(parent, "history.jsonl"))
		defer store.Close()
		completed := make(chan error, 2)
		if err := store.Append(Record{SessionID: "s", Timestamp: 0, Text: "fail"}, func(err error) { completed <- err }); err != nil {
			t.Fatal(err)
		}
		if err := waitHistoryCompletion(t, completed); err == nil {
			t.Fatal("append through regular-file parent completed successfully")
		}
		store.Close()
		select {
		case err := <-completed:
			t.Fatalf("failed append completed more than once: %v", err)
		default:
		}
		if got := string(historyFileBytes(t, parent)); got != "unchanged" {
			t.Fatalf("failed append changed parent file to %q", got)
		}
	})
}
