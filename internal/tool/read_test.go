package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exec 跑一次 read 工具
func exec(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	return (&Read{}).Execute(context.Background(), args)
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
	out, err := exec(t, map[string]any{"path": writeTemp(t, "a.txt", content)})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("输出 = %q, want %q（应无行号、无续读提示）", out, content)
	}
}

func TestReadKeepsTrailingNewline(t *testing.T) {
	const content = "a\nb\n"
	out, err := exec(t, map[string]any{"path": writeTemp(t, "a.txt", content)})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("输出 = %q, want %q", out, content)
	}
}

func TestReadEmptyFile(t *testing.T) {
	out, err := exec(t, map[string]any{"path": writeTemp(t, "empty.txt", "")})
	if err != nil {
		t.Fatalf("空文件不该报错: %v", err)
	}
	if out != "" {
		t.Errorf("输出 = %q, want 空串", out)
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := exec(t, map[string]any{"path": filepath.Join(t.TempDir(), "nope.txt")})
	if err == nil {
		t.Fatal("err = nil, want 文件不存在报错")
	}
	if !strings.Contains(err.Error(), "no such file or directory") {
		t.Errorf("err = %v, want 含 no such file or directory", err)
	}
}

func TestReadMissingPathParam(t *testing.T) {
	_, err := exec(t, map[string]any{})
	if err == nil {
		t.Fatal("err = nil, want 缺 path 参数报错")
	}
}

func TestReadRelativePath(t *testing.T) {
	const content = "relative"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	out, err := exec(t, map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("输出 = %q, want %q", out, content)
	}
}

func TestReadTildePath(t *testing.T) {
	const content = "home"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	out, err := exec(t, map[string]any{"path": "~/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Errorf("输出 = %q, want %q", out, content)
	}
}

func TestReadTruncatesByLines(t *testing.T) {
	rows := numberedLines(2500)
	path := writeTemp(t, "big.txt", strings.Join(rows, "\n"))

	out, err := exec(t, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:2000], "\n") +
		"\n\n[Showing lines 1-2000 of 2500. Use offset=2001 to continue.]"
	if out != want {
		t.Errorf("输出不匹配\n got 结尾 %q\n want 结尾 %q", tail(out), tail(want))
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

	out, err := exec(t, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:254], "\n") +
		"\n\n[Showing lines 1-254 of 500 (50.0KB limit). Use offset=255 to continue.]"
	if out != want {
		t.Errorf("输出不匹配\n got 结尾 %q\n want 结尾 %q", tail(out), tail(want))
	}
}

func TestReadOffset(t *testing.T) {
	rows := numberedLines(100)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	out, err := exec(t, map[string]any{"path": path, "offset": float64(51)})
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(rows[50:], "\n"); out != want {
		t.Errorf("输出不匹配\n got 开头 %q\n want 开头 %q", head(out), head(want))
	}
	if strings.Contains(out, "Use offset=") {
		t.Error("读到文件末尾不该有续读提示")
	}
}

func TestReadLimit(t *testing.T) {
	rows := numberedLines(100)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	out, err := exec(t, map[string]any{"path": path, "limit": float64(10)})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:10], "\n") +
		"\n\n[Showing lines 1-10 of 100. Use offset=11 to continue.]"
	if out != want {
		t.Errorf("输出不匹配\n got 结尾 %q\n want 结尾 %q", tail(out), tail(want))
	}
}

func TestReadOffsetWithLimit(t *testing.T) {
	rows := numberedLines(100)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	out, err := exec(t, map[string]any{"path": path, "offset": float64(41), "limit": float64(20)})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[40:60], "\n") +
		"\n\n[Showing lines 41-60 of 100. Use offset=61 to continue.]"
	if out != want {
		t.Errorf("输出不匹配\n got 结尾 %q\n want 结尾 %q", tail(out), tail(want))
	}
}

// limit 大于工具上限时两个成因同时成立，续读位置必须按"实际返回的最后一行的下一行"算，
// 否则会给出跳过内容的 offset
func TestReadLimitBeyondToolCap(t *testing.T) {
	rows := numberedLines(10000)
	path := writeTemp(t, "huge.txt", strings.Join(rows, "\n"))

	out, err := exec(t, map[string]any{"path": path, "limit": float64(5000)})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rows[:2000], "\n") +
		"\n\n[Showing lines 1-2000 of 10000. Use offset=2001 to continue.]"
	if out != want {
		t.Errorf("输出不匹配\n got 结尾 %q\n want 结尾 %q", tail(out), tail(want))
	}
}

func TestReadOffsetBeyondEnd(t *testing.T) {
	path := writeTemp(t, "a.txt", "Line 1\nLine 2\nLine 3")

	_, err := exec(t, map[string]any{"path": path, "offset": float64(100)})
	if err == nil {
		t.Fatal("err = nil, want 越界报错")
	}
	want := "Offset 100 is beyond end of file (3 lines total)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

func TestReadFirstLineExceedsByteLimit(t *testing.T) {
	path := writeTemp(t, "one.txt", strings.Repeat("x", 60000))

	out, err := exec(t, map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	want := "[Line 1 is 58.6KB, exceeds 50.0KB limit. Use offset=2 to continue.]"
	if out != want {
		t.Errorf("输出 = %q, want %q", out, want)
	}
	if strings.Contains(out, "bash") {
		t.Error("提示里不该出现 bash —— Rivo 没有这个工具")
	}
}

// limit 非正数当作没给；负值更不能流进切片下标
func TestReadNonPositiveLimitReadsWholeFile(t *testing.T) {
	rows := numberedLines(10)
	path := writeTemp(t, "a.txt", strings.Join(rows, "\n"))

	for _, limit := range []float64{-5, 0} {
		out, err := exec(t, map[string]any{"path": path, "limit": limit})
		if err != nil {
			t.Fatalf("limit=%v: %v", limit, err)
		}
		if want := strings.Join(rows, "\n"); out != want {
			t.Errorf("limit=%v: 输出 = %q, want 整个文件", limit, head(out))
		}
	}
}

func TestReadDefinition(t *testing.T) {
	r := &Read{}

	if r.Name() != "read" {
		t.Errorf("Name() = %q, want %q（模型看到的标识，必须与 pi 一致）", r.Name(), "read")
	}
	if !strings.Contains(r.Description(), "truncated to 2000 lines or 50KB") {
		t.Errorf("Description() = %q, want 含截断上限", r.Description())
	}
	if strings.Contains(r.Description(), "image") {
		t.Error("Description() 不该提图片 —— 本工具不处理图片")
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
			t.Errorf("parameters.properties 缺 %q", key)
		}
	}
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
