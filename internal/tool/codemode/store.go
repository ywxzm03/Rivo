package codemode

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const (
	maxStoreValueBytes = 256 * 1024
	maxStoreBytes      = 1024 * 1024
)

// storeState 只保存 JSON 文本，load 总是重新解析独立对象
type storeState struct {
	values     map[string]json.RawMessage // 祖先记录与本批结果合并后的当前值
	changes    map[string]json.RawMessage // 本次最后一次操作，nil 表示删除
	totalBytes int                        // 键与值的 UTF-8 字节总数
}

// restore 从原始分支和本批尚未提交的结果依次重建状态
func (s *storeState) restore(history History, earlier []llm.ToolResultMessage, sessionID string) error {
	s.values = make(map[string]json.RawMessage)
	s.changes = make(map[string]json.RawMessage)
	entries, err := history.BranchToolDetails("codemode")
	if err != nil {
		return fmt.Errorf("codemode: read branch store: %w", err)
	}
	for _, raw := range entries {
		var details Details
		if err := json.Unmarshal(raw, &details); err != nil {
			return fmt.Errorf("codemode: decode branch store: %w", err)
		}
		s.apply(details.StoreWrites, sessionID)
	}
	for _, message := range earlier {
		if message.ToolName != "codemode" || message.IsError || message.Details == nil {
			continue
		}
		raw, err := json.Marshal(message.Details)
		if err != nil {
			return fmt.Errorf("codemode: encode earlier store: %w", err)
		}
		var details Details
		if err := json.Unmarshal(raw, &details); err != nil {
			return fmt.Errorf("codemode: decode earlier store: %w", err)
		}
		s.apply(details.StoreWrites, sessionID)
	}
	for key, value := range s.values {
		s.totalBytes += len(key) + len(value)
	}
	return nil
}

// apply 仅接收当前会话记录，fork 复制的父会话写入不参与合并
func (s *storeState) apply(writes *StoreWrites, sessionID string) {
	if writes == nil || writes.SessionID != sessionID {
		return
	}
	for _, key := range writes.Delete {
		delete(s.values, key)
	}
	for key, value := range writes.Set {
		s.values[key] = value
	}
}

// put 先计算限制再更新，超限不会改变当前值或待提交操作
func (s *storeState) put(key string, value json.RawMessage) error {
	if len(value) > maxStoreValueBytes {
		return fmt.Errorf("codemode: store value exceeds %d bytes", maxStoreValueBytes)
	}
	previous, exists := s.values[key]
	next := s.totalBytes
	if exists {
		next -= len(key) + len(previous)
	}
	if value != nil {
		next += len(key) + len(value)
	}
	if next > maxStoreBytes {
		return fmt.Errorf("codemode: store exceeds %d bytes", maxStoreBytes)
	}
	if value == nil {
		delete(s.values, key)
	} else {
		s.values[key] = value
	}
	s.changes[key] = value
	s.totalBytes = next
	return nil
}

// writes 只在成功结果形成时生成一次最终变更
func (s *storeState) writes(sessionID string) *StoreWrites {
	if len(s.changes) == 0 {
		return nil
	}
	writes := &StoreWrites{SessionID: sessionID, Set: make(map[string]json.RawMessage), Delete: []string{}}
	for key, value := range s.changes {
		if value == nil {
			writes.Delete = append(writes.Delete, key)
		} else {
			writes.Set[key] = value
		}
	}
	sort.Strings(writes.Delete)
	return writes
}
