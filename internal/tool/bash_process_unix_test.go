//go:build !windows

package tool

import "testing"

func TestFindBashUsesSystemPath(t *testing.T) {
	got, err := findBash()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/bin/bash" {
		t.Errorf("findBash() = %q, want /bin/bash", got)
	}
}
