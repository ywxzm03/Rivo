//go:build !windows

package tool

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeFakeRipgrep(t *testing.T, output string, exitCode int, stderr string) (argsPath string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "rg")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RG_ARGS_FILE\"\nif [ -n \"$RG_STDERR\" ]; then printf '%s' \"$RG_STDERR\" >&2; fi\nprintf '%s' \"$RG_OUTPUT\"\ncase \"$RG_OUTPUT\" in\n  '') ;;\n  *'\n') ;;\n  *) printf '\\n' ;;\nesac\nif [ -n \"$RG_SLEEP_SECONDS\" ]; then sleep \"$RG_SLEEP_SECONDS\"; fi\nif [ -n \"$RG_MARKER\" ]; then touch \"$RG_MARKER\"; fi\nexit \"$RG_EXIT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return configureFakeRipgrep(t, dir, filepath.Join(dir, "args"), output, exitCode, stderr)
}

func configureFakeRipgrep(t *testing.T, dir, argsPath, output string, exitCode int, stderr string) string {
	t.Helper()
	t.Setenv("PATH", dir)
	t.Setenv("RG_ARGS_FILE", argsPath)
	t.Setenv("RG_OUTPUT", output)
	t.Setenv("RG_EXIT", strconv.Itoa(exitCode))
	t.Setenv("RG_STDERR", stderr)
	t.Setenv("RG_SLEEP_SECONDS", "")
	t.Setenv("RG_MARKER", "")
	return argsPath
}
