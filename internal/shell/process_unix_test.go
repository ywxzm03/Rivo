//go:build !windows

package shell

import "testing"

func TestFindBashUsesSystemPath(t *testing.T) {
	got, err := FindBash()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/bin/bash" {
		t.Errorf("FindBash() = %q, want /bin/bash", got)
	}
}
