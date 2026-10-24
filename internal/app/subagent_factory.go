package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ywxzm03/Rivo/internal/agent"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/prompt"
	"github.com/ywxzm03/Rivo/internal/session"
	"github.com/ywxzm03/Rivo/internal/subagent"
	toolpkg "github.com/ywxzm03/Rivo/internal/tool"
)

type subagentFactoryConfig struct {
	ParentCwd   string
	SessionDir  string
	AgentDir    string
	ParentModel llm.Model
	Thinking    llm.ThinkingLevel
	StreamFn    llm.StreamFn
	ParentTools []llm.Tool
	TaskStore   *subagent.Store
	OnEvent     llm.AgentEventSink
}

const forkMaxTurns = 200

func newSubagentFactory(config subagentFactoryConfig) subagent.AgentRuntimeFactory {
	return func(_ context.Context, spec subagent.AgentTaskSpec) (*subagent.AgentRuntime, error) {
		cwd := config.ParentCwd
		worktreePath := spec.WorktreePath
		createdWorktree := false
		factorySucceeded := false
		defer func() {
			if createdWorktree && !factorySucceeded {
				_ = cleanupSubagentWorktree(worktreePath, config.ParentCwd, metadataPathFor(worktreePath, config.SessionDir, spec.ID))
			}
		}()
		if spec.Worktree && worktreePath == "" {
			var err error
			worktreePath, err = createSubagentWorktree(config.ParentCwd, config.SessionDir, spec.ID)
			if err != nil {
				return nil, err
			}
			createdWorktree = true
		}
		if worktreePath != "" {
			cwd = worktreePath
		}
		if config.SessionDir == "" {
			return nil, fmt.Errorf("subagent: session directory is unavailable")
		}
		transcriptPath := filepath.Join(config.SessionDir, "subagents", "agent-"+spec.ID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
			return nil, err
		}
		metadataPath := strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"
		if !spec.Continuation || createdWorktree {
			metadata := map[string]any{"agentType": spec.AgentType, "description": spec.Description}
			if worktreePath != "" {
				metadata["worktreePath"] = worktreePath
			}
			data, err := json.Marshal(metadata)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
				return nil, err
			}
		}
		var sm *session.SessionManager
		var replacementState map[string][]llm.Content
		var pendingToPersist []llm.Message
		if spec.Continuation {
			if _, err := os.Stat(transcriptPath); err != nil {
				return nil, fmt.Errorf("subagent: transcript %q is unavailable: %w", transcriptPath, err)
			}
			var err error
			sm, err = session.Open(transcriptPath, cwd)
			if err != nil {
				return nil, err
			}
		} else {
			sm = session.NewAt(cwd, transcriptPath)
		}
		model := config.ParentModel
		forkRequested := strings.EqualFold(spec.AgentType, "fork")
		if !forkRequested && spec.Model.ID != "" {
			model = spec.Model
		}
		var definition *subagent.AgentDefinition
		if !strings.EqualFold(spec.AgentType, "fork") && !isBuiltinAgent(spec.AgentType) {
			var err error
			definition, err = subagent.LoadAgentDefinition(cwd, config.AgentDir, spec.AgentType)
			if err != nil {
				return nil, err
			}
		}
		if !forkRequested && definition != nil && spec.Model.ID == "" && definition.Model != "" {
			if parts := strings.SplitN(definition.Model, "/", 2); len(parts) == 2 {
				model.Provider, model.ID = parts[0], parts[1]
			} else {
				model.ID = definition.Model
			}
		}
		messages := cloneMessages(spec.InitialMessages)
		fork := strings.EqualFold(spec.AgentType, "fork")
		forkInitial := fork && !spec.Continuation && spec.ParentContext != nil
		if sm.FileHistory() == nil {
			sm.AttachFileHistory(config.AgentDir)
		}
		tools := childTools(config.ParentTools, fork, sm, config.TaskStore)
		if definition != nil {
			var err error
			tools, err = filterAgentTools(tools, definition.Tools)
			if err != nil {
				return nil, err
			}
		}
		if !fork && (strings.EqualFold(spec.AgentType, "explore") || strings.EqualFold(spec.AgentType, "plan")) {
			tools = readOnlyTools(tools)
		}
		if spec.Continuation {
			view := sm.BuildContext()
			messages = cloneMessages(view.Messages())
			replacementState = view.ContentReplacements
			if len(spec.PendingMessages) > 0 {
				pending := make([]llm.Message, len(spec.PendingMessages))
				for i, message := range spec.PendingMessages {
					pending[i] = message
				}
				pendingToPersist = pending
			}
		} else if forkInitial {
			messages = buildForkMessages(spec.ParentContext.Messages, spec.Prompt, worktreePath, config.ParentCwd)
		} else {
			var contextFiles []prompt.ContextFile
			var skills []prompt.Skill
			omitInstructions := strings.EqualFold(spec.AgentType, "explore") || strings.EqualFold(spec.AgentType, "plan")
			if definition != nil && definition.OmitAgentsMd {
				omitInstructions = true
			}
			if !omitInstructions {
				loader := prompt.NewLoader(cwd, config.AgentDir)
				loader.Reload()
				contextFiles, skills = loader.ContextFiles(), loader.Skills()
			}
			sections := prompt.BuildSections(prompt.Options{Cwd: cwd, Tools: tools, ContextFiles: contextFiles, Skills: skills})
			agentPrompt := builtinPrompt(spec.AgentType)
			if definition != nil {
				agentPrompt = definition.Prompt
			}
			if existing := sections["instructions"]; existing != "" {
				sections["instructions"] = existing + "\n\n<agent-instructions>\n" + agentPrompt + "\n</agent-instructions>"
			} else {
				sections["instructions"] = "<instructions>\n" + agentPrompt + "\n</instructions>"
			}
			messages = append(messages, llm.SystemMessage{Sections: sections})
		}
		if spec.Continuation && !fork {
			var contextFiles []prompt.ContextFile
			var skills []prompt.Skill
			omitInstructions := strings.EqualFold(spec.AgentType, "explore") || strings.EqualFold(spec.AgentType, "plan")
			if definition != nil && definition.OmitAgentsMd {
				omitInstructions = true
			}
			if !omitInstructions {
				loader := prompt.NewLoader(cwd, config.AgentDir)
				loader.Reload()
				contextFiles, skills = loader.ContextFiles(), loader.Skills()
			}
			sections := prompt.BuildSections(prompt.Options{Cwd: cwd, Tools: tools, ContextFiles: contextFiles, Skills: skills})
			agentPrompt := builtinPrompt(spec.AgentType)
			if definition != nil {
				agentPrompt = definition.Prompt
			}
			if existing := sections["instructions"]; existing != "" {
				sections["instructions"] = existing + "\n\n<agent-instructions>\n" + agentPrompt + "\n</agent-instructions>"
			} else {
				sections["instructions"] = "<instructions>\n" + agentPrompt + "\n</instructions>"
			}
			if existing := llm.CurrentSystemMessage(messages); existing == nil {
				system := llm.SystemMessage{Sections: sections}
				if _, err := sm.AppendMessages([]llm.Message{system}); err != nil {
					return nil, err
				}
				messages = append(messages, system)
			} else if patch := prompt.DiffSections(existing.Sections, sections); patch != nil {
				system := llm.SystemMessage{Sections: patch}
				if _, err := sm.AppendMessages([]llm.Message{system}); err != nil {
					return nil, err
				}
				messages = append(messages, system)
			}
		}
		if len(pendingToPersist) > 0 {
			if _, err := sm.AppendMessages(pendingToPersist); err != nil {
				return nil, err
			}
			messages = append(messages, pendingToPersist...)
		}
		if len(messages) > 0 && !spec.Continuation {
			if _, err := sm.AppendMessages(messages); err != nil {
				return nil, err
			}
		}
		thinking := config.Thinking
		if definition != nil && spec.Thinking == "" && definition.Thinking != "" {
			thinking = definition.Thinking
		}
		if !forkRequested && spec.Thinking != "" {
			thinking = spec.Thinking
		}
		cfg := llm.AgentConfig{Model: model, ThinkingLevel: thinking, SessionID: sm.ID()}
		if fork && spec.ParentConfig != nil {
			cfg = *spec.ParentConfig
			cfg.SessionID = sm.ID()
		}
		// 子事件由 Manager 的 task 级 handler 消费。把它们送到父 sink 会把
		// sidechain transcript 更新混进主 TUI 流。
		child := agent.NewAgent(cfg, llm.Context{Tools: tools, Messages: messages}, config.StreamFn, nil)
		child.WorkingDir = cwd
		if fork {
			child.MaxTurns = forkMaxTurns
		}
		if definition != nil {
			child.MaxTurns = definition.MaxTurns
		}
		child.CommitMessages = sm.AppendMessages
		runtime := &subagent.AgentRuntime{
			Agent: child, Cwd: cwd, OutputFile: transcriptPath, WorktreePath: worktreePath, ContentReplacementState: replacementState, Done: make(chan struct{}),
			AppendMessages: func(messages []llm.Message) error {
				_, err := sm.AppendMessages(messages)
				return err
			},
		}
		if spec.Worktree && worktreePath != "" {
			runtime.WorktreeCleanup = func() bool { return cleanupSubagentWorktree(worktreePath, config.ParentCwd, metadataPath) }
		}
		if forkInitial {
			runtime.Run = func(ctx context.Context, _ string) (*llm.AssistantMessage, error) {
				return child.Continue(ctx)
			}
		} else if spec.Continuation {
			runtime.Run = func(ctx context.Context, prompt string) (*llm.AssistantMessage, error) {
				return child.RunMessage(ctx, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: prompt}}})
			}
		}
		factorySucceeded = true
		return runtime, nil
	}
}

func createSubagentWorktree(parentCwd, sessionDir, id string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("subagent: git is unavailable for worktree: %w", err)
	}
	path := filepath.Join(sessionDir, "subagents", "worktree-"+id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	base := "HEAD"
	if output, err := exec.Command("git", "-C", parentCwd, "rev-parse", "--verify", "origin/HEAD").CombinedOutput(); err == nil && strings.TrimSpace(string(output)) != "" {
		base = "origin/HEAD"
	}
	cmd := exec.Command("git", "-C", parentCwd, "worktree", "add", "--detach", path, base)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("subagent: create worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return path, nil
}

func metadataPathFor(_ string, sessionDir, id string) string {
	return filepath.Join(sessionDir, "subagents", "agent-"+id+".meta.json")
}

func cleanupSubagentWorktree(path, parentCwd, metadataPath string) bool {
	status, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	if len(status) != 0 {
		return false
	}
	if err := exec.Command("git", "-C", parentCwd, "worktree", "remove", "--force", path).Run(); err != nil {
		return false
	}
	_ = os.Remove(metadataPath)
	return true
}

func childTools(parent []llm.Tool, keepAgent bool, childSession *session.SessionManager, taskStore *subagent.Store) []llm.Tool {
	if taskStore == nil {
		taskStore = subagent.NewStore()
	}
	tools := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		if tool.Name() == "TaskStop" {
			continue
		}
		if tool.Name() == "agent" && !keepAgent {
			continue
		}
		if tool.Name() == "agent" && keepAgent {
			tools = append(tools, rejectingAgentTool{Tool: tool})
			continue
		}
		switch tool.(type) {
		case *toolpkg.Write:
			tools = append(tools, &toolpkg.Write{History: childSession.FileHistory()})
			continue
		case *toolpkg.Edit:
			tools = append(tools, &toolpkg.Edit{History: childSession.FileHistory()})
			continue
		case *toolpkg.Bash:
			tools = append(tools, &toolpkg.Bash{Background: toolpkg.NewBackgroundBashRunner(taskStore)})
			continue
		}
		tools = append(tools, tool)
	}
	return tools
}

func readOnlyTools(parent []llm.Tool) []llm.Tool {
	tools := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		switch tool.Name() {
		case "write", "edit", "powershell", "agent", "TaskStop":
			continue
		case "bash":
			if bash, ok := tool.(*toolpkg.Bash); ok {
				tools = append(tools, toolpkg.NewReadOnlyBash(bash))
			} else {
				tools = append(tools, tool)
			}
		default:
			tools = append(tools, tool)
		}
	}
	return tools
}

func filterAgentTools(parent []llm.Tool, allowed []string) ([]llm.Tool, error) {
	if len(allowed) == 0 {
		return parent, nil
	}
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		if strings.TrimSpace(name) == "*" {
			return parent, nil
		}
		set[strings.ToLower(name)] = true
	}
	result := make([]llm.Tool, 0, len(parent))
	for _, tool := range parent {
		if set[strings.ToLower(tool.Name())] {
			result = append(result, tool)
		}
	}
	for name := range set {
		found := false
		for _, tool := range result {
			if strings.EqualFold(tool.Name(), name) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("subagent: agent definition requests unknown or unavailable tool %q", name)
		}
	}
	return result, nil
}

func isBuiltinAgent(name string) bool {
	switch strings.ToLower(name) {
	case "explore", "plan", "work":
		return true
	default:
		return false
	}
}

type rejectingAgentTool struct{ llm.Tool }

func (rejectingAgentTool) Execute(context.Context, map[string]any) (llm.ToolResult, error) {
	return llm.ToolResult{IsError: true, Content: []llm.Content{llm.TextContent{Text: "nested subagents are unavailable in a fork child"}}}, nil
}

func cloneMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		switch message := message.(type) {
		case llm.UserMessage:
			message.Content = cloneContent(message.Content)
			result = append(result, message)
		case llm.AssistantMessage:
			message.Content = cloneContent(message.Content)
			result = append(result, message)
		case llm.ToolResultMessage:
			message.Content = cloneContent(message.Content)
			result = append(result, message)
		default:
			result = append(result, message)
		}
	}
	return result
}

func cloneContent(content []llm.Content) []llm.Content {
	result := make([]llm.Content, len(content))
	for i, item := range content {
		if call, ok := item.(llm.ToolCall); ok {
			copy := call
			copy.Arguments = make(map[string]any, len(call.Arguments))
			for key, value := range call.Arguments {
				copy.Arguments[key] = value
			}
			result[i] = copy
			continue
		}
		result[i] = item
	}
	return result
}

const forkPlaceholder = "Fork started — processing in background"

func buildForkMessages(parent []llm.Message, directive, worktreePath, parentCwd string) []llm.Message {
	messages := cloneMessages(parent)
	lastAssistant := -1
	var calls []llm.ToolCall
	for i := len(messages) - 1; i >= 0; i-- {
		assistant, ok := messages[i].(llm.AssistantMessage)
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
	if lastAssistant >= 0 && len(calls) > 0 {
		existingResults := make(map[string]llm.ToolResultMessage)
		remaining := make([]llm.Message, 0, len(messages)-lastAssistant-1)
		callIDs := make(map[string]bool, len(calls))
		for _, call := range calls {
			callIDs[call.ID] = true
		}
		for _, message := range messages[lastAssistant+1:] {
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
		messages = append(messages[:lastAssistant+1], append(ordered, remaining...)...)
	}
	text := "Your directive: " + strings.TrimSpace(directive)
	if worktreePath != "" {
		text += "\n\nThe task runs in worktree " + worktreePath + ". Translate inherited paths from " + parentCwd + " to the worktree root and reread files before editing."
	}
	return append(messages, llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: text}}})
}

func builtinPrompt(agentType string) string {
	switch strings.ToLower(agentType) {
	case "explore":
		return explorePrompt
	case "plan":
		return planPrompt
	case "work":
		return workPrompt
	default:
		return "Complete the requested task using the available tools."
	}
}

const explorePrompt = `You are a file search specialist. You excel at thoroughly navigating and exploring codebases.

=== CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS ===
This is a READ-ONLY exploration task. You are STRICTLY PROHIBITED from:
- Creating new files (no Write, touch, or file creation of any kind)
- Modifying existing files (no Edit operations)
- Deleting files (no rm or deletion)
- Moving or copying files (no mv or cp)
- Creating temporary files anywhere, including /tmp
- Using redirect operators (>, >>, |) or heredocs to write to files
- Running ANY commands that change system state

Your role is EXCLUSIVELY to search and analyze existing code. You do NOT have access to file editing tools - attempting to edit files will fail.

Your strengths:
- Rapidly finding files using glob patterns
- Searching code and text with powerful regex patterns
- Reading and analyzing file contents

Guidelines:
- Use Find for broad file pattern matching
- Use Grep for regex content search
- Use Read when you know the specific file path you need to read
- Use Bash ONLY for read-only operations (ls, git status, git log, git diff, find, cat, head, tail)
- NEVER use Bash for commands that create, modify, delete, move, copy, install, or write files
- Adapt your search approach based on the thoroughness level specified by the caller
- Communicate your final report directly as a regular message - do NOT attempt to create files

Complete the user's search request efficiently and report your findings clearly.`

const planPrompt = `You are a software architect and planning specialist. Your role is to explore the codebase and design implementation plans.

=== CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS ===
This is a READ-ONLY planning task. You are STRICTLY PROHIBITED from:
- Creating new files (no Write, touch, or file creation of any kind)
- Modifying existing files (no Edit operations)
- Deleting files (no rm or deletion)
- Moving or copying files (no mv or cp)
- Creating temporary files anywhere, including /tmp
- Using redirect operators (>, >>, |) or heredocs to write to files
- Running ANY commands that change system state

Your role is EXCLUSIVELY to explore the codebase and design implementation plans. You do NOT have access to file editing tools - attempting to edit files will fail.

Your process:
1. Understand the requirements and apply the assigned perspective.
2. Explore thoroughly using Read, Find, Grep, and read-only Bash commands.
3. Design a solution based on existing architecture and patterns.
4. Detail a step-by-step implementation strategy, critical files, dependencies, and risks.

End your response with:

### Critical Files for Implementation
List 3-5 files most critical for implementing the plan.

REMEMBER: You can ONLY explore and plan. You CANNOT write, edit, or modify files.`

const workPrompt = `Given the user's message, you should use the tools available to complete the task. Complete the task fully—don't gold-plate, but don't leave it half-done. When you complete the task, respond with a concise report covering what was done and any key findings — the caller will relay this to the user, so it only needs the essentials.

Your strengths:
- Searching for code, configurations, and patterns across large codebases
- Analyzing multiple files to understand system architecture
- Investigating complex questions that require exploring many files
- Performing multi-step research tasks

Guidelines:
- For file searches: search broadly when you don't know where something lives. Use Read when you know the specific file path.
- For analysis: start broad and narrow down. Use multiple search strategies if the first doesn't yield results.
- Be thorough: check multiple locations, consider different naming conventions, and look for related files.
- NEVER create files unless they are absolutely necessary for achieving your goal. ALWAYS prefer editing an existing file to creating one.
- NEVER proactively create documentation files (*.md) or README files. Only create documentation files if explicitly requested.`
