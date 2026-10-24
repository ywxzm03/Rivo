package subagent

import (
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/session"
	"sort"
)

// ContentReplacement 是 Rivo 对 Claude contentReplacementState 条目的等价物:
// Content 为 nil 表示目标消息在投影中被省略。
type ContentReplacement struct {
	TargetEntryID string
	Content       []llm.Content
}

type AgentTranscript struct {
	Messages            []llm.Message
	ContentReplacements []ContentReplacement
	LeafEntryID         string
	LastEntryID         string
}

// ReadAgentTranscript 在追加任何 continuation prompt 之前重建活跃的
// sidechain 投影。context 编辑由 SessionManager 施加,并可通过
// ContentReplacements 观察到。
func ReadAgentTranscript(path, cwd string) (AgentTranscript, error) {
	sm, err := session.Open(path, cwd)
	if err != nil {
		return AgentTranscript{}, err
	}
	view := sm.BuildContext()
	result := AgentTranscript{Messages: view.Messages(), LeafEntryID: sm.LeafEntryID(), LastEntryID: sm.LastEntryID()}
	targets := make([]string, 0, len(view.ContentReplacements))
	for target := range view.ContentReplacements {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		content := view.ContentReplacements[target]
		result.ContentReplacements = append(result.ContentReplacements, ContentReplacement{TargetEntryID: target, Content: append([]llm.Content(nil), content...)})
	}
	return result, nil
}
