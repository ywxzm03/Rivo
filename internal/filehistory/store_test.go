package filehistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Fatalf("restored mode = %o, want 640", got)
		}
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

func TestStoreRejectsSymlinkBackupDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	backupRoot := filepath.Join(root, "file-history")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(backupRoot, "session")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", file); err == nil {
		t.Fatal("TrackEdit() succeeded through symlink backup directory")
	}
}

func TestTrackEditRejectsMismatchedSnapshotID(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(t.TempDir(), "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u2", file); err == nil {
		t.Fatal("TrackEdit() accepted a mismatched snapshot ID")
	}
	if got := len(store.Snapshots()[0].TrackedFileBackups); got != 0 {
		t.Fatalf("tracked backups = %d, want zero", got)
	}
}

func TestRestoreWithRollbackRestoresOriginalWorkspace(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, rollback, err := store.RestoreWithRollback("u1")
	if err != nil || rollback == nil || len(result.FilesChanged) != 1 {
		t.Fatalf("RestoreWithRollback() = result %+v, rollback %v, err %v", result, rollback != nil, err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "before" {
		t.Fatalf("restored content = %q, err %v", content, err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "after" {
		t.Fatalf("rolled-back content = %q, err %v", content, err)
	}
}

func TestTrackEditContentUsesProvidedData(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("actual"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(t.TempDir(), "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEditContent(context.Background(), "u1", file, []byte("captured"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore("u1"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "captured" {
		t.Fatalf("restored content = %q, err %v", content, err)
	}
}

func TestTrackEditRollsBackStateWhenRecordAppendFails(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls int
	sentinel := errors.New("metadata unavailable")
	store := New(root, "session", nil, nil, func(Record) error {
		calls++
		if calls == 2 {
			return sentinel
		}
		return nil
	})
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", file); !errors.Is(err, sentinel) {
		t.Fatalf("TrackEdit() error = %v, want metadata error", err)
	}
	if got := len(store.Snapshots()[0].TrackedFileBackups); got != 0 {
		t.Fatalf("tracked backups = %d, want zero after append failure", got)
	}
	entries, err := os.ReadDir(filepath.Join(root, "file-history", "session"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("backup directory entries = %d, want zero after rollback", len(entries))
	}
}

func TestMakeSnapshotRollsBackStateWhenRecordAppendFails(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls int
	sentinel := errors.New("metadata unavailable")
	store := New(root, "session", nil, nil, func(Record) error {
		calls++
		if calls == 3 {
			return sentinel
		}
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
	if err := store.MakeSnapshot("u2"); !errors.Is(err, sentinel) {
		t.Fatalf("MakeSnapshot() error = %v, want metadata error", err)
	}
	snapshots := store.Snapshots()
	if len(snapshots) != 1 || snapshots[0].MessageID != "u1" {
		t.Fatalf("snapshots after rollback = %+v, want only u1", snapshots)
	}
	if _, err := os.Stat(filepath.Join(root, "file-history", "session", backupName(file, 2))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back backup stat error = %v, want not exist", err)
	}
}

func TestRestorePreflightsAllFilesBeforeWriting(t *testing.T) {
	root := t.TempDir()
	fileA := filepath.Join(t.TempDir(), "a.txt")
	fileB := filepath.Join(t.TempDir(), "b.txt")
	for _, file := range []string{fileA, fileB} {
		if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{fileA, fileB} {
		if err := store.TrackEdit(context.Background(), "u1", file); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backup := store.Snapshots()[0].TrackedFileBackups[fileB].BackupFileName
	if backup == nil {
		t.Fatal("missing backup for fileB")
	}
	if err := os.Remove(filepath.Join(root, "file-history", "session", *backup)); err != nil {
		t.Fatal(err)
	}
	result, err := store.Restore("u1")
	if err == nil || len(result.FilesFailed) != 1 {
		t.Fatalf("Restore() = result %+v, err %v; want one preflight failure", result, err)
	}
	for _, file := range []string{fileA, fileB} {
		content, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != "after" {
			t.Fatalf("%s content = %q, want unchanged after preflight failure", file, content)
		}
	}
}
