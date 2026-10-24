package agenttool

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
	taskpkg "github.com/ywxzm03/Rivo/internal/task"
)

// parseInput 把工具参数解析成强类型输入,缺字段或类型不符时返回明确错误
func parseInput(args map[string]any) (taskpkg.AgentInput, error) {
	input := taskpkg.AgentInput{}
	// 1.必填字段:缺失时报错而不是静默派发空任务
	var err error
	if input.Description, err = requiredString(args, "description"); err != nil {
		return input, err
	}
	if input.Prompt, err = requiredString(args, "prompt"); err != nil {
		return input, err
	}
	// 2.可选字段:未提供时保持 nil,让 Manager 走默认值
	input.Name, err = optionalString(args, "name")
	if err != nil {
		return input, err
	}
	subagentType, err := optionalString(args, "subagent_type")
	if err != nil {
		return input, err
	}
	if subagentType != "" {
		input.SubagentType = &subagentType
	}
	worktree, err := optionalBool(args, "worktree")
	if err != nil {
		return input, err
	}
	input.Worktree = worktree
	thinking, err := optionalString(args, "thinking")
	if err != nil {
		return input, err
	}
	if thinking != "" {
		level := llm.ThinkingLevel(thinking)
		input.Thinking = &level
	}
	model, err := optionalString(args, "model")
	if err != nil {
		return input, err
	}
	if model != "" {
		input.Model = parseModelRef(model)
	}
	return input, nil
}

// parseModelRef 把 provider/id 形式的引用拆成两段,无斜杠时整串作为 ID
func parseModelRef(value string) *llm.Model {
	ref := &llm.Model{ID: value}
	if parts := strings.SplitN(value, "/", 2); len(parts) == 2 {
		ref.Provider, ref.ID = parts[0], parts[1]
	}
	return ref
}

func requiredString(args map[string]any, key string) (string, error) {
	value, ok := args[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("agent: %s is required", key)
	}
	return value, nil
}

func optionalString(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("agent: %s must be a string", key)
	}
	return text, nil
}

func optionalBool(args map[string]any, key string) (*bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("agent: %s must be a boolean", key)
	}
	return &flag, nil
}

var errMissingLauncher = errors.New("agent: launcher is unavailable")
