package tool

import "testing"

func TestReadOnlyShellCommandRejectsMutationAndShellInjection(t *testing.T) {
	for _, command := range []string{
		"cat file\nrm -rf target",
		"find . -delete",
		"sort input -o output",
		"git diff --output=target",
		"/bin/sh -c cat",
	} {
		if readOnlyShellCommand(command) {
			t.Errorf("readOnlyShellCommand(%q) = true, want false", command)
		}
	}
	for _, command := range []string{"cat file", "git status --short", "git diff --stat", "rg pattern ."} {
		if !readOnlyShellCommand(command) {
			t.Errorf("readOnlyShellCommand(%q) = false, want true", command)
		}
	}
}
