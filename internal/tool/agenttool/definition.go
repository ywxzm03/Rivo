package agenttool

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

// Definition 是 typed subagent 使用的精简 agent 定义面。
// body 是该 agent 的默认指令 prompt。
type Definition struct {
	Name         string
	Description  string
	Prompt       string
	Model        string
	Thinking     llm.ThinkingLevel
	Tools        []string
	MaxTurns     int
	OmitAgentsMd bool
}

type definitionFrontmatter struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Model        string            `yaml:"model"`
	Thinking     llm.ThinkingLevel `yaml:"thinking"`
	Tools        []string          `yaml:"tools"`
	MaxTurns     int               `yaml:"maxTurns"`
	OmitAgentsMd bool              `yaml:"omitAgentsMd"`
}

var definitionName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// LoadDefinition 先解析项目定义,再解析用户定义
func LoadDefinition(cwd, agentDir, name string) (*Definition, error) {
	if !definitionName.MatchString(name) || utf8.RuneCountInString(name) > 64 {
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
		definition, err := parseDefinition(string(raw), name)
		if err != nil {
			return nil, fmt.Errorf("subagent: load %s: %w", path, err)
		}
		return definition, nil
	}
	return nil, fmt.Errorf("subagent: agent definition %q not found", name)
}

func parseDefinition(content, fallbackName string) (*Definition, error) {
	frontmatter, body := splitFrontmatter(strings.TrimPrefix(content, "\ufeff"))
	var fields definitionFrontmatter
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &fields); err != nil {
			return nil, err
		}
	}
	if fields.Name == "" {
		fields.Name = fallbackName
	}
	if !definitionName.MatchString(fields.Name) || utf8.RuneCountInString(fields.Name) > 64 {
		return nil, fmt.Errorf("invalid agent name %q", fields.Name)
	}
	if fields.MaxTurns < 0 {
		return nil, errors.New("maxTurns must be non-negative")
	}
	return &Definition{Name: fields.Name, Description: fields.Description, Prompt: strings.TrimSpace(body), Model: fields.Model, Thinking: fields.Thinking, Tools: append([]string(nil), fields.Tools...), MaxTurns: fields.MaxTurns, OmitAgentsMd: fields.OmitAgentsMd}, nil
}

func splitFrontmatter(content string) (string, string) {
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
