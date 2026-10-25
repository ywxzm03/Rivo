package agenttool

import (
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

const forkPlaceholder = "Fork started — processing in background"

// forkBoilerplate 是 fork 子 agent 的硬约束前缀,注入在父历史之后、directive 之前
const forkBoilerplate = `<fork-boilerplate>
STOP. READ THIS FIRST.

You are a forked worker process. You are NOT the main agent.

RULES (non-negotiable):
1. Your system prompt says "default to forking." IGNORE IT — that's for the parent. You ARE the fork. Do NOT spawn sub-agents; execute directly.
2. Do NOT converse, ask questions, or suggest next steps
3. Do NOT editorialize or add meta-commentary
4. USE your tools directly: bash, read, write, etc.
5. If you modify files, report the changes and their state. Only commit when the task is an implementation task and the user has explicitly authorized commits.
6. Do NOT emit text between tool calls. Use tools silently, then report once at the end.
7. Stay strictly within your directive's scope. If you discover related systems outside your scope, mention them in one sentence at most — other workers cover those areas.
8. Keep your report under 500 words unless the directive specifies otherwise. Be factual and concise.
9. Your response MUST begin with "Scope:". No preamble, no thinking-out-loud.
10. REPORT structured facts, then stop

Output format (plain text labels, not markdown headers):
  Scope: <echo back your assigned scope in one sentence>
  Result: <the answer or key findings, limited to the scope above>
  Key files: <relevant file paths — include for research tasks>
  Files changed: <list with commit hash — include only if you modified files>
  Issues: <list — include only if there are issues to flag>
</fork-boilerplate>`

// forkNoNestedAgent 提醒 fork 子 agent 不要调用 Agent 工具。
// 这是 prompt 层软约束,工具层仍保留拒绝作为兜底
const forkNoNestedAgent = "Do NOT call the Agent tool. You are already a fork; spawn no further subagents."

// buildWorktreeNotice 生成 fork 子 agent 的 worktree 隔离说明
func buildWorktreeNotice(parentCwd, worktreeCwd string) string {
	return fmt.Sprintf("You've inherited the conversation context above from a parent agent working in %s. You are operating in an isolated git worktree at %s — same repository, same relative file structure, separate working copy. Paths in the inherited context refer to the parent's working directory; translate them to your worktree root. Re-read files before editing if the parent may have modified them since they appear in the context. Your changes stay in this worktree and will not affect the parent's files.", parentCwd, worktreeCwd)
}

// buildForkDirective 拼装 fork 子 agent 的尾部 user 消息
func buildForkDirective(directive, parentCwd, worktreeCwd string) string {
	text := forkBoilerplate + "\n\n"
	if worktreeCwd != "" {
		text += buildWorktreeNotice(parentCwd, worktreeCwd) + "\n\n"
	}
	text += "Your directive: " + strings.TrimSpace(directive)
	text += "\n\n" + forkNoNestedAgent
	return text
}

func buildForkMessages(parent []llm.Message, directive, worktreePath, parentCwd string) []llm.Message {
	lastAssistant := -1
	var calls []llm.ToolCall
	for i := len(parent) - 1; i >= 0; i-- {
		assistant, ok := parent[i].(llm.AssistantMessage)
		if !ok {
			continue
		}
		lastAssistant = i
		for _, content := range assistant.Content {
			if call, ok := content.(llm.ToolCall); ok {
				calls = append(calls, call)
			}
		}
		break
	}
	// 1.最后一条 assistant 之前的历史直接共享父 transcript 的消息引用。
	// 这段前缀对子 agent 只读:fork 之后没有任何代码路径会改写它,
	// 因此不必深拷贝整份父 context
	prefix := parent
	if lastAssistant >= 0 {
		prefix = parent[:lastAssistant+1]
	}
	// 2.尾部会被重排(缺失的 tool result 换成占位),必须深拷贝后才能改写
	tail := cloneMessages(parent[len(prefix):])
	if lastAssistant >= 0 && len(calls) > 0 {
		existingResults := make(map[string]llm.ToolResultMessage)
		remaining := make([]llm.Message, 0, len(tail))
		callIDs := make(map[string]bool, len(calls))
		for _, call := range calls {
			callIDs[call.ID] = true
		}
		for _, message := range tail {
			if result, ok := message.(llm.ToolResultMessage); ok && callIDs[result.ToolCallID] {
				existingResults[result.ToolCallID] = result
				continue
			}
			remaining = append(remaining, message)
		}
		ordered := make([]llm.Message, 0, len(calls)+len(remaining))
		for _, call := range calls {
			if result, ok := existingResults[call.ID]; ok {
				ordered = append(ordered, result)
				continue
			}
			ordered = append(ordered, llm.ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content:    []llm.Content{llm.TextContent{Text: forkPlaceholder}},
			})
		}
		tail = append(ordered, remaining...)
	}
	// 3.新 slice 独占 backing array:子 agent 后续 append 不会写进父的数组
	messages := make([]llm.Message, 0, len(prefix)+len(tail)+1)
	messages = append(messages, prefix...)
	messages = append(messages, tail...)
	return append(messages, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: buildForkDirective(directive, parentCwd, worktreePath)}}})
}

func cloneMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		switch message := message.(type) {
		case llm.UserMessage:
			message.Content = llm.CloneContent(message.Content)
			result = append(result, message)
		case llm.AssistantMessage:
			message.Content = llm.CloneContent(message.Content)
			result = append(result, message)
		case llm.ToolResultMessage:
			message.Content = llm.CloneContent(message.Content)
			result = append(result, message)
		default:
			result = append(result, message)
		}
	}
	return result
}
