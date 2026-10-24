package subagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ywxzm03/Rivo/internal/llm"
	"gopkg.in/yaml.v3"
)

// AgentDefinition 是 typed subagent 使用的精简 Rivo agent 定义面。
// body 是该 agent 的默认指令 prompt。
type AgentDefinition struct {
	Name         string
	Description  string
	Prompt       string
	Model        string
	Thinking     llm.ThinkingLevel
	Tools        []string
	MaxTurns     int
	OmitAgentsMd bool
}

type agentDefinitionFrontmatter struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Model        string            `yaml:"model"`
	Thinking     llm.ThinkingLevel `yaml:"thinking"`
	Tools        []string          `yaml:"tools"`
	MaxTurns     int               `yaml:"maxTurns"`
	OmitAgentsMd bool              `yaml:"omitAgentsMd"`
}

var agentDefinitionName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// LoadAgentDefinition 先解析项目定义,再解析用户定义
func LoadAgentDefinition(cwd, agentDir, name string) (*AgentDefinition, error) {
	if !agentDefinitionName.MatchString(name) || utf8.RuneCountInString(name) > 64 {
		return nil, fmt.Errorf("subagent: invalid agent name %q", name)
	}
	paths := []string{
		filepath.Join(cwd, ".rivo", "agent", "agents", name+".md"),
		filepath.Join(agentDir, "agents", name+".md"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		definition, err := parseAgentDefinition(string(raw), name)
		if err != nil {
			return nil, fmt.Errorf("subagent: load %s: %w", path, err)
		}
		return definition, nil
	}
	return nil, fmt.Errorf("subagent: agent definition %q not found", name)
}

func parseAgentDefinition(content, fallbackName string) (*AgentDefinition, error) {
	frontmatter, body := splitAgentFrontmatter(strings.TrimPrefix(content, "\ufeff"))
	var fields agentDefinitionFrontmatter
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &fields); err != nil {
			return nil, err
		}
	}
	if fields.Name == "" {
		fields.Name = fallbackName
	}
	if !agentDefinitionName.MatchString(fields.Name) || utf8.RuneCountInString(fields.Name) > 64 {
		return nil, fmt.Errorf("invalid agent name %q", fields.Name)
	}
	if fields.MaxTurns < 0 {
		return nil, errors.New("maxTurns must be non-negative")
	}
	return &AgentDefinition{Name: fields.Name, Description: fields.Description, Prompt: strings.TrimSpace(body), Model: fields.Model, Thinking: fields.Thinking, Tools: append([]string(nil), fields.Tools...), MaxTurns: fields.MaxTurns, OmitAgentsMd: fields.OmitAgentsMd}, nil
}

func splitAgentFrontmatter(content string) (string, string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}
	rest := content[len("---\n"):]
	if index := strings.Index(rest, "\n---"); index >= 0 {
		end := index + len("\n---")
		return rest[:index], strings.TrimPrefix(rest[end:], "\n")
	}
	return "", content
}
