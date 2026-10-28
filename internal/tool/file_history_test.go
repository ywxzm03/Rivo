package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

type recordingFileHistory struct {
	messageID string
	path      string
}

func (r *recordingFileHistory) TrackEdit(_ context.Context, messageID, path string) error {
	r.messageID, r.path = messageID, path
	return nil
}

func TestWriteTracksBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingFileHistory{}
	ctx := llm.WithCurrentUserMessageID(context.Background(), "u1")
	if _, err := (&Write{History: recorder}).Execute(ctx, map[string]any{"path": path, "content": "after"}); err != nil {
		t.Fatal(err)
	}
	if recorder.messageID != "u1" || recorder.path != path {
		t.Fatalf("tracker = %+v, want user u1 and %q", recorder, path)
	}
}

func TestEditTracksBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingFileHistory{}
	ctx := llm.WithCurrentUserMessageID(context.Background(), "u2")
	_, err := (&Edit{History: recorder}).Execute(ctx, map[string]any{
		"path":  path,
		"edits": []any{map[string]any{"oldText": "before", "newText": "after"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.messageID != "u2" || recorder.path != path {
		t.Fatalf("tracker = %+v, want user u2 and %q", recorder, path)
	}
}

func TestEditDoesNotTrackFailedReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingFileHistory{}
	ctx := llm.WithCurrentUserMessageID(context.Background(), "u3")
	if _, err := (&Edit{History: recorder}).Execute(ctx, map[string]any{
		"path":  path,
		"edits": []any{map[string]any{"oldText": "missing", "newText": "after"}},
	}); err == nil {
		t.Fatal("failed replacement returned nil error")
	}
	if recorder.messageID != "" || recorder.path != "" {
		t.Fatalf("tracker = %+v, want no call for failed replacement", recorder)
	}
}

func TestFileHistoryTrackerIsSkippedWithoutUserID(t *testing.T) {
	recorder := &recordingFileHistory{}
	path := filepath.Join(t.TempDir(), "file.txt")
	if _, err := (&Write{History: recorder}).Execute(context.Background(), map[string]any{"path": path, "content": "new"}); err != nil {
		t.Fatal(err)
	}
	if recorder.messageID != "" || recorder.path != "" {
		t.Fatalf("tracker = %+v, want no call", recorder)
	}
}
