package session

import (
	"bytes"
	"encoding/json"
	"io"
	"os"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// ReadMessages 从会话文件按字节偏移增量读取 message entry。
// 只消费已写完整的行,返回消息与新的偏移;调用方据此缓存已投影的前缀,
// 内存里不需要常驻整份 transcript。
func ReadMessages(path string, offset int64) ([]llm.Message, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, offset, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	// 末尾没有换行说明最后一行的写入还没完成,留到下次读取
	consumed := bytes.LastIndexByte(data, '\n') + 1
	var messages []llm.Message
	for _, rawLine := range bytes.Split(data[:consumed], []byte("\n")) {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		var probe entryLine
		if json.Unmarshal(line, &probe) != nil || probe.Type != TypeMessage {
			continue
		}
		entry, err := decodeEntry(probe.Type, string(line))
		if err != nil {
			continue
		}
		if message, ok := entry.(*MessageEntry); ok {
			messages = append(messages, message.Message)
		}
	}
	return messages, offset + int64(consumed), nil
}
