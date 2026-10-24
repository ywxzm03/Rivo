package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ywxzm03/Rivo/internal/subagent"
)

type subagentMetadata struct {
	AgentType    string `json:"agentType"`
	Description  string `json:"description"`
	WorktreePath string `json:"worktreePath"`
}

// subagentResumeResolver 从 eviction 后仍留存的 sidechain 文件重建被淘汰
// subagent 的任务身份。transcript 缺失意味着任务无法恢复。
func subagentResumeResolver(sessionDir string) subagent.ResumeResolver {
	return func(taskID string) (subagent.ResumeSource, bool) {
		if sessionDir == "" || strings.TrimSpace(taskID) == "" {
			return subagent.ResumeSource{}, false
		}
		// task ID 直接构成文件名,拒绝任何路径分隔符
		if strings.ContainsAny(taskID, `/\`) || taskID == "." || taskID == ".." {
			return subagent.ResumeSource{}, false
		}
		transcriptPath := filepath.Join(sessionDir, "subagents", "agent-"+taskID+".jsonl")
		if _, err := os.Stat(transcriptPath); err != nil {
			return subagent.ResumeSource{}, false
		}
		source := subagent.ResumeSource{ID: taskID, AgentType: "work", OutputFile: transcriptPath}
		// metadata 只影响展示与 cwd 选择,缺失时按 work 类型恢复
		data, err := os.ReadFile(metadataPathFor("", sessionDir, taskID))
		if err != nil {
			return source, true
		}
		var metadata subagentMetadata
		if json.Unmarshal(data, &metadata) != nil {
			return source, true
		}
		if metadata.AgentType != "" {
			source.AgentType = metadata.AgentType
		}
		source.Description = metadata.Description
		source.WorktreePath = metadata.WorktreePath
		return source, true
	}
}
