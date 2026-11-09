package imageutil_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/imageutil"
)

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// 魔数识别必须区分 JPEG / PNG / WebP，并对未知数据返回空串
func TestSniffRecognizesSupportedFormats(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, "image/jpeg"},
		{"png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, "image/png"},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBPVP8 ")...), "image/webp"},
		{"unknown", []byte("GIF89a"), ""},
		{"empty", nil, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageutil.Sniff(tt.data); got != tt.want {
				t.Errorf("Sniff() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 尺寸在限制内的小图必须原样返回，不重新编码
func TestProcessKeepsSmallImageUnchanged(t *testing.T) {
	raw := encodePNG(t, 8, 8)
	result, err := imageutil.Process(raw, "image/png")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", result.MIMEType)
	}
	if result.Width != 8 || result.Height != 8 {
		t.Errorf("size = %dx%d, want 8x8", result.Width, result.Height)
	}
	if result.Data == "" {
		t.Error("Data is empty")
	}
}

// 超出尺寸上限的图必须被等比缩小
func TestProcessResizesOversizedImage(t *testing.T) {
	raw := encodePNG(t, 3000, 1500)
	result, err := imageutil.Process(raw, "image/png")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result.Width > 2000 || result.Height > 2000 {
		t.Errorf("resized to %dx%d, want within 2000x2000", result.Width, result.Height)
	}
	if result.OriginalWidth != 3000 || result.OriginalHeight != 1500 {
		t.Errorf("original size = %dx%d, want 3000x1500", result.OriginalWidth, result.OriginalHeight)
	}
}

// 无法解码的数据必须返回可展示的错误，而不是 panic
func TestProcessRejectsUndecodableData(t *testing.T) {
	if _, err := imageutil.Process([]byte("not an image"), "image/png"); err == nil {
		t.Fatal("Process() error = nil, want decode error")
	} else if !strings.Contains(err.Error(), "Image omitted") {
		t.Errorf("error = %q, want Image omitted hint", err.Error())
	}
}
