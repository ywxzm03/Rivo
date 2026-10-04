package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// summaryMaxRunes 限制会话列表摘要长度，超出按 rune 截断并追加省略号
const summaryMaxRunes = 80

// SessionInfo 描述一个可恢复的会话文件
type SessionInfo struct {
	Path    string
	ModTime time.Time
	// Summary 为首条用户消息的截断摘要，无对话内容时为空
	Summary string
}

// ListSessions 列出 cwd 对应默认会话目录下的全部会话文件，按修改时间倒序；目录不存在返回空列表
func ListSessions(cwd string) ([]SessionInfo, error) {
	dir, err := DefaultDir(cwd)
	if err != nil {
		return nil, err
	}
	return listSessionsInDir(dir)
}

// listSessionsInDir 扫描目录下的 .jsonl 会话文件并按修改时间倒序返回
func listSessionsInDir(dir string) ([]SessionInfo, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: 读取会话目录失败: %w", err)
	}
	infos := make([]SessionInfo, 0, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, f.Name())
		infos = append(infos, SessionInfo{
			Path:    path,
			ModTime: info.ModTime(),
			Summary: firstUserSummary(path),
		})
	}
	// 1.修改时间倒序，最新的排最前，恢复取第一项；同时间戳按路径保证顺序确定
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].ModTime.Equal(infos[j].ModTime) {
			return infos[i].Path < infos[j].Path
		}
		return infos[i].ModTime.After(infos[j].ModTime)
	})
	return infos, nil
}

// firstUserSummary 逐行扫描会话文件，返回首条用户消息折叠空白后的截断摘要；
// 读取失败或无对话内容时返回空串。命中首条 user 消息即返回，避免列表场景全量解析历史；
// 用 ReadString 而非 Scanner，单行长度不限（user 消息内嵌 base64 图片时单行可能极大）
func firstUserSummary(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		trimmed := bytes.TrimSpace([]byte(line))
		if len(trimmed) > 0 {
			var probe entryLine
			if json.Unmarshal(trimmed, &probe) == nil && probe.Type != TypeSession {
				entry, decodeErr := decodeEntry(probe.Type, string(trimmed))
				if decodeErr == nil {
					if me, ok := entry.(*MessageEntry); ok && messageRole(me.Message) == "user" {
						// 1.多行输入折叠为单空格，避免破坏编号列表排版
						return truncateRunes(strings.Join(strings.Fields(userText(me.Message)), " "), summaryMaxRunes)
					}
				}
			}
		}
		if err != nil {
			// 2.EOF 或读取错误均停止：EOF 前已处理完剩余内容
			return ""
		}
	}
}

// userText 拼接用户消息中的全部文本块
func userText(msg llm.Message) string {
	m, ok := msg.(llm.UserMessage)
	if !ok {
		return ""
	}
	var texts []string
	for _, c := range m.Content {
		if t, ok := c.(llm.TextContent); ok {
			texts = append(texts, t.Text)
		}
	}
	return strings.Join(texts, " ")
}

// truncateRunes 按 rune 截断文本，超长时追加省略号
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
