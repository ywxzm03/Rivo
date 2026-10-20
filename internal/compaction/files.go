package compaction

import (
	"sort"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// FileLists 记录摘要历史中读过的文件与改动过的文件
type FileLists struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

type fileOperations struct {
	read    map[string]struct{}
	written map[string]struct{}
	edited  map[string]struct{}
}

func newFileOperations() fileOperations {
	return fileOperations{
		read:    make(map[string]struct{}),
		written: make(map[string]struct{}),
		edited:  make(map[string]struct{}),
	}
}

func fileOperationsFromLists(lists *FileLists) fileOperations {
	ops := newFileOperations()
	if lists == nil {
		return ops
	}
	for _, path := range lists.ReadFiles {
		ops.read[path] = struct{}{}
	}
	for _, path := range lists.ModifiedFiles {
		ops.edited[path] = struct{}{}
	}
	return ops
}

func collectFileOperations(messages []llm.Message, ops *fileOperations) {
	for _, message := range messages {
		assistant, ok := message.(llm.AssistantMessage)
		if !ok {
			continue
		}
		for _, content := range assistant.Content {
			call, ok := content.(llm.ToolCall)
			if !ok {
				continue
			}
			path, ok := call.Arguments["path"].(string)
			if !ok || path == "" {
				continue
			}
			switch call.Name {
			case "read":
				ops.read[path] = struct{}{}
			case "write":
				ops.written[path] = struct{}{}
			case "edit":
				ops.edited[path] = struct{}{}
			}
		}
	}
}

func computeFileLists(ops fileOperations) FileLists {
	modified := make(map[string]struct{}, len(ops.written)+len(ops.edited))
	for path := range ops.written {
		modified[path] = struct{}{}
	}
	for path := range ops.edited {
		modified[path] = struct{}{}
	}
	readFiles := make([]string, 0, len(ops.read))
	for path := range ops.read {
		if _, ok := modified[path]; !ok {
			readFiles = append(readFiles, path)
		}
	}
	modifiedFiles := make([]string, 0, len(modified))
	for path := range modified {
		modifiedFiles = append(modifiedFiles, path)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return FileLists{ReadFiles: readFiles, ModifiedFiles: modifiedFiles}
}

func formatFileOperations(lists FileLists) string {
	sections := make([]string, 0, 2)
	if len(lists.ReadFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(lists.ReadFiles, "\n")+"\n</read-files>")
	}
	if len(lists.ModifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(lists.ModifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}
