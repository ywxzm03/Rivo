package compaction

// SummarizerSystemPrompt 摘要模型的系统提示，强制只输出结构化摘要
const SummarizerSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// summaryFormat 摘要的固定输出格式，新写与增量更新共用
const summaryFormat = `Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// summarizeBasePrompt 新写摘要的用户提示词
const summarizeBasePrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

` + summaryFormat

// summarizeUpdatePrompt 已有上次摘要时增量合并的用户提示词
const summarizeUpdatePrompt = `The messages above are the conversation since the previous summary. Merge the new information into the existing summary instead of starting over: keep entries that are still relevant, update the ones that changed, and drop the ones that no longer matter. The result must be a complete, self-contained summary.

` + summaryFormat

// turnPrefixPrompt split turn 轮次前缀的摘要提示词
const turnPrefixPrompt = `The messages above are the beginning of the current conversation turn, which is too long to keep in context. Summarize what the user asked for at the start of this turn and what the assistant has done so far, including any intermediate results needed to continue the turn. Preserve exact file paths, function names, and error messages.`

// compactionSummaryPrefix 摘要回插上下文时的包装文本，session 包复用同一常量保证回放一致
const (
	compactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSummarySuffix = "\n</summary>"
)

func BuildSummarizePrompt(conversation, instructions string) string {
	return withFocus("<conversation>\n"+conversation+"\n</conversation>\n\n"+summarizeBasePrompt, instructions)
}

func BuildUpdatePrompt(previousSummary, conversation, instructions string) string {
	base := "<conversation>\n" + conversation + "\n</conversation>\n\n<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n" + summarizeUpdatePrompt
	return withFocus(base, instructions)
}

func BuildTurnPrefixPrompt(conversation string) string {
	return "<conversation>\n" + conversation + "\n</conversation>\n\n" + turnPrefixPrompt
}

func withFocus(prompt, instructions string) string {
	if instructions == "" {
		return prompt
	}
	return prompt + "\n\nAdditional focus: " + instructions
}

func MergeSummaries(history, turnPrefix string) string {
	return history + "\n\n---\n\n**Turn Context (split turn):**\n\n" + turnPrefix
}

func WrappedSummary(summary string) string {
	return compactionSummaryPrefix + summary + compactionSummarySuffix
}
