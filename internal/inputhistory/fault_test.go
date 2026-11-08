package inputhistory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestAppendRollbackAndPoison(t *testing.T) {
	for _, returnWriteError := range []bool{false, true} {
		name := "rollback-success-short-count"
		if returnWriteError {
			name = "rollback-success-write-error"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			old := []Record{
				{SessionID: "old", Timestamp: 0, Text: "完整前缀 A"},
				{SessionID: "old", Timestamp: 1, Text: "完整前缀 B\nsecond line"},
			}
			prefix := writeHistoryRecords(t, path, old...)
			store := New(path)
			defer store.Close()
			view := historySnapshot(t, store)
			writeErr := errors.New("injected partial write failure")
			var partial, afterPartialWrite []byte
			failed := false
			rollbackLength := int64(-1)
			store.file.ops.write = func(file *os.File, data []byte) (int, error) {
				if failed {
					return file.Write(data)
				}
				failed = true
				partial = append([]byte(nil), data[:len(data)/2]...)
				n, err := file.Write(partial)
				if err != nil {
					return n, err
				}
				afterPartialWrite = make([]byte, len(prefix)+n)
				if _, err = file.ReadAt(afterPartialWrite, 0); err != nil {
					return n, err
				}
				if returnWriteError {
					return n, writeErr
				}
				return n, nil
			}
			store.file.ops.truncate = func(file *os.File, length int64) error {
				rollbackLength = length
				return file.Truncate(length)
			}
			completed := make(chan error, 4)
			bad := Record{SessionID: "failed", Timestamp: 2, Text: "must be rolled back\n残缺文本"}
			if err := store.Append(bad, func(err error) { completed <- err }); err != nil {
				t.Fatal(err)
			}
			if err := waitHistoryCompletion(t, completed); err == nil {
				t.Fatal("partial write completed successfully")
			}
			store.Barrier()
			wantPartial := append(append([]byte(nil), prefix...), partial...)
			if len(partial) == 0 || !bytes.Equal(afterPartialWrite, wantPartial) {
				t.Fatalf("fault did not leave real partial bytes: got %q, want %q", afterPartialWrite, wantPartial)
			}
			if rollbackLength != int64(len(prefix)) {
				t.Fatalf("rollback boundary = %d, want %d", rollbackLength, len(prefix))
			}
			if got := historyFileBytes(t, path); !bytes.Equal(got, prefix) {
				t.Fatalf("successful rollback changed committed prefix: got %q, want %q", got, prefix)
			}
			assertHistoryView(t, store, view, old)
			after := Record{SessionID: "next", Timestamp: 3, Text: "independent record survives"}
			appendHistoryRecord(t, store, after)
			store.Close()
			select {
			case err := <-completed:
				t.Fatalf("failed append completed more than once: %v", err)
			default:
			}
			want := append(append([]Record(nil), old...), after)
			if got := historyFileRecords(t, path); !reflect.DeepEqual(got, want) {
				t.Fatalf("history after rollback and recovery = %+v, want %+v", got, want)
			}
			reopened := New(path)
			defer reopened.Close()
			assertHistoryView(t, reopened, historySnapshot(t, reopened), want)
		})
	}

	t.Run("rollback-failure-poisons-pending-and-future-appends", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "history.jsonl")
		old := Record{SessionID: "old", Timestamp: 0, Text: "committed prefix"}
		prefix := writeHistoryRecords(t, path, old)
		store := New(path)
		defer store.Close()
		view := historySnapshot(t, store)
		writeErr := errors.New("injected partial write failure")
		rollbackErr := errors.New("injected rollback failure")
		partialWritten := make(chan struct{})
		releaseWrite := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(releaseWrite) })
		var partial, afterPartialWrite []byte
		firstWrite := true
		store.file.ops.write = func(file *os.File, data []byte) (int, error) {
			if !firstWrite {
				return file.Write(data)
			}
			firstWrite = false
			partial = append([]byte(nil), data[:len(data)/2]...)
			n, err := file.Write(partial)
			if err == nil {
				afterPartialWrite = make([]byte, len(prefix)+n)
				_, err = file.ReadAt(afterPartialWrite, 0)
			}
			close(partialWritten)
			<-releaseWrite
			if err != nil {
				return n, err
			}
			return n, writeErr
		}
		store.file.ops.truncate = func(file *os.File, length int64) error {
			return rollbackErr
		}
		type completion struct {
			index int
			err   error
		}
		completed := make(chan completion, 16)
		accept := func(index int) {
			t.Helper()
			record := Record{SessionID: "accepted", Timestamp: int64(index), Text: fmt.Sprintf("record-%d must not survive poison", index)}
			if err := store.Append(record, func(err error) { completed <- completion{index: index, err: err} }); err != nil {
				t.Fatalf("valid Append(%d) was rejected before its worker completion: %v", index, err)
			}
		}
		accept(0)
		waitHistorySignal(t, partialWritten, "real partial write")
		accept(1)
		wantDamaged := append(append([]byte(nil), prefix...), partial...)
		if len(partial) == 0 || !bytes.Equal(afterPartialWrite, wantDamaged) {
			t.Fatal("fault did not write the intended partial JSON after the committed prefix")
		}
		releaseOnce.Do(func() { close(releaseWrite) })
		store.Barrier()
		// 首次失败已完成后仍接纳新项，由 poisoned 状态逐项报告失败
		accept(2)
		accept(3)
		store.Barrier()
		store.Close()
		for index := range 4 {
			select {
			case got := <-completed:
				if got.index != index || got.err == nil {
					t.Fatalf("completion %d = %+v, want this accepted item to fail", index, got)
				}
			default:
				t.Fatalf("missing completion for accepted item %d", index)
			}
		}
		select {
		case got := <-completed:
			t.Fatalf("accepted item completed more than once: %+v", got)
		default:
		}
		if got := historyFileBytes(t, path); !bytes.Equal(got, wantDamaged) {
			t.Fatalf("poisoned Store appended or repaired bytes after failed rollback: got %q, want %q", got, wantDamaged)
		}
		assertHistoryView(t, store, view, []Record{old})
		newView, issues, err := store.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if want := (ScanIssues{UnterminatedTailBytes: int64(len(partial))}); issues != want {
			t.Fatalf("poisoned file issues = %+v, want %+v", issues, want)
		}
		assertHistoryView(t, store, newView, []Record{old})
	})
}

func TestAppendCloseFailureDoesNotRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store := New(path)
	defer store.Close()
	closeErr := errors.New("injected close failure after descriptor close")
	closeAttempted := false
	store.file.ops.close = func(file *os.File) error {
		if err := file.Close(); err != nil {
			return err
		}
		if !closeAttempted {
			closeAttempted = true
			return closeErr
		}
		return nil
	}
	record := Record{SessionID: "s", Timestamp: 0, Text: "fully written despite close error\n中文"}
	completed := make(chan error, 4)
	if err := store.Append(record, func(err error) { completed <- err }); err != nil {
		t.Fatal(err)
	}
	if err := waitHistoryCompletion(t, completed); !errors.Is(err, closeErr) {
		t.Fatalf("completion = %v, want injected close error", err)
	}
	store.Barrier()
	store.Barrier()
	store.Close()
	select {
	case err := <-completed:
		t.Fatalf("close-failed append completed more than once: %v", err)
	default:
	}
	if got := historyFileBytes(t, path); !bytes.Equal(got, historyJSONLine(t, record)) {
		t.Fatalf("close failure retried or changed the complete record: got %q", got)
	}
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, []Record{record}) {
		t.Fatalf("close-failed append persisted = %+v, want exactly one record", got)
	}
	reopened := New(path)
	defer reopened.Close()
	assertHistoryView(t, reopened, historySnapshot(t, reopened), []Record{record})
}
