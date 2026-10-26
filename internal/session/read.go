package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// readChunkBytes 是单次增量读取的上限。sidechain 常驻磁盘,
// 长时间不轮询后一次补齐全部积压会瞬间放大内存占用
const readChunkBytes = 256 * 1024

// ReadMessages 从会话文件按字节偏移增量读取 message entry。
// 只消费已写完整的行,返回消息与新的偏移;调用方据此缓存已投影的前缀,
// 内存里不需要常驻整份 transcript。
// 单次最多消费 readChunkBytes,剩余部分留给下次调用继续读。
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
	reader := bufio.NewReaderSize(f, 64*1024)
	var messages []llm.Message
	consumed := int64(0)
	for consumed < readChunkBytes {
		remaining := readChunkBytes - consumed
		line, read, oversize, err := readCappedLine(reader, remaining)
		if err != nil {
			// 末尾没有换行说明最后一行的写入还没完成,留到下次读取;
			// 已读出的半行不能消费
			if errors.Is(err, io.EOF) {
				break
			}
			return messages, offset + consumed, err
		}
		if oversize {
			// 本轮已有内容时先返回,让这条大消息独占下一轮,避免它被永久跳过
			if consumed > 0 {
				return messages, offset + consumed, nil
			}
			// 独占一轮:完整解码这条大消息,偏移正常前进
			consumed += read
			if message, ok := decodeMessageLine(line); ok {
				messages = append(messages, message)
			}
			continue
		}
		consumed += read
		if message, ok := decodeMessageLine(line); ok {
			messages = append(messages, message)
		}
	}
	return messages, offset + consumed, nil
}

// readCappedLine 读一整行,同时报告它是否超过 limit。
// 行内容始终完整返回:超限只用于让调用方决定是否独占一轮,
// 不能因此丢弃消息
func readCappedLine(reader *bufio.Reader, limit int64) ([]byte, int64, bool, error) {
	var line []byte
	read := int64(0)
	oversize := false
	for {
		chunk, err := reader.ReadSlice('\n')
		read += int64(len(chunk))
		line = append(line, chunk...)
		if !oversize && read > limit {
			oversize = true
		}
		if err == nil {
			return line, read, oversize, nil
		}
		if err != bufio.ErrBufferFull {
			return nil, 0, false, err
		}
	}
}

// decodeMessageLine 解析一行 entry,非 message 或损坏的行返回 false
func decodeMessageLine(raw []byte) (llm.Message, bool) {
	line := bytes.TrimSpace(raw)
	if len(line) == 0 {
		return nil, false
	}
	var probe entryLine
	if json.Unmarshal(line, &probe) != nil || probe.Type != TypeMessage {
		return nil, false
	}
	entry, err := decodeEntry(probe.Type, string(line))
	if err != nil {
		return nil, false
	}
	message, ok := entry.(*MessageEntry)
	if !ok {
		return nil, false
	}
	return message.Message, true
}
