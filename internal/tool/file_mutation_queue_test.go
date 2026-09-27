package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileMutationQueueSerializesSameFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := withFileMutationQueue(context.Background(), path, func() (struct{}, error) {
			close(firstStarted)
			<-releaseFirst
			return struct{}{}, nil
		})
		firstDone <- err
	}()
	<-firstStarted

	secondDone := make(chan error, 1)
	go func() {
		_, err := withFileMutationQueue(context.Background(), path, func() (struct{}, error) {
			close(secondStarted)
			return struct{}{}, nil
		})
		secondDone <- err
	}()
	select {
	case <-secondStarted:
		t.Fatal("same-file mutation started before previous mutation completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestFileMutationQueueRunsDifferentFilesInParallel(t *testing.T) {
	dir := t.TempDir()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for _, name := range []string{"a.txt", "b.txt"} {
		name := name
		go func() {
			defer wg.Done()
			_, _ = withFileMutationQueue(context.Background(), filepath.Join(dir, name), func() (struct{}, error) {
				if name == "a.txt" {
					close(firstStarted)
				} else {
					close(secondStarted)
				}
				<-release
				return struct{}{}, nil
			})
		}()
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first mutation did not start")
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("different-file mutation was blocked")
	}
	close(release)
	wg.Wait()
}

func TestFileMutationQueueReleasesAfterError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	wantErr := errors.New("write failed")
	if _, err := withFileMutationQueue(context.Background(), path, func() (struct{}, error) { return struct{}{}, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("first error = %v, want %v", err, wantErr)
	}
	called := false
	if _, err := withFileMutationQueue(context.Background(), path, func() (struct{}, error) {
		called = true
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("queue did not accept mutation after an error")
	}
}

func TestFileMutationQueueUsesRealpathAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	alias := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	targetKey, err := mutationQueueKey(target)
	if err != nil {
		t.Fatal(err)
	}
	aliasKey, err := mutationQueueKey(alias)
	if err != nil {
		t.Fatal(err)
	}
	if targetKey != aliasKey {
		t.Fatalf("keys differ: target=%q alias=%q", targetKey, aliasKey)
	}
	if _, err := withFileMutationQueue(context.Background(), alias, func() (struct{}, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	mutationQueuesMu.Lock()
	_, exists := mutationQueues[targetKey]
	mutationQueuesMu.Unlock()
	if exists {
		t.Fatal("completed queue was not cleaned up")
	}
}

func TestFileMutationQueueCancellationBeforeCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := withFileMutationQueue(ctx, filepath.Join(t.TempDir(), "file.txt"), func() (struct{}, error) {
		called = true
		return struct{}{}, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("canceled mutation callback was called")
	}
}
