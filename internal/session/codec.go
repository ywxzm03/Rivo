package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ywxzm03/Rivo/internal/filehistory"
	"github.com/ywxzm03/Rivo/internal/llm"
)

// errUnknownRole 标记 message entry 携带了未知角色，上层将其降级为 UnknownEntry
var errUnknownRole = errors.New("session: unknown message role")

type entryLine struct {
	Type string `json:"type"`
}

// parseFile 解析会话文件内容，返回 header 与按文件序的 entry 列表
func parseFile(data []byte) (*Header, []Entry, error) {
	header, entries, _, _, err := parseFileWithRecords(data)
	return header, entries, err
}

// parseFileWithRecords 分离树 entry 与不参与树结构的 metadata record
func parseFileWithRecords(data []byte) (*Header, []Entry, []filehistory.Record, []string, error) {
	var header *Header
	var entries []Entry
	var records []filehistory.Record
	var warnings []string
	for lineNumber, rawLine := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		var probe entryLine
		if err := json.Unmarshal(line, &probe); err != nil {
			if bytes.Contains(line, []byte("file-history-")) {
				warnings = append(warnings, fmt.Sprintf("session: skipped malformed file history record on line %d: %v", lineNumber+1, err))
			}
			continue
		}
		if header == nil {
			h, err := decodeHeader(line, probe.Type)
			if err != nil {
				return nil, nil, nil, warnings, err
			}
			header = h
			continue
		}
		if probe.Type == TypeSession {
			continue
		}
		if probe.Type == TypeFileHistorySnapshot || probe.Type == TypeFileHistoryDelta {
			record, err := filehistory.DecodeRecord(probe.Type, line)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("session: skipped invalid file history record on line %d: %v", lineNumber+1, err))
				continue
			}
			records = append(records, record)
			continue
		}
		entry, err := decodeEntry(probe.Type, string(line))
		if err != nil {
			if errors.Is(err, errUnknownRole) {
				var base EntryBase
				if baseErr := json.Unmarshal(line, &base); baseErr != nil {
					warnings = append(warnings, fmt.Sprintf("session: skipped unknown entry on line %d: %v", lineNumber+1, baseErr))
					continue
				}
				entries = append(entries, &UnknownEntry{EntryBase: base})
				continue
			}
			warnings = append(warnings, fmt.Sprintf("session: skipped invalid entry on line %d: %v", lineNumber+1, err))
			continue
		}
		entries = append(entries, entry)
	}
	if header == nil {
		return nil, nil, nil, warnings, errors.New("session: missing session header")
	}
	return header, entries, records, warnings, nil
}

// decodeHeader 校验并解码 header：类型必须是 session、版本必须是当前版本、id 非空
func decodeHeader(line []byte, typ string) (*Header, error) {
	if typ != TypeSession {
		return nil, errors.New("session: missing session header")
	}
	var h Header
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("session: invalid session header: %w", err)
	}
	if h.Version != CurrentVersion {
		return nil, fmt.Errorf("session: unsupported session version %d", h.Version)
	}
	if h.ID == "" {
		return nil, errors.New("session: session header missing id")
	}
	return &h, nil
}

func decodeEntry(typ, line string) (Entry, error) {
	switch typ {
	case TypeMessage:
		var e MessageEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		return &e, nil
	case TypeModelChange:
		var e ModelChangeEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		return &e, nil
	case TypeThinkingLevelChange:
		var e ThinkingLevelChangeEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		return &e, nil
	case TypeSessionInfo:
		var e SessionInfoEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		return &e, nil
	case TypeUsage:
		var e UsageEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		return &e, nil
	case TypeCompaction:
		var e CompactionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		return &e, nil
	case TypeContextEdit:
		var e ContextEditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		return &e, nil
	default:
		var base EntryBase
		if err := json.Unmarshal([]byte(line), &base); err != nil {
			return nil, err
		}
		return &UnknownEntry{EntryBase: base}, nil
	}
}

func (e MessageEntry) MarshalJSON() ([]byte, error) {
	raw, err := encodeMessage(e.Message)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		EntryBase
		Message json.RawMessage `json:"message"`
	}{e.EntryBase, raw})
}

func (e *MessageEntry) UnmarshalJSON(data []byte) error {
	var wire struct {
		EntryBase
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	msg, err := decodeMessage(wire.Message)
	if err != nil {
		return err
	}
	e.EntryBase = wire.EntryBase
	e.Message = msg
	return nil
}

func (r Replacement) MarshalJSON() ([]byte, error) {
	content, err := encodeContents(r.Content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Content json.RawMessage `json:"content"`
	}{content})
}

func (r *Replacement) UnmarshalJSON(data []byte) error {
	var wire struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	content, err := decodeContents(wire.Content)
	if err != nil {
		return err
	}
	r.Content = content
	return nil
}

func encodeContents(contents []llm.Content) (json.RawMessage, error) {
	blocks := make([]json.RawMessage, 0, len(contents))
	for _, c := range contents {
		var wire any
		switch c := c.(type) {
		case llm.TextContent:
			wire = struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{string(llm.ContentText), c.Text}
		case llm.ImageContent:
			wire = struct {
				Type     string `json:"type"`
				Data     string `json:"data"`
				MIMEType string `json:"mimeType"`
			}{string(llm.ContentImage), c.Data, c.MIMEType}
		case llm.ThinkingContent:
			wire = struct {
				Type              string `json:"type"`
				Thinking          string `json:"thinking"`
				ThinkingSignature string `json:"thinkingSignature,omitempty"`
			}{string(llm.ContentThinking), c.Thinking, c.ThinkingSignature}
		case llm.ToolCall:
			wire = struct {
				Type      string         `json:"type"`
				ID        string         `json:"id"`
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments,omitempty"`
			}{string(llm.ContentToolCall), c.ID, c.Name, c.Arguments}
		default:
			return nil, fmt.Errorf("session: unsupported content type %T", c)
		}
		block, err := json.Marshal(wire)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return json.Marshal(blocks)
}

// decodeContents 解码 content JSON 数组，未知 content 块丢弃
func decodeContents(raw json.RawMessage) ([]llm.Content, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	var out []llm.Content
	for _, item := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &probe); err != nil {
			return nil, err
		}
		switch probe.Type {
		case string(llm.ContentText):
			var wire struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(item, &wire); err != nil {
				return nil, err
			}
			out = append(out, llm.TextContent{Text: wire.Text})
		case string(llm.ContentImage):
			var wire struct {
				Data     string `json:"data"`
				MIMEType string `json:"mimeType"`
			}
			if err := json.Unmarshal(item, &wire); err != nil {
				return nil, err
			}
			out = append(out, llm.ImageContent{Data: wire.Data, MIMEType: wire.MIMEType})
		case string(llm.ContentThinking):
			var wire struct {
				Thinking          string `json:"thinking"`
				ThinkingSignature string `json:"thinkingSignature"`
			}
			if err := json.Unmarshal(item, &wire); err != nil {
				return nil, err
			}
			out = append(out, llm.ThinkingContent{Thinking: wire.Thinking, ThinkingSignature: wire.ThinkingSignature})
		case string(llm.ContentToolCall):
			var wire struct {
				ID        string         `json:"id"`
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(item, &wire); err != nil {
				return nil, err
			}
			out = append(out, llm.ToolCall{ID: wire.ID, Name: wire.Name, Arguments: wire.Arguments})
		default:
		}
	}
	return out, nil
}

func messageRole(m llm.Message) string {
	switch m.(type) {
	case llm.UserMessage:
		return "user"
	case llm.AssistantMessage:
		return "assistant"
	case llm.ToolResultMessage:
		return "toolResult"
	case llm.SystemMessage:
		return "system"
	default:
		return ""
	}
}

func encodeMessage(m llm.Message) (json.RawMessage, error) {
	if sys, ok := m.(llm.SystemMessage); ok {
		return json.Marshal(struct {
			Role     string            `json:"role"`
			Sections map[string]string `json:"sections"`
		}{"system", sys.Sections})
	}
	content, err := encodeContents(messageContent(m))
	if err != nil {
		return nil, err
	}
	switch m := m.(type) {
	case llm.UserMessage:
		return json.Marshal(struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}{"user", content})
	case llm.AssistantMessage:
		return json.Marshal(struct {
			Role         string          `json:"role"`
			Content      json.RawMessage `json:"content"`
			API          string          `json:"api"`
			Provider     string          `json:"provider"`
			Model        string          `json:"model"`
			ResponseID   string          `json:"responseID,omitempty"`
			StopReason   string          `json:"stopReason"`
			ErrorMessage string          `json:"errorMessage,omitempty"`
			Usage        llm.Usage       `json:"usage"`
		}{"assistant", content, string(m.API), m.Provider, m.Model, m.ResponseID, string(m.StopReason), m.ErrorMessage, cloneUsage(m.Usage)})
	case llm.ToolResultMessage:
		wire := struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Content    json.RawMessage `json:"content"`
			Details    json.RawMessage `json:"details,omitempty"`
			IsError    bool            `json:"isError"`
			Usage      *llm.Usage      `json:"usage,omitempty"`
		}{"toolResult", m.ToolCallID, m.ToolName, content, nil, m.IsError, nil}
		if m.Usage != nil {
			usage := cloneUsage(*m.Usage)
			wire.Usage = &usage
		}
		if m.Details != nil {
			details, err := json.Marshal(m.Details)
			if err != nil {
				return nil, err
			}
			wire.Details = details
		}
		return json.Marshal(wire)
	default:
		return nil, fmt.Errorf("session: unsupported message type %T", m)
	}
}

func decodeMessage(raw json.RawMessage) (llm.Message, error) {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.Role {
	case "user":
		var wire struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}
		content, err := decodeContents(wire.Content)
		if err != nil {
			return nil, err
		}
		return llm.UserMessage{Content: content}, nil
	case "assistant":
		var wire struct {
			Content      json.RawMessage `json:"content"`
			API          string          `json:"api"`
			Provider     string          `json:"provider"`
			Model        string          `json:"model"`
			ResponseID   string          `json:"responseID"`
			StopReason   string          `json:"stopReason"`
			ErrorMessage string          `json:"errorMessage,omitempty"`
			Usage        llm.Usage       `json:"usage"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}
		content, err := decodeContents(wire.Content)
		if err != nil {
			return nil, err
		}
		return llm.AssistantMessage{
			Content:      content,
			API:          llm.API(wire.API),
			Provider:     wire.Provider,
			Model:        wire.Model,
			ResponseID:   wire.ResponseID,
			StopReason:   llm.StopReason(wire.StopReason),
			ErrorMessage: wire.ErrorMessage,
			Usage:        cloneUsage(wire.Usage),
		}, nil
	case "toolResult":
		var wire struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Content    json.RawMessage `json:"content"`
			Details    json.RawMessage `json:"details"`
			IsError    bool            `json:"isError"`
			Usage      *llm.Usage      `json:"usage"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}
		content, err := decodeContents(wire.Content)
		if err != nil {
			return nil, err
		}
		msg := llm.ToolResultMessage{
			ToolCallID: wire.ToolCallID,
			ToolName:   wire.ToolName,
			Content:    content,
			IsError:    wire.IsError,
		}
		if wire.Usage != nil {
			usage := cloneUsage(*wire.Usage)
			msg.Usage = &usage
		}
		if len(wire.Details) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Details), []byte("null")) {
			details, err := llm.DecodeToolDetails(json.RawMessage(append([]byte(nil), wire.Details...)))
			if err != nil {
				return nil, err
			}
			msg.Details = details
		}
		return msg, nil
	case "system":
		var wire struct {
			Sections map[string]string `json:"sections"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}
		return llm.SystemMessage{Sections: wire.Sections}, nil
	default:
		return nil, errUnknownRole
	}
}

func messageContent(m llm.Message) []llm.Content {
	switch m := m.(type) {
	case llm.UserMessage:
		return m.Content
	case llm.AssistantMessage:
		return m.Content
	case llm.ToolResultMessage:
		return m.Content
	default:
		return nil
	}
}

// cloneUsage 复制一份 usage，避免写入方与读取方共享 Cost 指针
func cloneUsage(u llm.Usage) llm.Usage {
	u.Cost = cloneCost(u.Cost)
	return u
}
