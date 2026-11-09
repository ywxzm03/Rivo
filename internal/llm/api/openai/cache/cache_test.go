package cache

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// 只有 openai 且 gpt-5.6 及以上的模型支持 prompt_cache_options
func TestSupportsPromptCacheOptions(t *testing.T) {
	tests := []struct {
		name  string
		model llm.Model
		want  bool
	}{
		{"gpt-5.6 支持", llm.Model{Provider: "openai", ID: "gpt-5.6"}, true},
		{"gpt-5.6 高版本支持", llm.Model{Provider: "openai", ID: "gpt-5.10"}, true},
		{"gpt-6 支持", llm.Model{Provider: "openai", ID: "gpt-6"}, true},
		{"gpt-5.5 不支持", llm.Model{Provider: "openai", ID: "gpt-5.5"}, false},
		{"gpt-5 不支持", llm.Model{Provider: "openai", ID: "gpt-5"}, false},
		{"非 openai 厂商不支持", llm.Model{Provider: "deepseek", ID: "gpt-5.6"}, false},
		{"非 gpt 前缀不支持", llm.Model{Provider: "openai", ID: "o3"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsPromptCacheOptions(tt.model); got != tt.want {
				t.Errorf("SupportsPromptCacheOptions(%s/%s) = %v, want %v", tt.model.Provider, tt.model.ID, got, tt.want)
			}
		})
	}
}

// cache key 超过 64 个 code point 时截断，短 key 原样返回
func TestPromptCacheKeyTruncatesTo64Runes(t *testing.T) {
	short := strings.Repeat("a", 64)
	if got := PromptCacheKey(short); got != short {
		t.Errorf("PromptCacheKey(64 runes) = %q, want unchanged", got)
	}

	long := strings.Repeat("a", 65)
	if got := PromptCacheKey(long); len([]rune(got)) != 64 {
		t.Errorf("PromptCacheKey(65 runes) length = %d, want 64", len([]rune(got)))
	}

	// 多字节字符按 code point 计数，不能按字节截断
	multi := strings.Repeat("中", 65)
	got := PromptCacheKey(multi)
	if len([]rune(got)) != 64 || !strings.HasPrefix(multi, got) {
		t.Errorf("PromptCacheKey(multibyte) = %q, want 64 whole runes", got)
	}
}

func TestGPTVersion(t *testing.T) {
	tests := []struct {
		id           string
		major, minor int
		ok           bool
	}{
		{"gpt-5.6", 5, 6, true},
		{"gpt-5", 5, 0, true},
		{"GPT-4.1", 4, 1, true},
		{"gpt-5.6-turbo", 5, 6, true},
		{"gpt-", 0, 0, false},
		{"o3", 0, 0, false},
		{"gpt-x", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			major, minor, ok := GPTVersion(tt.id)
			if ok != tt.ok || major != tt.major || minor != tt.minor {
				t.Errorf("GPTVersion(%q) = (%d, %d, %v), want (%d, %d, %v)", tt.id, major, minor, ok, tt.major, tt.minor, tt.ok)
			}
		})
	}
}
