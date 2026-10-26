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
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	// WorktreePath 是任务的工作目录,恢复时据此把子 agent 放回原 worktree
	WorktreePath string `json:"worktreePath,omitempty"`
	// WorktreeOwned 记录该 worktree 由子 agent 创建、理论上可回收。
	// 恢复路径只读取不回收:恢复后的会话会把用户工作写进该目录
	WorktreeOwned bool `json:"worktreeOwned,omitempty"`
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
		// 2.元数据缺失时退回 work;存在但损坏则说明落盘有问题,直接判为不可恢复
		data, err := os.ReadFile(filepath.Join(sessionDir, "subagents", "agent-"+taskID+".meta.json"))
		if err != nil {
			if os.IsNotExist(err) {
				return source, true
			}
			return taskpkg.ResumeSource{}, false
		}
		var meta metadata
		if err := json.Unmarshal(data, &meta); err != nil {
			return taskpkg.ResumeSource{}, false
		}
		if strings.TrimSpace(meta.AgentType) != "" {
			source.AgentType = meta.AgentType
		}
		source.Description = meta.Description
		source.WorktreePath = meta.WorktreePath
		source.WorktreeOwned = meta.WorktreeOwned
		return source, true
	}
}
