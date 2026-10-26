package agenttool

// agentToolSnippet 是 tools 分节里的一行说明
const agentToolSnippet = "Launch an asynchronous subagent that runs in the background and notifies you when it finishes"

// agentToolDescription 是工具 schema 里的完整描述
const agentToolDescription = "Launch an asynchronous subagent for an independent task. " +
	"Omit subagent_type to fork yourself and inherit this conversation; " +
	"set subagent_type to start a fresh agent with its own context."

// agentToolGuidelines 注入 rules 分节,按模型决策顺序排列
func agentToolGuidelines() []string {
	return []string{
		// 1.何时不该用
		"When NOT to use the Agent tool: " +
			"to read a specific file path use read instead; " +
			"to find a specific class or definition use grep instead; " +
			"to search within a specific file or a set of 2-3 files use read instead; " +
			"and for tasks unrelated to the agent descriptions above, do them yourself.",

		// 2.并发启动
		"Launch multiple agents concurrently whenever possible to maximize performance; " +
			"to do that, use a single message with multiple tool uses.",

		// 3.后台语义:Rivo 没有 run_in_background,所有 Agent 调用都是异步
		"Every agent runs in the background. You will be automatically notified when it completes — " +
			"do NOT sleep, poll, or proactively check on its progress. Continue with other work or respond to the user instead.",

		// 4.When to fork
		"## When to fork\n" +
			"Fork yourself (omit subagent_type) when the intermediate tool output isn't worth keeping in your context. " +
			"The criterion is qualitative — \"will I need this output again\" — not task size.\n" +
			"- Research: fork open-ended questions. If research can be broken into independent questions, launch parallel forks in one message. " +
			"A fork beats a fresh subagent for this — it inherits context and shares your cache.\n" +
			"- Implementation: prefer to fork implementation work that requires more than a couple of edits. " +
			"Do research before jumping to implementation.\n" +
			"Forks are cheap because they share your prompt cache. Don't set model on a fork — a different model can't reuse the parent's cache.",

		// 5.Don't peek / Don't race
		"Don't peek. The tool result includes an output_file path — do not Read or tail it unless the user explicitly asks for a progress check. " +
			"You get a completion notification; trust it. Reading the transcript mid-flight pulls the fork's tool noise into your context, " +
			"which defeats the point of forking.",
		"Don't race. After launching, you know nothing about what the fork found. Never fabricate or predict fork results in any format — " +
			"not as prose, summary, or structured output. The notification arrives as a user-role message in a later turn; it is never something you write yourself. " +
			"If the user asks a follow-up before the notification lands, tell them the fork is still running — give status, not a guess.",

		// 6.Writing the prompt
		"## Writing the prompt\n" +
			"When spawning a fresh agent (with a subagent_type), it starts with zero context. " +
			"Brief the agent like a smart colleague who just walked into the room — it hasn't seen this conversation, " +
			"doesn't know what you've tried, doesn't understand why this task matters.\n" +
			"- Explain what you're trying to accomplish and why.\n" +
			"- Describe what you've already learned or ruled out.\n" +
			"- Give enough context about the surrounding problem that the agent can make judgment calls rather than just following a narrow instruction.\n" +
			"- If you need a short response, say so (\"report in under 200 words\").\n" +
			"- Lookups: hand over the exact command. Investigations: hand over the question — prescribed steps become dead weight when the premise is wrong.\n" +
			"For fresh agents, terse command-style prompts produce shallow, generic work.\n" +
			"Writing a fork prompt: since the fork inherits your context, the prompt is a directive — what to do, not what the situation is. " +
			"Be specific about scope: what's in, what's out, what another agent is handling. Don't re-explain background.",

		// 7.不要把理解外包
		"Never delegate understanding. Don't write \"based on your findings, fix the bug\" or \"based on the research, implement it.\" " +
			"Those phrases push synthesis onto the agent instead of doing it yourself. " +
			"Write prompts that prove you understood: include file paths, line numbers, what specifically to change.",
	}
}
