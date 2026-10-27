package adapter

import (
	"strings"

	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// projectTree 隐藏 assistant/tool entry 但保留其父链，
// 使每个可见 user 节点都挂在最近的可见 user 祖先下。
// 隐藏 user 条目(子任务通知)不是可回退节点,同样只保留父链
func projectTree(snapshot session.TreeSnapshot) []tui.TreeNode {
	entries := make(map[string]session.TreeEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.ID] = entry
	}

	// visible 判定条目能否成为树节点:只有可见 user 可以
	visible := func(entry session.TreeEntry) bool {
		return entry.Role == "user" && !entry.Hidden
	}
	visibleParent := func(entry session.TreeEntry) string {
		parent := entry.ParentID
		seen := make(map[string]bool)
		for parent != "" && !seen[parent] {
			seen[parent] = true
			ancestor, ok := entries[parent]
			if !ok {
				return ""
			}
			if visible(ancestor) {
				return ancestor.ID
			}
			parent = ancestor.ParentID
		}
		return ""
	}

	children := make(map[string][]string)
	var roots []string
	for _, entry := range snapshot.Entries {
		if !visible(entry) {
			continue
		}
		parent := visibleParent(entry)
		children[parent] = append(children[parent], entry.ID)
		if parent == "" {
			roots = append(roots, entry.ID)
		}
	}

	current := snapshot.LeafID
	seen := make(map[string]bool)
	for current != "" && !seen[current] {
		seen[current] = true
		entry, ok := entries[current]
		if !ok {
			break
		}
		if visible(entry) {
			current = entry.ID
			break
		}
		current = entry.ParentID
	}
	if current == "" || !visible(entries[current]) {
		current = ""
	}

	var nodes []tui.TreeNode
	var walk func(parent, prefix string, depth int)
	walk = func(parent, prefix string, depth int) {
		for index, id := range children[parent] {
			entry := entries[id]
			last := index == len(children[parent])-1
			connector := ""
			nextPrefix := prefix
			if parent != "" {
				if last {
					connector = "└─ "
					nextPrefix += "   "
				} else {
					connector = "├─ "
					nextPrefix += "│  "
				}
			}
			text := strings.Join(strings.Fields(entry.Text), " ")
			nodes = append(nodes, tui.TreeNode{ID: id, ParentID: parent, Prefix: prefix + connector, Text: text, Depth: depth, Current: id == current})
			walk(id, nextPrefix, depth+1)
		}
	}
	if len(roots) <= 1 {
		walk("", "", 0)
	} else {
		for index, id := range roots {
			entry := entries[id]
			last := index == len(roots)-1
			connector := "├─ "
			nextPrefix := "│  "
			if last {
				connector = "└─ "
				nextPrefix = "   "
			}
			nodes = append(nodes, tui.TreeNode{ID: id, ParentID: "", Prefix: connector, Text: strings.Join(strings.Fields(entry.Text), " "), Depth: 0, Current: id == current})
			walk(id, nextPrefix, 1)
		}
	}
	return nodes
}
