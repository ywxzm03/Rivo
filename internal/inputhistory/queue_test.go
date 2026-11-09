package inputhistory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// 用 Cond 的解锁事件确认屏障已捕获水位，避免依赖调度延时
type historyWaitObserver struct {
	sync.Locker
	waiting chan struct{}
}

func (observer *historyWaitObserver) Unlock() {
	observer.Locker.Unlock()
	select {
	case observer.waiting <- struct{}{}:
	default:
	}
}

func observeHistoryWait(store *Store) <-chan struct{} {
	waiting := make(chan struct{}, 8)
	store.queue.changed = sync.NewCond(&historyWaitObserver{Locker: &store.queue.mu, waiting: waiting})
	return waiting
}

func TestAppendValidatesBeforeAcceptance(t *testing.T) {
	valid := Record{SessionID: "s", Timestamp: 0, Text: "valid"}
	for _, test := range []struct {
		name   string
		record Record
		nilFn  bool
	}{
		{name: "missing-session", record: Record{Timestamp: 0, Text: "text"}},
		{name: "negative-timestamp", record: Record{SessionID: "s", Timestamp: -1, Text: "text"}},
		{name: "empty-text", record: Record{SessionID: "s", Timestamp: 0}},
		{name: "missing-completion", record: valid, nilFn: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			store := New(path)
			defer store.Close()
			completed := make(chan error, 2)
			var onComplete func(error)
			if !test.nilFn {
				onComplete = func(err error) { completed <- err }
			}
			if err := store.Append(test.record, onComplete); err == nil {
				t.Fatal("invalid Append was accepted")
			}
			store.Barrier()
			store.Close()
			select {
			case err := <-completed:
				t.Fatalf("rejected request invoked completion: %v", err)
			default:
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected request created history: stat error = %v", err)
			}
		})
	}
	t.Run("zero-timestamp-and-whitespace", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "history.jsonl")
		store := New(path)
		defer store.Close()
		record := Record{SessionID: "s", Timestamp: 0, Text: " \n\t "}
		appendHistoryRecord(t, store, record)
		if got := historyFileRecords(t, path); !reflect.DeepEqual(got, []Record{record}) {
			t.Fatalf("valid zero timestamp or whitespace changed: %+v", got)
		}
	})
}

func TestAppendFIFOAndCompletionOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store := New(path)
	defer store.Close()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseFirst) })
	type completion struct {
		index int
		err   error
	}
	completed := make(chan completion, 128)
	want := make([]Record, 40)
	for index := range want {
		want[index] = Record{SessionID: fmt.Sprintf("session-%d", index%3), Timestamp: int64(100 - index), Text: fmt.Sprintf("row-%02d\n中文", index)}
		if err := store.Append(want[index], func(err error) {
			if index == 0 {
				close(firstStarted)
				<-releaseFirst
			}
			completed <- completion{index: index, err: err}
		}); err != nil {
			t.Fatalf("Append(%d) error = %v", index, err)
		}
		if index == 0 {
			waitHistorySignal(t, firstStarted, "first callback")
		}
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	store.Close()
	for index := range want {
		select {
		case got := <-completed:
			if got.index != index || got.err != nil {
				t.Fatalf("completion %d = %+v", index, got)
			}
		default:
			t.Fatalf("Close returned before completion %d", index)
		}
	}
	select {
	case got := <-completed:
		t.Fatalf("extra completion = %+v", got)
	default:
	}
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted FIFO order = %+v, want %+v", got, want)
	}
}

func TestBarrierWaitsForCapturedTicket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store := New(path)
	waiting := observeHistoryWait(store)
	defer store.Close()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var firstOnce, secondOnce sync.Once
	defer firstOnce.Do(func() { close(releaseFirst) })
	defer secondOnce.Do(func() { close(releaseSecond) })
	completed := make(chan error, 4)
	first := Record{SessionID: "s", Timestamp: 2, Text: "before barrier"}
	second := Record{SessionID: "s", Timestamp: 1, Text: "after barrier"}
	if err := store.Append(first, func(err error) {
		close(firstStarted)
		<-releaseFirst
		completed <- err
	}); err != nil {
		t.Fatal(err)
	}
	waitHistorySignal(t, firstStarted, "first callback")
	barrierDone := make(chan struct{})
	go func() {
		store.Barrier()
		close(barrierDone)
	}()
	waitHistorySignal(t, waiting, "Barrier waiting on its captured ticket")
	select {
	case <-barrierDone:
		t.Fatal("Barrier returned before its completion callback finished")
	default:
	}
	if err := store.Append(second, func(err error) {
		close(secondStarted)
		<-releaseSecond
		completed <- err
	}); err != nil {
		t.Fatal(err)
	}
	firstOnce.Do(func() { close(releaseFirst) })
	waitHistorySignal(t, secondStarted, "later callback")
	waitHistorySignal(t, barrierDone, "Barrier without waiting for later ticket")
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, []Record{first, second}) {
		t.Fatalf("file after captured barrier = %+v", got)
	}
	secondOnce.Do(func() { close(releaseSecond) })
	store.Barrier()
	store.Barrier()
	store.Close()
	for range 2 {
		if err := waitHistoryCompletion(t, completed); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-completed:
		t.Fatalf("extra completion: %v", err)
	default:
	}
}

func TestCloseDrainsAndRejectsNewAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store := New(path)
	waiting := observeHistoryWait(store)
	defer store.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	completed := make(chan error, 16)
	want := []Record{
		{SessionID: "s", Timestamp: 3, Text: "first"},
		{SessionID: "s", Timestamp: 2, Text: "second"},
		{SessionID: "s", Timestamp: 1, Text: "third"},
	}
	for i, record := range want {
		if err := store.Append(record, func(err error) {
			if i == 0 {
				close(started)
				<-release
			}
			completed <- err
		}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			waitHistorySignal(t, started, "blocked completion")
		}
	}
	closed := make(chan struct{})
	go func() {
		store.Close()
		close(closed)
	}()
	waitHistorySignal(t, waiting, "Close waiting for accepted callbacks")
	select {
	case <-closed:
		t.Fatal("Close returned while an accepted completion was blocked")
	default:
	}
	rejected := make(chan error, 4)
	if err := store.Append(Record{SessionID: "s", Timestamp: 0, Text: "during close"}, func(err error) { rejected <- err }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append during Close = %v, want ErrClosed", err)
	}
	releaseOnce.Do(func() { close(release) })
	waitHistorySignal(t, closed, "Close draining accepted work")
	store.Close()
	if err := store.Append(Record{SessionID: "s", Timestamp: 0, Text: "after close"}, func(err error) { rejected <- err }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append after Close = %v, want ErrClosed", err)
	}
	for range want {
		select {
		case err := <-completed:
			if err != nil {
				t.Fatalf("accepted append completion = %v", err)
			}
		default:
			t.Fatal("Close returned with missing accepted completion")
		}
	}
	select {
	case err := <-completed:
		t.Fatalf("accepted append completed twice: %v", err)
	default:
	}
	select {
	case err := <-rejected:
		t.Fatalf("rejected append invoked completion: %v", err)
	default:
	}
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("Close persisted = %+v, want %+v", got, want)
	}
}

func TestCompletionRunsOutsideFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store := New(path)
	defer store.Close()
	record := Record{SessionID: "s", Timestamp: 0, Text: "callback may read committed history"}
	completed := make(chan error, 2)
	probeFinished := make(chan struct{})
	if err := store.Append(record, func(err error) {
		if err != nil {
			close(probeFinished)
			completed <- err
			return
		}
		probe := make(chan error, 1)
		go func() {
			defer close(probeFinished)
			view, issues, err := store.Snapshot(context.Background())
			if err != nil {
				probe <- err
				return
			}
			if issues != (ScanIssues{}) || view.Count() != 1 {
				probe <- fmt.Errorf("callback snapshot = count %d, issues %+v", view.Count(), issues)
				return
			}
			got, err := store.Read(context.Background(), view, 0)
			if err == nil && got != record {
				err = fmt.Errorf("callback read = %+v, want %+v", got, record)
			}
			probe <- err
		}()
		select {
		case err := <-probe:
			completed <- err
		case <-time.After(2 * time.Second):
			completed <- errors.New("completion callback retained the history file lock")
		}
	}); err != nil {
		t.Fatal(err)
	}
	completionErr := waitHistoryCompletion(t, completed)
	store.Close()
	waitHistorySignal(t, probeFinished, "callback read probe")
	if completionErr != nil {
		t.Fatal(completionErr)
	}
	select {
	case err := <-completed:
		t.Fatalf("append completed twice: %v", err)
	default:
	}
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, []Record{record}) {
		t.Fatalf("callback-visible record on disk = %+v", got)
	}
}
