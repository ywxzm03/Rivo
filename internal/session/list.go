package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// summaryMaxRunes 限制会话列表摘要长度
const summaryMaxRunes = 80

// SessionInfo 描述一个可恢复的会话文件
type SessionInfo struct {
	Path    string
	ID      string
	Cwd     string
	ModTime time.Time
	Name    string
	// Summary 为首条用户消息的截断摘要，无对话内容时为空
	Summary string
}

// ListSessions 列出 cwd 对应默认会话目录下的全部会话文件，按修改时间倒序
func ListSessions(cwd string) ([]SessionInfo, error) {
	dir, err := DefaultDir(cwd)
	if err != nil {
		return nil, err
	}
	return listSessionsInDir(dir)
}

// ListAllSessions 列出 agent 根目录下所有项目的会话
func ListAllSessions() ([]SessionInfo, error) {
	root, err := SessionsRoot()
	if err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: failed to read session root: %w", err)
	}
	var all []SessionInfo
	var listErrs []error
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		infos, err := listSessionsInDir(filepath.Join(root, project.Name()))
		if err != nil {
			listErrs = append(listErrs, fmt.Errorf("%s: %w", project.Name(), err))
			continue
		}
		all = append(all, infos...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ModTime.Equal(all[j].ModTime) {
			return all[i].Path < all[j].Path
		}
		return all[i].ModTime.After(all[j].ModTime)
	})
	return all, errors.Join(listErrs...)
}

// SessionsRoot 返回按 cwd 分组的会话根目录
func SessionsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: failed to get user home directory: %w", err)
	}
	return filepath.Join(home, ".rivo", "agent", "sessions"), nil
}

func listSessionsInDir(dir string) ([]SessionInfo, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: failed to read session directory: %w", err)
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
		id, cwd, name, summary := readSessionMetadata(path)
		infos = append(infos, SessionInfo{
			Path:    path,
			ID:      id,
			Cwd:     cwd,
			ModTime: info.ModTime(),
			Name:    name,
			Summary: summary,
		})
	}
	// 1.修改时间倒序，同时间戳按路径保证顺序确定
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].ModTime.Equal(infos[j].ModTime) {
			return infos[i].Path < infos[j].Path
		}
		return infos[i].ModTime.After(infos[j].ModTime)
	})
	return infos, nil
}

// summaryLineLimit 限制摘要扫描的单行缓冲，超长行（如内嵌 base64 图片）直接跳过
const summaryLineLimit = 1 << 20

// readSessionMetadata 有界扫描会话文件，取最新名称与首条用户摘要，
// 内存占用不随会话内容增长
func readSessionMetadata(path string) (id, cwd, name, summary string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", ""
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	hasSummary := false
	for {
		line, oversize, readErr := readBoundedLine(reader)
		if !oversize {
			trimmed := bytes.TrimSpace(line)
			var probe entryLine
			if json.Unmarshal(trimmed, &probe) == nil {
				if probe.Type == TypeSession {
					var header Header
					if json.Unmarshal(trimmed, &header) == nil {
						id, cwd = header.ID, header.Cwd
					}
				}
				switch probe.Type {
				case TypeSessionInfo:
					var info SessionInfoEntry
					if json.Unmarshal(trimmed, &info) == nil {
						name = info.Name
					}
				case TypeMessage:
					if !hasSummary {
						entry, decodeErr := decodeEntry(probe.Type, string(trimmed))
						if decodeErr == nil {
							// 摘要取首条真实用户输入,通知这类隐藏消息不参与
							if me, ok := entry.(*MessageEntry); ok && messageRole(me.Message) == "user" && !llm.IsHiddenMessage(me.Message) {
								// 多行输入折叠为空格，避免破坏会话列表排版
								summary = truncateRunes(strings.Join(strings.Fields(userText(me.Message)), " "), summaryMaxRunes)
								hasSummary = true
							}
						}
					}
				}
			}
		}
		if readErr != nil {
			return id, cwd, name, summary
		}
	}
}

// readBoundedLine 读取一行，缓冲超限时继续消费到行尾并标记 oversize
func readBoundedLine(reader *bufio.Reader) (line []byte, oversize bool, err error) {
	var buf []byte
	for {
		chunk, readErr := reader.ReadSlice('\n')
		if !oversize {
			if len(buf)+len(chunk) > summaryLineLimit {
				oversize = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if readErr == bufio.ErrBufferFull {
			continue
		}
		return buf, oversize, readErr
	}
}

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

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
