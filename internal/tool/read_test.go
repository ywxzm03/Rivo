package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// exec 跑一次 read 工具，把返回内容里的文本拼起来（纯文本用例用它）
func execRead(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	content, err := (&Read{SupportsImages: true}).Execute(context.Background(), args)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range content.Content {
		if v, ok := c.(llm.TextContent); ok {
			b.WriteString(v.Text)
		}
	}
	return b.String(), nil
}

// numberedLines 造 "Line 1" 到 "Line n"
func numberedLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("Line %d", i+1)
	}
	return lines
}

// writeTemp 在临时目录写一个文件并返回绝对路径
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSmallFile(t *testing.T) {
	const content = "Hello, world!\nLine 2\nLine 3"
	out, err := execRead(t, map[string]any{"path": writeTemp(t, "a.txt", content)})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("output = %q, want %q (no line numbers, no continuation notice)", out, content)
	}
}

func TestReadKeepsTrailingNewline(t *testing.T) {
	const content = "a\nb\n"
	out, err := execRead(t, map[string]any{"path": writeTemp(t, "a.txt", content)})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("output = %q, want %q", out, content)
	}
}

func TestReadEmptyFile(t *testing.T) {
	out, err := execRead(t, map[string]any{"path": writeTemp(t, "empty.txt", "")})
	if err != nil {
		t.Fatalf("empty file should not error: %v", err)
	}
	if out != "" {
		t.Errorf("output = %q, want empty string", out)
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := execRead(t, map[string]any{"path": filepath.Join(t.TempDir(), "nope.txt")})
	if err == nil {
		t.Fatal("err = nil, want a missing-file error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestReadMissingPathParam(t *testing.T) {
	_, err := execRead(t, map[string]any{})
	if err == nil {
		t.Fatal("err = nil, want a missing-path error")
	}
}

func TestReadRelativePath(t *testing.T) {
	const content = "relative"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := execRead(t, map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("output = %q, want %q", out, content)
	}
}

func TestReadTildePath(t *testing.T) {
	const content = "home"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	out, err := execRead(t, map[string]any{"path": "~/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("output = %q, want %q", out, content)
	}
}

func TestReadTruncatesByLines(t *testing.T) {
	rows := numberedLines(2500)
	path := writeTemp(t, "big.txt", strings.Join(rows, "\n"))

	out, err := execRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:2000], "\n") +
		"\n\n[Showing lines 1-2000 of 2500. Use offset=2001 to continue.]"
	if out != want {
		t.Errorf("output mismatch\n got tail %q\n want tail %q", tail(out), tail(want))
	}
}

func TestReadTruncatesByBytes(t *testing.T) {
	// 等长行：每行 200 字节，第 i(>=1) 行在流里占 201 字节
	// 累计 201k-1 <= 51200 ⇒ k=254；再加一行 51254 > 51200
	row := strings.Repeat("x", 200)
	rows := make([]string, 500)
	for i := range rows {
		rows[i] = row
	}
	path := writeTemp(t, "wide.txt", strings.Join(rows, "\n"))

	out, err := execRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:254], "\n") +
		"\n\n[Showing lines 1-254 of 500 (50.0KB limit). Use offset=255 to continue.]"
	if out != want {
		t.Errorf("output mismatch\n got tail %q\n want tail %q", tail(out), tail(want))
	}
}

func TestReadOffset(t *testing.T) {
	rows := numberedLines(100)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	out, err := execRead(t, map[string]any{"path": path, "offset": float64(51)})
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(rows[50:], "\n"); out != want {
		t.Errorf("output mismatch\n got head %q\n want head %q", head(out), head(want))
	}
	if strings.Contains(out, "Use offset=") {
		t.Error("reading to EOF should not show a continuation notice")
	}
}

func TestReadLimit(t *testing.T) {
	rows := numberedLines(100)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	out, err := execRead(t, map[string]any{"path": path, "limit": float64(10)})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:10], "\n") +
		"\n\n[Showing lines 1-10 of 100. Use offset=11 to continue.]"
	if out != want {
		t.Errorf("output mismatch\n got tail %q\n want tail %q", tail(out), tail(want))
	}
}

func TestReadOffsetWithLimit(t *testing.T) {
	rows := numberedLines(100)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	out, err := execRead(t, map[string]any{"path": path, "offset": float64(41), "limit": float64(20)})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[40:60], "\n") +
		"\n\n[Showing lines 41-60 of 100. Use offset=61 to continue.]"
	if out != want {
		t.Errorf("output mismatch\n got tail %q\n want tail %q", tail(out), tail(want))
	}
}

// limit 大于工具上限时两个成因同时成立，续读位置必须按"实际返回的最后一行的下一行"算，
// 否则会给出跳过内容的 offset
func TestReadLimitBeyondToolCap(t *testing.T) {
	rows := numberedLines(10000)
	path := writeTemp(t, "huge.txt", strings.Join(rows, "\n"))

	out, err := execRead(t, map[string]any{"path": path, "limit": float64(5000)})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:2000], "\n") +
		"\n\n[Showing lines 1-2000 of 10000. Use offset=2001 to continue.]"
	if out != want {
		t.Errorf("output mismatch\n got tail %q\n want tail %q", tail(out), tail(want))
	}
}

func TestReadOffsetBeyondEnd(t *testing.T) {
	path := writeTemp(t, "a.txt", "Line 1\nLine 2\nLine 3")

	_, err := execRead(t, map[string]any{"path": path, "offset": float64(100)})
	if err == nil {
		t.Fatal("err = nil, want an out-of-range error")
	}
	want := "Offset 100 is beyond end of file (3 lines total)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

func TestReadFirstLineExceedsByteLimit(t *testing.T) {
	path := writeTemp(t, "one.txt", strings.Repeat("x", 60000))

	out, err := execRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	want := "[Line 1 is 58.6KB, exceeds 50.0KB limit. Use offset=2 to continue.]"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if strings.Contains(out, "bash") {
		t.Error("the notice should not mention bash — Rivo has no such tool")
	}
}

// limit 非正数当作没给；负值更不能流进切片下标
func TestReadNonPositiveLimitReadsWholeFile(t *testing.T) {
	rows := numberedLines(10)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	for _, limit := range []float64{-5, 0} {
		out, err := execRead(t, map[string]any{"path": path, "limit": limit})
		if err != nil {
			t.Fatalf("limit=%v: %v", limit, err)
		}
		if want := strings.Join(rows, "\n"); out != want {
			t.Errorf("limit=%v: output = %q, want the whole file", limit, head(out))
		}
	}
}

func TestReadDefinition(t *testing.T) {
	r := &Read{}

	if r.Name() != "read" {
		t.Errorf("Name() = %q, want %q (model-facing identifier must match pi)", r.Name(), "read")
	}
	if !strings.Contains(r.Description(), "truncated to 2000 lines or 50KB") {
		t.Errorf("Description() = %q, want it to mention the truncation limit", r.Description())
	}
	if !strings.Contains(r.Description(), "images (jpg, png, webp)") {
		t.Errorf("Description() = %q, want it to mention supported image formats", r.Description())
	}
	if strings.Contains(r.Description(), "gif") || strings.Contains(r.Description(), "bmp") {
		t.Error("Description() should not mention gif/bmp — this tool does not support them")
	}

	params := r.Parameters()
	if params["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", params["type"])
	}
	required, ok := params["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "path" {
		t.Errorf("parameters.required = %v, want [path]", params["required"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters.properties = %T, want map", params["properties"])
	}
	for _, key := range []string{"path", "offset", "limit"} {
		if _, ok := props[key]; !ok {
			t.Errorf("parameters.properties is missing %q", key)
		}
	}
}

// makePNG 现场生成一张纯色 PNG
func makePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeJPEG 现场生成一张纯色 JPEG
func makeJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeNoisePNG 生成一张随机噪点 PNG，它压不动，用来逼出逐轮缩小
func makeNoisePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.Intn(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTempBytes(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSniffImage(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"png", makePNG(t, 2, 2), "image/png"},
		{"jpeg", makeJPEG(t, 2, 2), "image/jpeg"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		{"plain text", []byte("hello world"), ""},
		{"empty file", nil, ""},
		{"jpeg-ls is not jpeg", []byte{0xff, 0xd8, 0xff, 0xf7}, ""},
		{"riff but not webp", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sniffImage(tt.data); got != tt.want {
				t.Errorf("sniffImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadImageAsAttachment(t *testing.T) {
	data := makePNG(t, 4, 4)
	path := writeTempBytes(t, "pic.png", data)

	content, err := (&Read{SupportsImages: true}).Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Content) != 2 {
		t.Fatalf("content has %d items, want 2 (text + image)", len(content.Content))
	}

	text, ok := content.Content[0].(llm.TextContent)
	if !ok || text.Text != "Read image file [image/png]" {
		t.Errorf("content[0] = %+v, want an image description", content.Content[0])
	}
	img, ok := content.Content[1].(llm.ImageContent)
	if !ok {
		t.Fatalf("content[1] = %T, want ImageContent", content.Content[1])
	}
	if img.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", img.MIMEType)
	}
	// 小图应当原样返回，不重新编码
	if want := base64.StdEncoding.EncodeToString(data); img.Data != want {
		t.Error("small image base64 should match the original bytes (no re-encoding)")
	}
}

func TestReadImageWithoutVisionSupport(t *testing.T) {
	path := writeTempBytes(t, "pic.png", makePNG(t, 7, 5))

	content, err := (&Read{}).Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Content) != 1 {
		t.Fatalf("content has %d items, want 1 (model takes no image; text only)", len(content.Content))
	}
	text, _ := content.Content[0].(llm.TextContent)
	want := "Read image file [image/png]\n" + noVisionNote
	if text.Text != want {
		t.Errorf("text = %q, want %q (model cannot see the image; dimensions are useless)", text.Text, want)
	}
}

// 模型不收图时完全不该碰图片内容：坏图与好图给出同样的说明
func TestReadImageWithoutVisionDoesNotTouchImage(t *testing.T) {
	broken := append(append([]byte{}, pngSignature...), []byte("this is not a real png")...)
	path := writeTempBytes(t, "broken.png", broken)

	content, err := (&Read{}).Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := content.Content[0].(llm.TextContent)
	want := "Read image file [image/png]\n" + noVisionNote
	if text.Text != want {
		t.Errorf("text = %q, want %q (it decoded the broken image; it should not)", text.Text, want)
	}
}

// 质量档位必须严格递减，否则某一档永远选不到（pi 原本的顺序就踩了这个）
func TestImageJPEGQualitiesDescend(t *testing.T) {
	for i := 1; i < len(imageJPEGQualities); i++ {
		if imageJPEGQualities[i] >= imageJPEGQualities[i-1] {
			t.Errorf("quality tier %d (%d) is not smaller than the previous %d — this tier is never selected",
				i, imageJPEGQualities[i], imageJPEGQualities[i-1])
		}
	}
}

func TestReadImageDecodeFailure(t *testing.T) {
	// 魔数对但内容是垃圾，不能 panic
	bad := append(append([]byte{}, pngSignature...), []byte("not a real png")...)
	path := writeTempBytes(t, "broken.png", bad)

	content, err := (&Read{SupportsImages: true}).Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(content.Content) != 1 {
		t.Fatalf("content has %d items, want 1 (text description only)", len(content.Content))
	}
	text, _ := content.Content[0].(llm.TextContent)
	if !strings.Contains(text.Text, "could not be converted") {
		t.Errorf("missing failure description: %q", text.Text)
	}
}

func TestProcessImage(t *testing.T) {
	t.Run("scales down proportionally when oversized", func(t *testing.T) {
		result, err := processImage(makePNG(t, 3000, 100), "image/png", defaultImageLimits)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Resized {
			t.Error("Resized = false, want true")
		}
		if result.Width != imageMaxWidth {
			t.Errorf("Width = %d, want %d", result.Width, imageMaxWidth)
		}
		if result.OriginalWidth != 3000 || result.OriginalHeight != 100 {
			t.Errorf("original size = %dx%d, want 3000x100", result.OriginalWidth, result.OriginalHeight)
		}
		note := dimensionNote(result)
		if !strings.Contains(note, "original 3000x100") {
			t.Errorf("dimensionNote() = %q", note)
		}
		if !strings.Contains(note, "Multiply coordinates by") {
			t.Errorf("dimensionNote() = %q, want it to mention coordinate conversion", note)
		}
	})

	t.Run("shrinks round by round until it fits when over the byte limit", func(t *testing.T) {
		// 上限压到 4KB：200x200 的噪点图连 JPEG 最低质量都装不下，必须靠缩尺寸
		limits := imageLimits{maxWidth: 1000, maxHeight: 1000, maxBytes: 4 * 1024}
		result, err := processImage(makeNoisePNG(t, 200, 200), "image/png", limits)
		if err != nil {
			t.Fatalf("err = %v, want it to shrink within the limit", err)
		}
		if len(result.Data) >= limits.maxBytes {
			t.Errorf("output is %d bytes, still over the limit %d", len(result.Data), limits.maxBytes)
		}
		if result.Width >= 200 {
			t.Errorf("Width = %d, want it below 200 (should have been resized)", result.Width)
		}
	})

	t.Run("re-encoding at the same size fits", func(t *testing.T) {
		// 上限够大：不必缩尺寸，换 JPEG 编码即可，Resized 仍为 true
		limits := imageLimits{maxWidth: 1000, maxHeight: 1000, maxBytes: 20 * 1024}
		result, err := processImage(makeNoisePNG(t, 200, 200), "image/png", limits)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Resized {
			t.Error("Resized = false, want true (re-encoded)")
		}
		if result.Width != 200 {
			t.Errorf("Width = %d, want 200 (size unchanged; only re-encoded)", result.Width)
		}
		if result.MIMEType == "image/png" {
			t.Errorf("MIMEType = %q, want it switched to the smaller JPEG", result.MIMEType)
		}
	})

	t.Run("errors when even 1x1 does not fit", func(t *testing.T) {
		limits := imageLimits{maxWidth: 1000, maxHeight: 1000, maxBytes: 10}
		_, err := processImage(makeNoisePNG(t, 50, 50), "image/png", limits)
		if !errors.Is(err, errImageTooBig) {
			t.Errorf("err = %v, want errImageTooBig", err)
		}
	})

	t.Run("web formats are handled too", func(t *testing.T) {
		// 用 JPEG 走一遍，确认解码器分派正确
		result, err := processImage(makeJPEG(t, 20, 20), "image/jpeg", defaultImageLimits)
		if err != nil {
			t.Fatal(err)
		}
		if result.MIMEType != "image/jpeg" {
			t.Errorf("MIMEType = %q, want image/jpeg", result.MIMEType)
		}
	})
}

func head(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func tail(s string) string {
	if len(s) > 80 {
		return "..." + s[len(s)-80:]
	}
	return s
}
