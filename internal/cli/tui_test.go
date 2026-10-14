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

func TestTerminalStreamsRejectsNullDevice(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	if terminalStreams(input, output) {
		t.Fatal("terminalStreams accepted null devices as interactive terminals")
	}
}
