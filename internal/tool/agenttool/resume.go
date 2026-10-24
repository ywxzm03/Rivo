package agenttool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// metadata 是随 transcript 一起落盘的任务元数据
type metadata struct {
	AgentType    string `json:"agentType"`
	Description  string `json:"description"`
	WorktreePath string `json:"worktreePath,omitempty"`
}

// ResumeResolver 从 eviction 后仍留存的 sidechain 文件重建任务身份。
// transcript 缺失意味着任务无法恢复。
func ResumeResolver(sessionDir string) taskpkg.ResumeResolver {
	return func(taskID string) (taskpkg.ResumeSource, bool) {
		// 1.任务 ID 直接拼进文件名,必须先挡住路径穿越
		if taskID == "" || strings.ContainsAny(taskID, `/\`) || strings.Contains(taskID, "..") {
			return taskpkg.ResumeSource{}, false
		}
		transcript := filepath.Join(sessionDir, "subagents", "agent-"+taskID+".jsonl")
		if _, err := os.Stat(transcript); err != nil {
			return taskpkg.ResumeSource{}, false
		}
		source := taskpkg.ResumeSource{ID: taskID, OutputFile: transcript, AgentType: "work"}
		// 2.元数据缺失时退回 work,不让恢复整体失败
		data, err := os.ReadFile(filepath.Join(sessionDir, "subagents", "agent-"+taskID+".meta.json"))
		if err != nil {
			return source, true
		}
		var meta metadata
		if err := json.Unmarshal(data, &meta); err != nil {
			return source, true
		}
		if strings.TrimSpace(meta.AgentType) != "" {
			source.AgentType = meta.AgentType
		}
		source.Description = meta.Description
		source.WorktreePath = meta.WorktreePath
		return source, true
	}
}
