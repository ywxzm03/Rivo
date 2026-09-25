package tool

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "ripgrep-14.1.1-aarch64-apple-darwin.tar.gz"},
		{"darwin", "amd64", "ripgrep-14.1.1-x86_64-apple-darwin.tar.gz"},
		{"linux", "arm64", "ripgrep-14.1.1-aarch64-unknown-linux-musl.tar.gz"},
		{"linux", "amd64", "ripgrep-14.1.1-x86_64-unknown-linux-musl.tar.gz"},
		{"windows", "amd64", "ripgrep-14.1.1-x86_64-pc-windows-msvc.zip"},
	}
	for _, tt := range tests {
		got, err := assetName(tt.goos, tt.goarch, "14.1.1")
		if err != nil || got != tt.want {
			t.Errorf("assetName(%q, %q) = %q, %v; want %q", tt.goos, tt.goarch, got, err, tt.want)
		}
	}
	if _, err := assetName("freebsd", "amd64", "14.1.1"); err == nil {
		t.Error("assetName() error = nil, want unsupported platform error")
	}
}

func TestLatestVersion(t *testing.T) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://github.com/BurntSushi/ripgrep/releases/tag/14.1.1"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}
	version, err := latestVersion(t.Context(), client, "https://github.com/BurntSushi/ripgrep/releases/latest")
	if err != nil {
		t.Fatal(err)
	}
	if version != "14.1.1" {
		t.Errorf("version = %q, want 14.1.1", version)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestFindRipgrepOffline(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RIVO_OFFLINE", "1")

	_, err := findRipgrep(t.Context())
	if err == nil || err.Error() != errRipgrepUnavailable.Error() {
		t.Fatalf("findRipgrep() error = %v, want unavailable error", err)
	}
}

func TestFindRipgrepUsesCache(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RIVO_OFFLINE", "yes")

	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "rivo", "bin", executableName())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := findRipgrep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != path || !strings.HasSuffix(got, executableName()) {
		t.Errorf("findRipgrep() = %q, want %q", got, path)
	}
}
