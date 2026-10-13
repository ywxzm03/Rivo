package tui

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestTerminalWrappersPreserveFileDescriptor(t *testing.T) {
	file, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, ok := NewInputReader(file).(terminalFile); !ok {
		t.Fatal("input wrapper lost terminal file methods")
	}
	if _, ok := NewCursorWriter(file, nil).(terminalFile); !ok {
		t.Fatal("output wrapper lost terminal file methods")
	}
}

func TestInputReaderDropsMalformedMouseReports(t *testing.T) {
	r := NewInputReader(strings.NewReader("helloM[<65;76;30Mworld"))
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "helloworld" {
		t.Fatalf("filtered input = %q", data)
	}
}

func TestInputReaderPreservesValidMouseReports(t *testing.T) {
	value := "hello\x1b[<65;76;30Mworld"
	r := NewInputReader(strings.NewReader(value))
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != value {
		t.Fatalf("valid report changed: %q", data)
	}
}

func TestFilterMouseRunesHandlesSplitReports(t *testing.T) {
	var pending string
	var got string
	for _, part := range []string{"[", "<", "64;94;23", "M", "hello"} {
		clean, handled := filterMouseRunes(part, &pending)
		if !handled && part != "hello" {
			t.Fatalf("part %q was not handled", part)
		}
		got += clean
	}
	if got != "hello" || pending != "" {
		t.Fatalf("filtered split report = %q pending=%q", got, pending)
	}
}

func TestFilterMouseRunesPreservesOrdinaryBracketText(t *testing.T) {
	var pending string
	clean, handled := filterMouseRunes("[abc", &pending)
	if handled || clean != "[abc" || pending != "" {
		t.Fatalf("ordinary bracket text = %q handled=%v pending=%q", clean, handled, pending)
	}
}
