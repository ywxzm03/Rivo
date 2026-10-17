package filehistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreTracksVersionsAndRestoresUserTurn(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	var records []Record
	store := New(root, "session", nil, nil, func(record Record) error {
		records = append(records, record)
		return nil
	})
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.MakeSnapshot("u2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("latest"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.Restore("u1")
	if err != nil {
		t.Fatalf("Restore() error = %v, result = %+v", err, result)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before" {
		t.Fatalf("restored content = %q, want before", content)
	}
	if len(result.FilesChanged) != 1 || len(records) != 3 {
		t.Fatalf("result = %+v, records = %d, want one changed file and snapshot/delta/snapshot", result, len(records))
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("restored mode = %o, want 640", got)
	}
}

func TestStoreDeletesFilesCreatedDuringUserTurn(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "new.txt")
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore("u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file stat error = %v, want not exist", err)
	}
}

func TestStoreEvictsOldestSnapshots(t *testing.T) {
	store := New(t.TempDir(), "session", nil, nil, nil)
	for i := 0; i < MaxSnapshots+1; i++ {
		if err := store.MakeSnapshot(string(rune('a' + i))); err != nil {
			t.Fatal(err)
		}
	}
	snapshots := store.Snapshots()
	if len(snapshots) != MaxSnapshots {
		t.Fatalf("snapshot count = %d, want %d", len(snapshots), MaxSnapshots)
	}
	if got := snapshots[0].MessageID; got != string(rune('a'+1)) {
		t.Fatalf("oldest retained message = %q, want %q", got, string(rune('a'+1)))
	}
}

func TestStoreReloadsSnapshotAndDeltaRecords(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	var records []Record
	first := New(root, "session", nil, nil, func(record Record) error {
		records = append(records, record)
		return nil
	})
	if err := first.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := first.TrackEdit(context.Background(), "u1", file); err != nil {
		t.Fatal(err)
	}
	reloaded := New(root, "session", records, nil, nil)
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Restore("u1"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before" {
		t.Fatalf("reloaded restored content = %q, want before", content)
	}
}

func TestStoreRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", link); err == nil {
		t.Fatal("TrackEdit() on symlink returned nil")
	}
}
