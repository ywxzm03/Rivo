package session

import "github.com/ywxzm03/Rivo/internal/llm"

// Sink 返回接入 SessionManager 的事件 sink：message_end 先追加进会话再转发给 next，
// 持久化失败返回错误以终止当前 Agent 调用
func Sink(m *SessionManager, next llm.AgentEventSink) llm.AgentEventSink {
	return func(event llm.AgentEvent) error {
		if e, ok := event.(llm.MessageEndEvent); ok {
			if err := m.AppendMessage(e.Message); err != nil {
				return err
			}
		}
		if next == nil {
			return nil
		}
		return next(event)
	}
}
