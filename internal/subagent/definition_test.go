package subagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentDefinitionPrefersProjectAndParsesBody(t *testing.T) {
	root := t.TempDir()
	user := t.TempDir()
	path := filepath.Join(root, ".rivo", "agent", "agents")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(user, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review code\nthinking: high\ntools: [read, grep]\nmaxTurns: 4\nomitAgentsMd: true\n---\nInspect the requested files.\n"
	if err := os.WriteFile(filepath.Join(path, "review.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, "agents", "review.md"), []byte("---\nname: review\n---\nwrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := LoadAgentDefinition(root, user, "review")
	if err != nil {
		t.Fatal(err)
	}
	if definition.Prompt != "Inspect the requested files." || definition.Thinking != "high" || definition.MaxTurns != 4 || !definition.OmitAgentsMd {
		t.Fatalf("definition = %#v", definition)
	}
}
