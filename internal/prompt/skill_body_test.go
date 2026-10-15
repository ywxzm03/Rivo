package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSkillBodyUsesCurrentContentAndNormalizesFrontmatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	for _, content := range []string{"\ufeff---\r\nname: docs\r\ndescription: d\r\n---\r\n\r\n# Instructions\r\nbody\r\n", "# Instructions\nbody", "---\n---\n# Instructions\nbody"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		body, err := ReadSkillBody(path)
		if err != nil || body != "# Instructions\nbody" {
			t.Fatalf("body = %q, err = %v", body, err)
		}
	}
	if err := os.WriteFile(path, []byte("---\ndescription: [broken\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSkillBody(path); err == nil {
		t.Fatal("malformed frontmatter was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSkillBody(path); err == nil {
		t.Fatal("missing skill did not return a read error")
	}
}
