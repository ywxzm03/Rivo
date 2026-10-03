package session

import "github.com/ywxzm03/Rivo/internal/llm"

// sessionSink 维护 agent 消息序与 session entry ID 的镜像映射：Agent 每条消息都伴随一个
// message_end 事件且按序到达，liveIDs 因此与 Agent 上下文的消息列表一一同序对应
type sessionSink struct {
	m       *SessionManager
	next    llm.AgentEventSink
	liveIDs []string
}

// Sink 返回接入 SessionManager 的事件 sink：message_end 先追加进会话再转发给 next，
// 持久化失败返回错误以终止当前 Agent 调用
func Sink(m *SessionManager, next llm.AgentEventSink) llm.AgentEventSink {
	s := &sessionSink{m: m, next: next}
	return s.handle
}

func (s *sessionSink) handle(event llm.AgentEvent) error {
	switch e := event.(type) {
	case llm.MessageEndEvent:
		if err := s.m.AppendMessage(e.Message); err != nil {
			return err
		}
		s.liveIDs = append(s.liveIDs, s.m.LastEntryID())
	case llm.CompactionEndEvent:
		if err := s.recordCompaction(e); err != nil {
			return err
		}
	}
	if s.next == nil {
		return nil
	}
	return s.next(event)
}

// recordCompaction 解析保留区间首条消息的 entry ID 并落盘压缩点。
// 压缩把 Agent 上下文重排为 [摘要, 保留尾部...]，摘要消息由 CompactionEntry 回放产出，
// 镜像相应重置为空占位 + 保留区间的 entry ID，空占位让下一次压缩跳过它取 FirstKeptEntryID
func (s *sessionSink) recordCompaction(e llm.CompactionEndEvent) error {
	count := e.KeptMessageCount
	if count > len(s.liveIDs) {
		count = len(s.liveIDs)
	}
	kept := append([]string(nil), s.liveIDs[len(s.liveIDs)-count:]...)
	firstKept := ""
	for _, id := range kept {
		if id != "" {
			firstKept = id
			break
		}
	}
	var usage *llm.Usage
	if e.Usage.InputTokens != 0 || e.Usage.OutputTokens != 0 {
		usage = &e.Usage
	}
	if err := s.m.AppendCompaction(e.Summary, firstKept, e.TokensBefore, "", usage); err != nil {
		return err
	}
	liveIDs := make([]string, 0, count+1)
	liveIDs = append(liveIDs, "")
	liveIDs = append(liveIDs, kept...)
	s.liveIDs = liveIDs
	return nil
}
