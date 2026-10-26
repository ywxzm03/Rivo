package filehistory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// DecodeRecord 必须能解出两类记录，并对未知类型与坏数据报错
func TestDecodeRecord(t *testing.T) {
	snapshot := SnapshotRecord{
		Type:      TypeSnapshot,
		MessageID: "u1",
		Snapshot:  Snapshot{MessageID: "u1"},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeRecord(TypeSnapshot, body)
	if err != nil {
		t.Fatalf("DecodeRecord(snapshot) error = %v", err)
	}
	decoded, ok := record.(SnapshotRecord)
	if !ok {
		t.Fatalf("DecodeRecord(snapshot) = %T, want SnapshotRecord", record)
	}
	if decoded.MessageID != "u1" {
		t.Errorf("MessageID = %q, want u1", decoded.MessageID)
	}

	delta := DeltaRecord{Type: TypeDelta, MessageID: "u2", SnapshotMessageID: "u2", TrackingPath: "/tmp/a"}
	body, err = json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	record, err = DecodeRecord(TypeDelta, body)
	if err != nil {
		t.Fatalf("DecodeRecord(delta) error = %v", err)
	}
	if _, ok := record.(DeltaRecord); !ok {
		t.Fatalf("DecodeRecord(delta) = %T, want DeltaRecord", record)
	}

	if _, err := DecodeRecord("unknown", []byte("{}")); err == nil {
		t.Error("DecodeRecord(unknown) error = nil, want error")
	}
	if _, err := DecodeRecord(TypeSnapshot, []byte("not json")); err == nil {
		t.Error("DecodeRecord(bad json) error = nil, want error")
	}
}

// 空活跃集合必须清空全部运行态快照与已跟踪文件
func TestPruneActiveBranchClearsWhenNoActiveIDs(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", file); err != nil {
		t.Fatal(err)
	}
	if len(store.Snapshots()) == 0 {
		t.Fatal("no snapshot recorded")
	}
	store.PruneActiveBranch(nil)
	if got := store.Snapshots(); len(got) != 0 {
		t.Fatalf("Snapshots() = %d after prune, want 0", len(got))
	}
}

// 只有活跃路径上的快照被保留，已跟踪文件集合随之重建
func TestPruneActiveBranchKeepsOnlyActiveIDs(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	for _, file := range []string{first, second} {
		if err := os.WriteFile(file, []byte("before"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", first); err != nil {
		t.Fatal(err)
	}
	if err := store.MakeSnapshot("u2"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u2", second); err != nil {
		t.Fatal(err)
	}

	store.PruneActiveBranch(map[string]bool{"u1": true})
	snapshots := store.Snapshots()
	if len(snapshots) != 1 || snapshots[0].MessageID != "u1" {
		t.Fatalf("Snapshots() = %+v, want only u1", snapshots)
	}
	if _, ok := snapshots[0].TrackedFileBackups[first]; !ok {
		t.Errorf("kept snapshot lacks %q backup", first)
	}
}

// 目标快照没有该文件的备份时，必须回退到更早的快照
func TestRestoreFallsBackToEarlierBackup(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "tracked.txt")
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := New(root, "session", nil, nil, nil)
	if err := store.MakeSnapshot("u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrackEdit(context.Background(), "u1", file); err != nil {
		t.Fatal(err)
	}
	// u2 快照本身不记录该文件，恢复时必须沿更早的快照找备份
	if err := store.MakeSnapshot("u2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := store.Restore("u2")
	if err != nil {
		t.Fatalf("Restore(u2) error = %v", err)
	}
	if len(result.FilesChanged) == 0 {
		t.Fatalf("Restore() = %+v, want the earlier backup reused", result)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("restored content = %q, want the original bytes", data)
	}
}
