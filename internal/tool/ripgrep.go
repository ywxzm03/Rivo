package tool

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	execpkg "os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const ripgrepRepo = "BurntSushi/ripgrep"
const ripgrepBinary = "rg"

var errRipgrepUnavailable = errors.New("ripgrep (rg) is not available and could not be downloaded")

// findRipgrep 查找系统或缓存中的 rg，必要时下载到缓存
func findRipgrep(ctx context.Context) (string, error) {
	if path, err := execpkg.LookPath(ripgrepBinary); err == nil {
		return path, nil
	}

	cacheDir, err := os.UserCacheDir()
	if err == nil {
		cached := filepath.Join(cacheDir, "rivo", "bin", executableName())
		if isExecutable(cached) {
			return cached, nil
		}
		if offlineModeEnabled() {
			return "", errRipgrepUnavailable
		}
		if err := downloadRipgrep(ctx, filepath.Dir(cached)); err == nil && isExecutable(cached) {
			return cached, nil
		}
	}
	return "", errRipgrepUnavailable
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "rg.exe"
	}
	return ripgrepBinary
}

// isExecutable 判断路径是否指向可执行文件
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0)
}

// offlineModeEnabled 判断是否启用离线模式
func offlineModeEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("RIVO_OFFLINE")))
	return value == "1" || value == "true" || value == "yes"
}

// assetName 根据平台生成 ripgrep 发布资产名
func assetName(goos, goarch, version string) (string, error) {
	var triple string
	var extension string
	switch {
	case goos == "darwin" && goarch == "arm64":
		triple, extension = "aarch64-apple-darwin", ".tar.gz"
	case goos == "darwin" && goarch == "amd64":
		triple, extension = "x86_64-apple-darwin", ".tar.gz"
	case goos == "linux" && goarch == "arm64":
		triple, extension = "aarch64-unknown-linux-musl", ".tar.gz"
	case goos == "linux" && goarch == "amd64":
		triple, extension = "x86_64-unknown-linux-musl", ".tar.gz"
	case goos == "windows" && goarch == "amd64":
		triple, extension = "x86_64-pc-windows-msvc", ".zip"
	default:
		return "", fmt.Errorf("unsupported platform: %s/%s", goos, goarch)
	}
	return fmt.Sprintf("ripgrep-%s-%s%s", version, triple, extension), nil
}

// downloadRipgrep 下载并解包 ripgrep 到缓存目录
func downloadRipgrep(ctx context.Context, cacheDir string) error {
	version, err := latestRipgrepVersion(ctx)
	if err != nil {
		return err
	}
	asset, err := assetName(runtime.GOOS, runtime.GOARCH, version)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	workDir, err := os.MkdirTemp(cacheDir, ".rivo-rg-download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	archivePath := filepath.Join(workDir, asset)
	downloadURL := "https://github.com/" + ripgrepRepo + "/releases/download/" + version + "/" + asset
	if err := downloadArchive(ctx, downloadURL, archivePath); err != nil {
		return err
	}

	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	var binary []byte
	// 1.按平台归档格式解包并提取 rg
	if strings.HasSuffix(asset, ".tar.gz") {
		binary, err = extractTarGz(archiveData, executableName())
	} else {
		binary, err = extractZip(archiveData, executableName())
	}
	if err != nil {
		return err
	}

	target := filepath.Join(cacheDir, executableName())
	temporary, err := os.CreateTemp(cacheDir, ".rivo-rg-bin-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(binary); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(temporaryPath, 0o755); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		if runtime.GOOS == "windows" && isExecutable(target) {
			return nil
		}
		return err
	}
	return nil
}

// latestRipgrepVersion 从 GitHub Releases 重定向获取最新版本
func latestRipgrepVersion(ctx context.Context) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return latestVersion(requestCtx, client, "https://github.com/"+ripgrepRepo+"/releases/latest")
}

// latestVersion 解析发布页的 302 Location 中的版本号
func latestVersion(ctx context.Context, client *http.Client, releaseURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "rivo-coding-agent")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusMultipleChoices || response.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("failed to resolve latest ripgrep release: HTTP %d without redirect", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("failed to resolve latest ripgrep release: HTTP %d without redirect", response.StatusCode)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	if !strings.Contains(parsed.Path, "/releases/tag/") {
		return "", fmt.Errorf("failed to resolve latest ripgrep release: unexpected redirect to %s", location)
	}
	tag := pathpkg.Base(parsed.Path)
	if tag == "." || tag == "/" || tag == "" {
		return "", fmt.Errorf("failed to resolve latest ripgrep release: unexpected redirect to %s", location)
	}
	if tag, err = url.PathUnescape(tag); err != nil {
		return "", err
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// downloadArchive 下载归档文件到指定路径
func downloadArchive(ctx context.Context, downloadURL, destination string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with HTTP %d: %s", response.StatusCode, downloadURL)
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, response.Body)
	return err
}

// extractTarGz 从 tar.gz 中提取指定二进制
func extractTarGz(data []byte, binaryName string) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == binaryName {
			return io.ReadAll(tarReader)
		}
	}
	return nil, fmt.Errorf("binary not found in archive: %s", binaryName)
}

// extractZip 从 zip 中提取指定二进制
func extractZip(data []byte, binaryName string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != binaryName {
			continue
		}
		input, err := file.Open()
		if err != nil {
			return nil, err
		}
		binary, readErr := io.ReadAll(input)
		input.Close()
		return binary, readErr
	}
	return nil, fmt.Errorf("binary not found in archive: %s", binaryName)
}
