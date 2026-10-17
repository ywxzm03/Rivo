package cli

import (
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestClipboardImagesEnforceTotalLimit(t *testing.T) {
	_, err := clipboardImages([]llm.ImageContent{{Data: strings.Repeat("a", maxClipboardImageBytes+1)}})
	if err == nil || !strings.Contains(err.Error(), "32 MiB") {
		t.Fatalf("clipboardImages error = %v, want total limit error", err)
	}
}

func TestClipboardImagesPreserveOrder(t *testing.T) {
	data, err := clipboardImages([]llm.ImageContent{{MIMEType: "image/png", Data: "one"}, {MIMEType: "image/jpeg", Data: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Images) != 2 || data.Images[0].Data != "one" || data.Images[1].Data != "two" {
		t.Fatalf("images = %#v, want original order", data.Images)
	}
}
