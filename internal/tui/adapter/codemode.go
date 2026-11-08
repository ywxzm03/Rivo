package adapter

import (
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool/codemode"
	"github.com/ywxzm03/Rivo/internal/tui"
)

// codemodeDetails 只接受已解码且带 marker 的领域详情
func codemodeDetails(details llm.ToolDetails) (codemode.Details, bool) {
	switch details := details.(type) {
	case codemode.Details:
		return details, details.Codemode
	case *codemode.Details:
		if details != nil {
			return *details, details.Codemode
		}
	}
	return codemode.Details{}, false
}

// codemodeBody 按结构边界取正文，不从状态头或英文前缀猜测
func codemodeBody(result llm.ToolResult, details codemode.Details) (string, bool) {
	if len(result.Content) == 0 {
		return "", false
	}
	if _, ok := result.Content[0].(llm.TextContent); !ok {
		return "", false
	}
	end := len(result.Content)
	if details.Error != "" {
		if end < 2 {
			return "", false
		}
		tail, ok := result.Content[end-1].(llm.TextContent)
		if !ok || tail.Text != details.Error {
			return "", false
		}
		end--
	}
	return llm.TextOf(result.Content[1:end]), true
}

// codemodeView 一次投影调用和分组，不保留 store 或图片内容
func codemodeView(details codemode.Details) *tui.CodemodeView {
	view := &tui.CodemodeView{Calls: make([]tui.CodemodeCallView, len(details.Calls))}
	indices := make(map[string]int, len(details.Calls))
	for i, call := range details.Calls {
		view.Calls[i] = tui.CodemodeCallView{
			ID: call.ID, Name: call.Name, Args: call.Args,
			Status: tui.CodemodeCallStatus(call.Status), Error: call.Error,
		}
		if call.DurationMS != nil {
			view.Calls[i].DurationMS = new(*call.DurationMS)
		}
		addCodemodeCount(&view.Summary.Groups, indices, call.Name, tui.CodemodeCallStatus(call.Status))
	}
	moveCodemodeEditLast(view.Summary.Groups)
	return view
}

func addCodemodeCount(groups *[]tui.CodemodeToolCountView, indices map[string]int, name string, status tui.CodemodeCallStatus) {
	index, exists := indices[name]
	if !exists {
		index = len(*groups)
		indices[name] = index
		*groups = append(*groups, tui.CodemodeToolCountView{Name: name})
	}
	group := &(*groups)[index]
	switch status {
	case tui.CodemodeOK:
		group.Success++
	case tui.CodemodeError:
		group.Failure++
	default:
		group.Unfinished++
	}
}

func moveCodemodeEditLast(groups []tui.CodemodeToolCountView) {
	for i, group := range groups {
		if group.Name == "edit" {
			copy(groups[i:], groups[i+1:])
			groups[len(groups)-1] = group
			return
		}
	}
}

// sameCodemodeCalls 在分配新投影前判断进度是否改变可见数据
func sameCodemodeCalls(view *tui.CodemodeView, calls []codemode.Call) bool {
	if view == nil || len(view.Calls) != len(calls) {
		return false
	}
	for i, call := range calls {
		previous := view.Calls[i]
		if previous.ID != call.ID || previous.Name != call.Name || previous.Args != call.Args || previous.Status != tui.CodemodeCallStatus(call.Status) || previous.Error != call.Error {
			return false
		}
		if (previous.DurationMS == nil) != (call.DurationMS == nil) || (call.DurationMS != nil && *previous.DurationMS != *call.DurationMS) {
			return false
		}
	}
	return true
}

// applyToolProgress 保留空进度之前的快照，只有可见变化才失效缓存
func applyToolProgress(call *tui.ToolCallView, result llm.ToolResult) bool {
	changed := false
	if call.Name == "codemode" {
		if details, ok := codemodeDetails(result.Details); ok {
			if !sameCodemodeCalls(call.Codemode, details.Calls) {
				call.Codemode = codemodeView(details)
				changed = true
			}
			if call.Details != "" {
				call.Details = ""
				changed = true
			}
			if len(result.Content) > 0 {
				if body := llm.TextOf(result.Content); call.Result != body {
					call.Result = body
					changed = true
				}
			}
			return changed
		}
		if call.Details != "" {
			call.Details = ""
			changed = true
		}
	} else if result.Details != nil {
		if details := encodeToolDetails(result.Details); call.Details != details {
			call.Details = details
			changed = true
		}
	}
	if len(result.Content) > 0 {
		if body := formatToolResult(result); call.Result != body {
			call.Result = body
			changed = true
		}
	}
	return changed
}

// applyToolFinal 现场和回放共用最终正文边界与元数据解释
func applyToolFinal(call *tui.ToolCallView, result llm.ToolResult) {
	call.Codemode = nil
	if call.Name == "codemode" {
		call.Details = ""
		if details, ok := codemodeDetails(result.Details); ok {
			if body, valid := codemodeBody(result, details); valid {
				view := codemodeView(details)
				view.Summary.Complete = true
				view.Output = tui.CodemodeOutputView{DurationMS: new(details.WallTimeMS), LineCount: details.OutputLines, FullOutputPath: details.FullOutputPath}
				view.Error = details.Error
				call.Codemode, call.Result = view, body
				return
			}
		}
	} else {
		call.Details = encodeToolDetails(result.Details)
	}
	call.Result = formatToolResult(result)
}

// interruptToolCall 封闭尚未返回最终结果的父调用和嵌套阶段
func interruptToolCall(call *tui.ToolCallView) bool {
	if call.Status != tui.ToolPending {
		return false
	}
	call.Status = tui.ToolInterrupted
	if view := call.Codemode; view != nil {
		view.Summary.Complete = false
		view.Output.DurationMS = nil
		view.Summary.Groups = nil
		indices := make(map[string]int, len(view.Calls))
		for i := range view.Calls {
			nested := &view.Calls[i]
			if nested.Status == tui.CodemodeQueued || nested.Status == tui.CodemodeRunning {
				nested.Status = tui.CodemodeCancelled
			}
			addCodemodeCount(&view.Summary.Groups, indices, nested.Name, nested.Status)
		}
		moveCodemodeEditLast(view.Summary.Groups)
	}
	return true
}

// terminalToolCallLocked 阻止封组后到达的旧进度重建父调用
func (r *Runtime) terminalToolCallLocked(id string) bool {
	if _, group := r.groupAnchorLocked(); group != nil {
		if index := findToolCall(group, id); index >= 0 {
			return group.Calls[index].Status != tui.ToolPending
		}
	}
	for i := len(r.messages) - 1; i >= 0; i-- {
		if group := r.messages[i].ToolGroup; group != nil {
			if index := findToolCall(group, id); index >= 0 {
				return group.Calls[index].Status != tui.ToolPending
			}
		}
	}
	return false
}
