package tui

import (
	"maps"
	"strings"
)

// snapshotComposerDraft 只收集文本中仍存在的注册标记，不赋予字面标记附件归属
func (m *Model) snapshotComposerDraft(text string, expanded bool) ComposerDraft {
	draft := ComposerDraft{Text: text, PasteCounter: m.composer.pasteCounter, ImageCounter: m.composer.imageCounter}
	if !expanded {
		for id, body := range m.composer.pastes {
			if strings.Contains(text, pasteMarker(id, body)) {
				if draft.Pastes == nil {
					draft.Pastes = make(map[int]string)
				}
				draft.Pastes[id] = body
			}
		}
	}
	for id, image := range m.composer.images {
		if strings.Contains(text, imageMarker(id)) {
			if draft.Images == nil {
				draft.Images = make(map[int]ImagePart)
			}
			draft.Images[id] = image
		}
	}
	return draft
}

// restoreComposerDraft 发布活动副本，后续编辑不能改写已保存条目
func (m *Model) restoreComposerDraft(draft ComposerDraft) {
	m.composer.pastes = maps.Clone(draft.Pastes)
	m.composer.images = maps.Clone(draft.Images)
	m.composer.pasteCounter, m.composer.imageCounter = draft.PasteCounter, draft.ImageCounter
	m.editor.SetValue(draft.Text)
	m.editor.CursorEnd()
	m.composer.completion, m.composer.completionHidden = 0, true
	m.composer.histories.lookup.editorRevision++
	m.layout()
}

func equalComposerDraft(a, b ComposerDraft) bool {
	return a.Text == b.Text && maps.Equal(a.Pastes, b.Pastes) && maps.Equal(a.Images, b.Images)
}

// rememberHistory 合并相邻完整相同项，独立输入的持久身份仍分别生成
func (m *Model) rememberHistory(entry ComposerHistoryEntry) {
	scope := m.currentHistory()
	if len(scope.entries) == 0 || !equalComposerDraft(scope.entries[len(scope.entries)-1].Draft, entry.Draft) {
		scope.entries = append(scope.entries, entry)
	}
	m.resetHistoryNavigation()
}

func (m *Model) nextInputOrigin(text string) *InputOrigin {
	h := &m.composer.histories
	h.sequence++
	return &InputOrigin{Generation: h.persistent.metadata.Generation, Sequence: h.sequence, SessionID: h.activeScope, Text: text}
}

// clearComposerInput 清空活动输入及导航，不恢复被清空前的草稿
func (m *Model) clearComposerInput() {
	m.resetHistoryNavigation()
	m.editor.Reset()
	m.resetPastes()
	m.composer.completion, m.composer.completionHidden = 0, true
	m.composer.histories.lookup.editorRevision++
	m.layout()
}

// recordSubmission 在清空前保存完整条目，并赋予这次手动提交新的身份
func (m *Model) recordSubmission(input string, message ComposerPayload) ComposerPayload {
	m.resumePending = false
	m.rememberHistory(ComposerHistoryEntry{Draft: m.snapshotComposerDraft(input, true)})
	message.Origin = m.nextInputOrigin(input)
	m.clearComposerInput()
	return message
}

// ComposerDraftFromPayload 将恢复会话的文本与附件转成可召回草稿
func ComposerDraftFromPayload(payload ComposerPayload) ComposerDraft {
	var draft ComposerDraft
	appendPayloadDraft(&draft, payload)
	return draft
}

func appendPayloadDraft(draft *ComposerDraft, payload ComposerPayload) {
	if len(payload.Images) == 0 {
		draft.Text = payload.Text
		return
	}
	reserved := imageMarkerIDs(payload.Text)
	if draft.Images == nil {
		draft.Images = make(map[int]ImagePart)
	}
	var builder strings.Builder
	builder.WriteString(payload.Text)
	for _, image := range payload.Images {
		id := nextImageID(&draft.ImageCounter, reserved)
		draft.Images[id] = image
		builder.WriteString(imageMarker(id))
	}
	draft.Text = builder.String()
}
