package tool

import "testing"

// Windows 盘符判定：单字符输入不能越界，盘符大小写都要识别
func TestIsWindowsDrivePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"C:/tmp/a.go", true},
		{"c:/tmp/a.go", true},
		{"Z:\\a.go", true},
		{"/tmp/a.go", false},
		{"1:/tmp", false},
		{"C", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isWindowsDrivePath(tt.path); got != tt.want {
				t.Errorf("isWindowsDrivePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// 行尾归一化必须同时处理 CRLF 与孤立 CR
func TestNormalizeLineEndings(t *testing.T) {
	if got := normalizeLineEndings("a\r\nb\rc\nd"); got != "a\nb\nc\nd" {
		t.Errorf("normalizeLineEndings() = %q, want LF only", got)
	}
}

// 行尾风格按首次出现位置判定
func TestDetectLineEnding(t *testing.T) {
	if got := detectLineEnding("a\r\nb\n"); got != "\r\n" {
		t.Errorf("detectLineEnding(crlf first) = %q, want CRLF", got)
	}
	if got := detectLineEnding("a\nb\r\n"); got != "\n" {
		t.Errorf("detectLineEnding(lf first) = %q, want LF", got)
	}
	if got := detectLineEnding("no newline"); got != "\n" {
		t.Errorf("detectLineEnding(no newline) = %q, want LF", got)
	}
}
