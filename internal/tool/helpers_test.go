package tool

import (
	"strings"
	"testing"
)

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int
		want  string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{51200, "50.0KB"},
		{1024 * 1024, "1.0MB"},
		{3 * 1024 * 1024 / 2, "1.5MB"},
	}
	for _, tt := range tests {
		if got := formatSize(tt.bytes); got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestTruncateHead(t *testing.T) {
	t.Run("returns content unchanged when below the limit", func(t *testing.T) {
		got := truncateHead("a\nb\nc", 10, 100)
		if got.Truncated || got.Content != "a\nb\nc" || got.OutputLines != 3 {
			t.Errorf("got = %+v", got)
		}
	})

	t.Run("trailing blank lines are not counted", func(t *testing.T) {
		got := truncateHead("a\nb\nc\n", 3, 100)
		if got.Truncated || got.TotalLines != 3 {
			t.Errorf("got = %+v, want untruncated with TotalLines=3", got)
		}
	})

	t.Run("counts UTF-8 bytes", func(t *testing.T) {
		// "éé" 占 4 字节，两行共 4 + (3+1) = 8 字节
		got := truncateHead("éé\nabc", 10, 4)
		if got.Content != "éé" || got.TruncatedBy != truncatedByBytes {
			t.Errorf("got = %+v, want Content=éé truncated by bytes", got)
		}
	})

	t.Run("returns empty content when the first line alone exceeds the limit", func(t *testing.T) {
		got := truncateHead("éé\nabc", 10, 3)
		if got.Content != "" || !got.FirstLineExceedsLimit || got.OutputLines != 0 {
			t.Errorf("got = %+v", got)
		}
	})

	t.Run("truncates by line count", func(t *testing.T) {
		got := truncateHead(strings.Join(numberedLines(2500), "\n"), 2000, 50*1024)
		if got.OutputLines != 2000 || got.TruncatedBy != truncatedByLines {
			t.Errorf("OutputLines = %d, TruncatedBy = %q", got.OutputLines, got.TruncatedBy)
		}
	})

	t.Run("a large maxLines effectively disables the line limit", func(t *testing.T) {
		// ls / find / grep 那类工具只要字节上限时就是这么用的
		lines := []string{"aaaa", "bbbb", "cccc"}
		got := truncateHead(strings.Join(lines, "\n"), 1<<31-1, 8)
		if got.Content != "aaaa" || got.TruncatedBy != truncatedByBytes {
			t.Errorf("got = %+v, want truncation by bytes only", got)
		}
	})

	t.Run("never splits a line in half", func(t *testing.T) {
		lines := []string{"aaaa", "bbbbbb", "cc", "dddddddd"}
		content := strings.Join(lines, "\n")
		for _, maxBytes := range []int{0, 1, 5, 8, 17, 64, 1024} {
			got := truncateHead(content, 100, maxBytes)
			if got.FirstLineExceedsLimit {
				continue
			}
			if want := strings.Join(lines[:got.OutputLines], "\n"); got.Content != want {
				t.Errorf("maxBytes=%d: Content = %q, want %q", maxBytes, got.Content, want)
			}
			if len(got.Content) > maxBytes {
				t.Errorf("maxBytes=%d: output is %d bytes, over the limit", maxBytes, len(got.Content))
			}
		}
	})
}

func TestTruncateLine(t *testing.T) {
	if got, truncated := truncateLine("short", 5); truncated || got != "short" {
		t.Errorf("truncateLine(short) = %q, %v", got, truncated)
	}
	if got, truncated := truncateLine("€€€€", 3); !truncated || got != "€€€... [truncated]" {
		t.Errorf("truncateLine(multibyte) = %q, %v", got, truncated)
	}
}
