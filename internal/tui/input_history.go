package tui

import (
	"errors"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

type historyNavigation struct {
	cursor   *int
	lastText *string
}

type scopeHistory struct {
	entries     []ComposerHistoryEntry
	replayTexts map[string]struct{}
	navigation  historyNavigation
}

type persistentHistory struct {
	metadata    InputHistoryMetadata
	fetched     map[int]string
	unavailable bool
}

type historyLookupState struct {
	requestID      uint64
	editorRevision uint64
	pending        *HistoryLookupRequest
	anchor         *historyNavigation // 未显示读取前最后确认的位置
	direction      int
}

type composerHistories struct {
	persistent  persistentHistory
	scopes      map[string]*scopeHistory
	activeScope string
	lookup      historyLookupState
	sequence    uint64
	preparing   *ScopePrepareRequest
}

func (m *Model) currentHistory() *scopeHistory {
	h := &m.composer.histories
	if h.scopes == nil {
		h.scopes = make(map[string]*scopeHistory)
	}
	if h.scopes[h.activeScope] == nil {
		h.scopes[h.activeScope] = &scopeHistory{}
	}
	return h.scopes[h.activeScope]
}

func copyHistoryNavigation(n historyNavigation) historyNavigation {
	out := historyNavigation{}
	if n.cursor != nil {
		value := *n.cursor
		out.cursor = &value
	}
	if n.lastText != nil {
		value := *n.lastText
		out.lastText = &value
	}
	return out
}

// cancelHistoryLookup 取消整批未显示读取，恢复确认位置而非逐个回滚请求
func (m *Model) cancelHistoryLookup() {
	h := &m.composer.histories
	if h.lookup.anchor != nil {
		m.currentHistory().navigation = *h.lookup.anchor
	}
	h.lookup.pending, h.lookup.anchor = nil, nil
	h.lookup.requestID++
}

func (m *Model) invalidateHistoryInput() {
	m.cancelHistoryLookup()
	m.composer.histories.preparing = nil
}

func (m *Model) resetHistoryNavigation() {
	m.invalidateHistoryInput()
	m.currentHistory().navigation = historyNavigation{}
}

func (m *Model) historyBoundary() bool {
	line := m.editor.LineInfo()
	column := line.StartColumn + line.ColumnOffset
	if m.editor.Line() == 0 && column == 0 {
		return true
	}
	text := m.editor.Value()
	last := strings.LastIndexByte(text, '\n') + 1
	return m.editor.Line() == m.editor.LineCount()-1 && column == utf8.RuneCountInString(text[last:])
}

func (m *Model) canBrowseHistory() bool {
	h := &m.composer.histories
	scope := m.currentHistory()
	if len(scope.entries) == 0 && (h.persistent.metadata.PersistentCount == 0 || h.persistent.unavailable) {
		return false
	}
	if !m.editor.Focused() || m.panel.taskSelector || m.overlayOpen() || m.hasSelection() {
		return false
	}
	value := m.editor.Value()
	return value == "" || (scope.navigation.lastText != nil && value == *scope.navigation.lastText && m.historyBoundary())
}

func setHistoryCursor(n *historyNavigation, position int) {
	if n.cursor == nil {
		n.cursor = new(int)
	}
	*n.cursor = position
}

func (m *Model) commitHistoryDraft(position int, draft ComposerDraft) {
	h := &m.composer.histories
	h.lookup.pending, h.lookup.anchor = nil, nil
	h.lookup.requestID++
	navigation := &m.currentHistory().navigation
	setHistoryCursor(navigation, position)
	m.restoreComposerDraft(draft)
	if navigation.lastText == nil {
		navigation.lastText = new(string)
	}
	*navigation.lastText = m.editor.Value()
}

// browseHistory 只消费实际存在的候选，最旧边界交回编辑器移动光标
func (m *Model) browseHistory(direction int) (tea.Cmd, bool) {
	if !m.canBrowseHistory() {
		return nil, false
	}
	h := &m.composer.histories
	scope := m.currentHistory()
	count := h.persistent.metadata.PersistentCount
	total := count + len(scope.entries)
	position := total
	if scope.navigation.cursor != nil {
		position = *scope.navigation.cursor
	} else if direction > 0 {
		return nil, false
	}
	for target := position + direction; target >= 0 && target < total; target += direction {
		if target >= count {
			m.commitHistoryDraft(target, scope.entries[target-count].Draft)
			return nil, true
		}
		if h.persistent.unavailable || m.handlers.History.Lookup == nil {
			return nil, false
		}
		if text, ok := h.persistent.fetched[target]; ok {
			if _, repeated := scope.replayTexts[text]; repeated {
				continue
			}
			m.commitHistoryDraft(target, ComposerDraft{Text: text})
			return nil, true
		}
		if h.lookup.anchor == nil {
			anchor := copyHistoryNavigation(scope.navigation)
			h.lookup.anchor = &anchor
		}
		h.lookup.requestID++
		request := HistoryLookupRequest{Generation: h.persistent.metadata.Generation, ScopeID: h.activeScope, RequestID: h.lookup.requestID, Offset: target, EditorRevision: h.lookup.editorRevision}
		setHistoryCursor(&scope.navigation, target)
		h.lookup.pending, h.lookup.direction = &request, direction
		return m.handlers.History.Lookup(request), true
	}
	if direction > 0 && position+direction >= total && scope.navigation.cursor != nil {
		m.clearComposerInput()
		return nil, true
	}
	return nil, false
}

// handleHistoryLookup 缓存同代次合法结果，只有当前资格仍有效时才回填
func (m *Model) handleHistoryLookup(msg HistoryLookupMsg) tea.Cmd {
	h := &m.composer.histories
	if msg.Request.Generation != h.persistent.metadata.Generation {
		return nil
	}
	if msg.Unavailable {
		h.persistent.unavailable = true
		clear(h.persistent.fetched)
		m.cancelHistoryLookup()
	}
	if msg.Err != nil {
		if h.lookup.pending != nil && *h.lookup.pending == msg.Request {
			m.cancelHistoryLookup()
		}
		m.notice("Input history: " + msg.Err.Error())
		return nil
	}
	if msg.Request.Offset < 0 || msg.Request.Offset >= h.persistent.metadata.PersistentCount || h.persistent.unavailable {
		return nil
	}
	if msg.Text == "" {
		return m.handleHistoryLookup(HistoryLookupMsg{Request: msg.Request, Err: errors.New("empty persistent history record")})
	}
	if h.persistent.fetched == nil {
		h.persistent.fetched = make(map[int]string)
	}
	h.persistent.fetched[msg.Request.Offset] = msg.Text
	if h.lookup.pending == nil || *h.lookup.pending != msg.Request {
		return nil
	}
	navigation := &m.currentHistory().navigation
	if h.activeScope != msg.Request.ScopeID || h.lookup.editorRevision != msg.Request.EditorRevision || navigation.cursor == nil || *navigation.cursor != msg.Request.Offset || !m.canBrowseHistory() {
		m.cancelHistoryLookup()
		return nil
	}
	if _, repeated := m.currentHistory().replayTexts[msg.Text]; repeated {
		h.lookup.pending = nil
		cmd, handled := m.browseHistory(h.lookup.direction)
		if !handled {
			m.cancelHistoryLookup()
		}
		return cmd
	}
	m.commitHistoryDraft(msg.Request.Offset, ComposerDraft{Text: msg.Text})
	return nil
}

type historyEditorStamp struct {
	text                                                     string
	line, column, pastes, images, pasteCounter, imageCounter int
	revision                                                 uint64
}

func (m *Model) historyEditorStamp() historyEditorStamp {
	line := m.editor.LineInfo()
	return historyEditorStamp{text: m.editor.Value(), line: m.editor.Line(), column: line.StartColumn + line.ColumnOffset, pastes: len(m.composer.pastes), images: len(m.composer.images), pasteCounter: m.composer.pasteCounter, imageCounter: m.composer.imageCounter, revision: m.composer.histories.lookup.editorRevision}
}

// finishHistoryKey 区分真实编辑、光标移动和历史自身回填
func (m *Model) finishHistoryKey(before historyEditorStamp) {
	if before.revision != m.composer.histories.lookup.editorRevision {
		return
	}
	after := m.historyEditorStamp()
	if before.text != after.text || before.pastes != after.pastes || before.images != after.images || before.pasteCounter != after.pasteCounter || before.imageCounter != after.imageCounter {
		m.invalidateHistoryInput()
		m.composer.histories.lookup.editorRevision++
	} else if before.line != after.line || before.column != after.column {
		m.invalidateHistoryInput()
	}
}
