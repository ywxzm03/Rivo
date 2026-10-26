package llm_test

import (
	"slices"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// ThinkingLevels 是思考等级顺序的唯一来源：所有需要「从低到高」列表的地方都必须复用它
func TestThinkingLevelsIsSingleOrderedSource(t *testing.T) {
	want := []llm.ThinkingLevel{
		llm.ThinkingLevelOff, llm.ThinkingLevelMinimal, llm.ThinkingLevelLow,
		llm.ThinkingLevelMedium, llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax,
	}
	if !slices.Equal(llm.ThinkingLevels, want) {
		t.Fatalf("ThinkingLevels = %v, want %v", llm.ThinkingLevels, want)
	}
}

// 每个等级都必须通过 ValidThinkingLevel，避免列表与校验漂移
func TestThinkingLevelsAllValid(t *testing.T) {
	for _, level := range llm.ThinkingLevels {
		if !llm.ValidThinkingLevel(level) {
			t.Errorf("ValidThinkingLevel(%q) = false, want true", level)
		}
	}
}

// ThinkingLevelList 的文本必须与 ThinkingLevels 顺序一致
func TestThinkingLevelListMatchesOrder(t *testing.T) {
	got := llm.ThinkingLevelList()
	want := ""
	for i, level := range llm.ThinkingLevels {
		if i > 0 {
			want += ", "
		}
		want += string(level)
	}
	if got != want {
		t.Errorf("ThinkingLevelList() = %q, want %q", got, want)
	}
}
