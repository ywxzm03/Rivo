package tui

import tea "github.com/charmbracelet/bubbletea"

// installInputHistory 仅在新代次重置全部 scope，同代次快照不重复播种
func (m *Model) installInputHistory(state *InputHistoryState) {
	if state == nil {
		return
	}
	h := &m.composer.histories
	changed := h.persistent.metadata.Generation != state.Metadata.Generation
	if state.Metadata.Generation < h.persistent.metadata.Generation || (changed && !state.ResetAll) {
		return
	}
	if changed {
		revision := h.lookup.editorRevision + 1
		m.invalidateHistoryInput()
		*h = composerHistories{persistent: persistentHistory{metadata: state.Metadata, fetched: make(map[int]string)}, scopes: make(map[string]*scopeHistory), lookup: historyLookupState{editorRevision: revision}}
		m.editor.Reset()
		m.resetPastes()
		m.composer.completionHidden = true
	}
	id := state.ScopeID
	if id == "" {
		id = state.Metadata.SessionID
	}
	if h.activeScope != id {
		m.invalidateHistoryInput()
		h.activeScope = id
	}
	if h.scopes == nil {
		h.scopes = make(map[string]*scopeHistory)
	}
	_, exists := h.scopes[id]
	if !exists {
		scope := &scopeHistory{replayTexts: make(map[string]struct{})}
		for _, entry := range state.Replay {
			if len(scope.entries) == 0 || !equalComposerDraft(scope.entries[len(scope.entries)-1].Draft, entry.Draft) {
				scope.entries = append(scope.entries, entry)
			}
			if entry.Replayed {
				scope.replayTexts[entry.Draft.Text] = struct{}{}
			}
		}
		h.scopes[id] = scope
	}
	if changed || !exists {
		for _, warning := range state.Warnings {
			m.notice("Input history: " + warning)
		}
	}
}

func (m *Model) activateHistoryScope(id string) {
	if m.composer.histories.activeScope != id {
		m.invalidateHistoryInput()
		m.composer.histories.activeScope = id
	}
	m.currentHistory()
}

func (m *Model) selectedHistoryScope() string {
	if m.panel.mainSelected || m.panel.selectedTaskID == "" {
		return m.composer.histories.persistent.metadata.SessionID
	}
	return m.panel.selectedTaskID
}

// prepareHistoryScope 首次入口只读取种子，尚不改变可见视图与后端输入目标
func (m *Model) prepareHistoryScope(id string) (tea.Cmd, bool) {
	h := &m.composer.histories
	if _, exists := h.scopes[id]; exists || m.handlers.History.PrepareScope == nil {
		m.activateHistoryScope(id)
		return nil, false
	}
	m.invalidateHistoryInput()
	request := ScopePrepareRequest{Generation: h.persistent.metadata.Generation, RequestID: h.lookup.requestID, ScopeID: id, EditorRevision: h.lookup.editorRevision}
	h.preparing = &request
	return m.handlers.History.PrepareScope(request), true
}

func (m *Model) handleScopePrepared(msg ScopePreparedMsg) tea.Cmd {
	h := &m.composer.histories
	if h.preparing == nil || *h.preparing != msg.Request || h.persistent.metadata.Generation != msg.Request.Generation || h.lookup.editorRevision != msg.Request.EditorRevision || !m.panel.taskSelector || m.editor.Value() != "" || m.selectedHistoryScope() != msg.Request.ScopeID {
		return nil
	}
	h.preparing = nil
	if msg.Err != nil {
		m.notice("Input history: " + msg.Err.Error())
		return nil
	}
	m.installInputHistory(&InputHistoryState{Metadata: h.persistent.metadata, ScopeID: msg.Request.ScopeID, Replay: msg.Replay})
	return m.enterSelectedSubagent()
}

// reconcileHistoryScopes 释放已驱逐任务持有的历史与附件
func (m *Model) reconcileHistoryScopes(tasks []SubagentTaskView) {
	h := &m.composer.histories
	for id := range h.scopes {
		if id != h.persistent.metadata.SessionID && !hasSubagentTask(tasks, id) {
			delete(h.scopes, id)
		}
	}
}

func (m *Model) canFocusHistoryTasks() bool {
	return len(m.state.Subagents.Tasks) > 0 && m.editor.Focused() && m.editor.Value() == "" && len(m.composer.images) == 0 && len(m.composer.pastes) == 0 && !m.overlayOpen() && len(m.completionItems()) == 0
}
