package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	textclipboard "github.com/atotto/clipboard"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool"
	"github.com/ywxzm03/Rivo/internal/tui"
	systemclipboard "golang.design/x/clipboard"
)

const maxClipboardImageBytes = 32 * 1024 * 1024

var clipboardInit struct {
	sync.Mutex
	initialized bool
	err         error
}

func initSystemClipboard() error {
	clipboardInit.Lock()
	defer clipboardInit.Unlock()
	if clipboardInit.initialized {
		return clipboardInit.err
	}
	clipboardInit.err = systemclipboard.Init()
	if clipboardInit.err == nil {
		clipboardInit.initialized = true
	}
	return clipboardInit.err
}

// readClipboardData 先读取系统图片/文件，再回退到现有文本剪贴板
func readClipboardData() (tui.ClipboardData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var clipboardErr error
	if err := initSystemClipboard(); err == nil {
		if data, ok := readClipboardFiles(ctx); ok {
			return clipboardImages(data)
		}
		if png, err := systemclipboard.Read(ctx, systemclipboard.FmtImage); err == nil && len(png) > 0 {
			image, err := processClipboardImage(png, "image/png")
			if err != nil {
				return tui.ClipboardData{}, err
			}
			return clipboardImages([]llm.ImageContent{image})
		} else if err != nil && !errors.Is(err, systemclipboard.ErrNoData) && !errors.Is(err, systemclipboard.ErrUnsupported) && !errors.Is(err, systemclipboard.ErrUnavailable) {
			clipboardErr = err
		}
	}
	if png, ok := readWSLClipboardImage(ctx); ok {
		if image, err := processClipboardImage(png, "image/png"); err == nil {
			return clipboardImages([]llm.ImageContent{image})
		}
	}
	if clipboardErr != nil {
		return tui.ClipboardData{}, clipboardErr
	}
	text, err := textclipboard.ReadAll()
	if err != nil {
		return tui.ClipboardData{}, err
	}
	return tui.ClipboardData{Text: text}, nil
}

func clipboardImages(images []llm.ImageContent) (tui.ClipboardData, error) {
	total := 0
	for _, image := range images {
		total += len(image.Data)
	}
	if total > maxClipboardImageBytes {
		return tui.ClipboardData{}, fmt.Errorf("clipboard images exceed the 32 MiB limit")
	}
	return tui.ClipboardData{Images: images}, nil
}

func readClipboardFiles(ctx context.Context) ([]llm.ImageContent, bool) {
	paths, err := systemclipboard.ReadFiles(ctx)
	if err != nil || len(paths) == 0 {
		return nil, false
	}
	var images []llm.ImageContent
	for _, path := range paths {
		data, ok := readClipboardFile(path)
		if !ok {
			continue
		}
		mimeType := tool.SniffImage(data)
		if mimeType == "" {
			continue
		}
		image, err := processClipboardImage(data, mimeType)
		if err == nil {
			images = append(images, image)
		}
	}
	return images, len(images) > 0
}

func readClipboardFile(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxClipboardImageBytes {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxClipboardImageBytes+1))
	if err != nil {
		return nil, false
	}
	if int64(len(data)) > maxClipboardImageBytes {
		return nil, false
	}
	return data, true
}

func processClipboardImage(data []byte, mimeType string) (llm.ImageContent, error) {
	result, err := tool.ProcessImage(data, mimeType)
	if err != nil {
		return llm.ImageContent{}, err
	}
	return llm.ImageContent{Data: result.Data, MIMEType: result.MIMEType}, nil
}

func readWSLClipboardImage(ctx context.Context) ([]byte, bool) {
	if runtime.GOOS != "linux" || !isWSL() {
		return nil, false
	}
	const script = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $img = Get-Clipboard -Format Image; if ($img -eq $null) { exit 1 }; $ms = New-Object System.IO.MemoryStream; $img.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png); [Convert]::ToBase64String($ms.ToArray())`
	for _, command := range []string{"powershell.exe", "pwsh", "powershell"} {
		output, err := exec.CommandContext(ctx, command, "-NoProfile", "-Command", script).Output()
		if err != nil {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(output)))
		if err == nil && len(data) > 0 {
			return data, true
		}
	}
	return nil, false
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft")
}
