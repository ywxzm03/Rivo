//go:build unix

package inputhistory

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const historyProcessRows = 24

func historyProcessRecord(session string, index int) Record {
	return Record{
		SessionID: session,
		Timestamp: int64(index),
		Text:      fmt.Sprintf("%s:%02d\n", session, index) + strings.Repeat("并发🦜\n", 8192),
	}
}

// 子进程通过管道握手，确保竞争发生在真实进程之间
func TestHistoryProcessHelper(t *testing.T) {
	role := os.Getenv("RIVO_INPUT_HISTORY_TEST_ROLE")
	if role == "" {
		return
	}
	path := os.Getenv("RIVO_INPUT_HISTORY_TEST_PATH")
	input := bufio.NewReader(os.Stdin)
	switch role {
	case "append":
		store := New(path)
		defer store.Close()
		if _, err := fmt.Fprintln(os.Stdout, "HISTORY_READY"); err != nil {
			t.Fatal(err)
		}
		if _, err := input.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		completed := make(chan error, historyProcessRows*2)
		session := os.Getenv("RIVO_INPUT_HISTORY_TEST_SESSION")
		for index := range historyProcessRows {
			if err := store.Append(historyProcessRecord(session, index), func(err error) { completed <- err }); err != nil {
				t.Fatal(err)
			}
		}
		store.Close()
		for range historyProcessRows {
			select {
			case err := <-completed:
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("Close omitted accepted completion")
			}
		}
		select {
		case err := <-completed:
			t.Fatalf("duplicate completion: %v", err)
		default:
		}
	case "lock":
		file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Errorf("Close after lock failure: %v", closeErr)
			}
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "HISTORY_READY"); err != nil {
			t.Error(err)
		}
		if _, err := input.ReadString('\n'); err != nil {
			t.Error(err)
		}
		if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	default:
		t.Fatalf("unknown history helper role %q", role)
	}
}

// 不嵌入 Buffer，避免 io.Copy 通过提升的 ReadFrom 绕过握手检测
type historyReadyOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	ready  chan struct{}
	once   sync.Once
}

func (output *historyReadyOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	n, err := output.buffer.Write(data)
	if bytes.Contains(output.buffer.Bytes(), []byte("HISTORY_READY\n")) {
		output.once.Do(func() { close(output.ready) })
	}
	return n, err
}

func (output *historyReadyOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

type historyProcess struct {
	input  io.WriteCloser
	done   chan struct{}
	output *historyReadyOutput
	err    error
	cancel context.CancelFunc
}

func startHistoryProcess(t *testing.T, role, path, session string) *historyProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestHistoryProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(),
		"RIVO_INPUT_HISTORY_TEST_ROLE="+role,
		"RIVO_INPUT_HISTORY_TEST_PATH="+path,
		"RIVO_INPUT_HISTORY_TEST_SESSION="+session,
	)
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	process := &historyProcess{input: input, done: make(chan struct{}), output: &historyReadyOutput{ready: make(chan struct{})}, cancel: cancel}
	command.Stdout = process.output
	command.Stderr = process.output
	if err := command.Start(); err != nil {
		cancel()
		if closeErr := input.Close(); closeErr != nil {
			t.Errorf("Close helper stdin: %v", closeErr)
		}
		t.Fatal(err)
	}
	go func() {
		process.err = command.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-process.done:
		case <-time.After(historyTestTimeout):
			t.Error("history subprocess failed to exit after cancellation")
		}
	})
	select {
	case <-process.output.ready:
		return process
	case <-process.done:
		t.Fatalf("history subprocess exited before ready: %v\n%s", process.err, process.output.String())
	case <-time.After(historyTestTimeout):
		cancel()
		select {
		case <-process.done:
			t.Fatalf("history subprocess did not become ready: %v\n%s", process.err, process.output.String())
		case <-time.After(historyTestTimeout):
			t.Fatalf("history subprocess did not exit after readiness timeout\n%s", process.output.String())
		}
	}
	return nil
}

func releaseHistoryProcess(t *testing.T, process *historyProcess) {
	t.Helper()
	if _, err := io.WriteString(process.input, "continue\n"); err != nil {
		t.Fatalf("release history subprocess: %v", err)
	}
	if err := process.input.Close(); err != nil {
		t.Fatalf("close history subprocess stdin: %v", err)
	}
}

func waitHistoryProcess(t *testing.T, process *historyProcess) {
	t.Helper()
	waitHistorySignal(t, process.done, "history subprocess exit")
	if process.err != nil {
		t.Fatalf("history subprocess failed: %v\n%s", process.err, process.output.String())
	}
}

func TestConcurrentProcessesAppendCompleteRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	first := startHistoryProcess(t, "append", path, "process-A")
	second := startHistoryProcess(t, "append", path, "process-B")
	releaseHistoryProcess(t, first)
	releaseHistoryProcess(t, second)
	waitHistoryProcess(t, first)
	waitHistoryProcess(t, second)
	records := historyFileRecords(t, path)
	if len(records) != historyProcessRows*2 {
		t.Fatalf("persisted %d rows, want %d", len(records), historyProcessRows*2)
	}
	next := map[string]int{"process-A": 0, "process-B": 0}
	for row, record := range records {
		index, ok := next[record.SessionID]
		if !ok || index >= historyProcessRows {
			t.Fatalf("unexpected or duplicate row %d: session=%q, ts=%d", row, record.SessionID, record.Timestamp)
		}
		if want := historyProcessRecord(record.SessionID, index); record != want {
			t.Fatalf("row %d is interleaved, truncated, missing, or out of FIFO order: session=%q, ts=%d, want ts=%d", row, record.SessionID, record.Timestamp, index)
		}
		next[record.SessionID]++
	}
	if next["process-A"] != historyProcessRows || next["process-B"] != historyProcessRows {
		t.Fatalf("per-process persisted rows = %v", next)
	}
	store := New(path)
	defer store.Close()
	assertHistoryView(t, store, historySnapshot(t, store), records)
}

func TestLockContentionIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	prefix := Record{SessionID: "old", Timestamp: 0, Text: "committed"}
	original := writeHistoryRecords(t, path, prefix)
	store := New(path)
	defer store.Close()
	holder := startHistoryProcess(t, "lock", path, "")
	defer holder.cancel()
	completed := make(chan error, 4)
	accepted := make(chan error, 1)
	start := time.Now()
	go func() {
		accepted <- store.Append(Record{SessionID: "blocked", Timestamp: 0, Text: "must fail"}, func(err error) { completed <- err })
	}()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("Append performed synchronous lock I/O: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Append blocked behind another process's file lock")
	}
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("append succeeded while another process held the exclusive lock")
		}
		if elapsed := time.Since(start); elapsed < 700*time.Millisecond {
			t.Fatalf("lock retries ended after %v, want the specified bounded retry window", elapsed)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("append lock wait exceeded its bounded retry window")
	}
	if got := historyFileBytes(t, path); !bytes.Equal(got, original) {
		t.Fatal("failed lock acquisition changed committed file bytes")
	}
	releaseHistoryProcess(t, holder)
	waitHistoryProcess(t, holder)
	store.Barrier()
	after := Record{SessionID: "after", Timestamp: 0, Text: "worker recovers"}
	appendHistoryRecord(t, store, after)
	store.Close()
	select {
	case err := <-completed:
		t.Fatalf("failed append completed twice: %v", err)
	default:
	}
	if got := historyFileRecords(t, path); !reflect.DeepEqual(got, []Record{prefix, after}) {
		t.Fatalf("history after contention = %+v", got)
	}
}

func TestSnapshotAndReadCancelDuringLockWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	record := Record{SessionID: "s", Timestamp: 0, Text: "prefix"}
	original := writeHistoryRecords(t, path, record)
	store := New(path)
	defer store.Close()
	view := historySnapshot(t, store)
	holder := startHistoryProcess(t, "lock", path, "")
	defer holder.cancel()
	for _, operation := range []string{"Snapshot", "Read"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			completed := make(chan error, 1)
			go func() {
				if operation == "Snapshot" {
					_, _, err := store.Snapshot(ctx)
					completed <- err
					return
				}
				_, err := store.Read(ctx, view, 0)
				completed <- err
			}()
			select {
			case err := <-completed:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%s() error = %v, want context deadline", operation, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s ignored cancellation during lock wait", operation)
			}
		})
	}
	releaseHistoryProcess(t, holder)
	waitHistoryProcess(t, holder)
	assertHistoryView(t, store, view, []Record{record})
	if !bytes.Equal(historyFileBytes(t, path), original) {
		t.Fatal("canceled reads changed persisted history")
	}
}

func TestAppendUsesPrivateUnixPermissions(t *testing.T) {
	t.Run("new-parent-and-file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "new-private-history")
		path := filepath.Join(parent, "history.jsonl")
		store := New(path)
		defer store.Close()
		appendHistoryRecord(t, store, Record{SessionID: "s", Timestamp: 0, Text: "private"})
		for target, want := range map[string]os.FileMode{parent: 0o700, path: 0o600} {
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s permissions = %o, want %o", target, got, want)
			}
		}
	})
	t.Run("existing-history-only", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "history.jsonl")
		other := filepath.Join(root, "config.json")
		for _, target := range []string{path, other} {
			if err := os.WriteFile(target, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		store := New(path)
		defer store.Close()
		record := Record{SessionID: "s", Timestamp: 0, Text: "tighten existing history"}
		appendHistoryRecord(t, store, record)
		for target, want := range map[string]os.FileMode{path: 0o600, other: 0o644} {
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s permissions = %o, want %o", target, got, want)
			}
		}
		if got := historyFileRecords(t, path); !reflect.DeepEqual(got, []Record{record}) {
			t.Fatalf("history after permission update = %+v", got)
		}
	})
}

func TestAppendReportsReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses Unix directory write permissions")
	}
	parent := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(parent, "history.jsonl")
	store := New(path)
	defer store.Close()
	completed := make(chan error, 2)
	if err := store.Append(Record{SessionID: "s", Timestamp: 0, Text: "cannot save"}, func(err error) { completed <- err }); err != nil {
		t.Fatal(err)
	}
	if err := waitHistoryCompletion(t, completed); err == nil {
		t.Fatal("append to read-only directory reported success")
	}
	store.Close()
	select {
	case err := <-completed:
		t.Fatalf("failed append completed twice: %v", err)
	default:
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed append created history: stat error = %v", err)
	}
}
