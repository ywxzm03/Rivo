package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// execLs 跑一次 ls 工具，把返回内容里的文本拼起来
func execLs(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	content, err := (&Ls{}).Execute(context.Background(), args)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range content {
		if v, ok := c.(llm.TextContent); ok {
			b.WriteString(v.Text)
		}
	}
	return b.String(), nil
}

// touch 造一个空文件，名字里的 "/" 会被建成子目录
func touch(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(name)), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestLsListsFilesAndDirs(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "afile.txt")
	touch(t, dir, ".hidden-file")
	mkdir(t, dir, "zdir")
	mkdir(t, dir, ".hidden-dir")

	out, err := execLs(t, map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	// 一条一行、目录带斜杠、dotfiles 也在、末尾无换行
	want := ".hidden-dir/\n.hidden-file\nafile.txt\nzdir/"
	if out != want {
		t.Errorf("输出 = %q, want %q", out, want)
	}
}

func TestLsSortsCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "Zebra")
	touch(t, dir, "apple")

	out, err := execLs(t, map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	// 按字节序 Zebra 会排在前面，大小写不敏感则相反
	if want := "apple\nZebra"; out != want {
		t.Errorf("输出 = %q, want %q", out, want)
	}
}

func TestLsEmptyDirectory(t *testing.T) {
	out, err := execLs(t, map[string]any{"path": t.TempDir()})
	if err != nil {
		t.Fatalf("空目录不该报错: %v", err)
	}
	if out != "(empty directory)" {
		t.Errorf("输出 = %q, want %q", out, "(empty directory)")
	}
}

func TestLsPathNotFound(t *testing.T) {
	want := filepath.Join(t.TempDir(), "nope")

	_, err := execLs(t, map[string]any{"path": want})
	if err == nil {
		t.Fatal("err = nil, want 路径不存在报错")
	}
	if got := err.Error(); got != "Path not found: "+want {
		t.Errorf("err = %q, want %q", got, "Path not found: "+want)
	}
}

func TestLsNotADirectory(t *testing.T) {
	path := writeTemp(t, "a.txt", "x")

	_, err := execLs(t, map[string]any{"path": path})
	if err == nil {
		t.Fatal("err = nil, want 不是目录报错")
	}
	if got := err.Error(); got != "Not a directory: "+path {
		t.Errorf("err = %q, want %q", got, "Not a directory: "+path)
	}
}

func TestLsDefaultsToCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "here.txt")
	t.Chdir(dir)

	out, err := execLs(t, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "here.txt" {
		t.Errorf("输出 = %q, want %q", out, "here.txt")
	}
}

func TestLsLimit(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		touch(t, dir, fmt.Sprintf("f%02d", i))
	}

	out, err := execLs(t, map[string]any{"path": dir, "limit": float64(3)})
	if err != nil {
		t.Fatal(err)
	}
	want := "f00\nf01\nf02\n\n[Showing 3 of 10 entries. Use limit=6 to continue.]"
	if out != want {
		t.Errorf("输出 = %q, want %q", out, want)
	}
}

// limit 非正数当作没给；pi 那边会让它真生效，于是误报 (empty directory)
func TestLsNonPositiveLimitUsesDefault(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "only.txt")

	for _, limit := range []float64{0, -5} {
		out, err := execLs(t, map[string]any{"path": dir, "limit": limit})
		if err != nil {
			t.Fatalf("limit=%v: %v", limit, err)
		}
		if out != "only.txt" {
			t.Errorf("limit=%v: 输出 = %q, want %q（有文件，不该说空目录）", limit, out, "only.txt")
		}
	}
}

func TestLsTruncatesByBytes(t *testing.T) {
	dir := t.TempDir()
	// 250 个约 243 字节的名字 ≈ 60KB，超过 50KB 上限
	long := strings.Repeat("n", 240)
	for i := range 250 {
		touch(t, dir, fmt.Sprintf("%03d%s", i, long))
	}

	out, err := execLs(t, map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}

	// 截断了也要把已经读到的条目带回来
	if !strings.HasPrefix(out, "000"+long+"\n") {
		t.Errorf("输出开头不是第一个条目，内容没带回来: %q", head(out))
	}
	if !strings.Contains(out, "entries (50.0KB limit).]") {
		t.Errorf("缺截断提示: %q", tail(out))
	}
	// 字节上限卡住时调大 limit 也装不下，不该给这个建议
	if strings.Contains(out, "to continue.") {
		t.Error("字节上限卡住时不该建议继续——调大 limit 也没用")
	}
	if len(out) > defaultMaxBytes+200 {
		t.Errorf("输出 %d 字节，超过上限 %d", len(out), defaultMaxBytes)
	}
}

func TestLsFollowsSymlinkToDirectory(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, dir, "real")
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	out, err := execLs(t, map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if want := "link/\nreal/"; out != want {
		t.Errorf("输出 = %q, want %q（跟随符号链接，指向目录也该带斜杠）", out, want)
	}
}

func TestLsSkipsBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "ok.txt")
	if err := os.Symlink(filepath.Join(dir, "nonexistent"), filepath.Join(dir, "broken")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	out, err := execLs(t, map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok.txt" {
		t.Errorf("输出 = %q, want %q（断链的条目应被静默跳过）", out, "ok.txt")
	}
}

func TestLsDefinition(t *testing.T) {
	l := &Ls{}

	if l.Name() != "ls" {
		t.Errorf("Name() = %q, want %q", l.Name(), "ls")
	}

	desc := l.Description()
	for _, want := range []string{"'/' suffix", "Includes dotfiles", "500 entries", "50KB"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description() 缺 %q: %q", want, desc)
		}
	}
	if strings.Contains(desc, "${") {
		t.Errorf("Description() 里有未插值的占位符: %q", desc)
	}

	params := l.Parameters()
	if params["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", params["type"])
	}
	if _, ok := params["required"]; ok {
		t.Error("ls 的两个参数都是可选的，不该有 required")
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters.properties = %T, want map", params["properties"])
	}
	for _, key := range []string{"path", "limit"} {
		if _, ok := props[key]; !ok {
			t.Errorf("properties 缺 %q", key)
		}
	}
}
