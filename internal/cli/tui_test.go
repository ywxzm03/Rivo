package cli

import (
	"os"
	"testing"
)

func TestWriteOSC52RejectsRedirectedOutput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := writeOSC52(file, "copied"); err == nil {
		t.Fatal("writeOSC52 reported success for redirected output")
	}
}
