package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadAndReplace(t *testing.T) {
	body := tarGz(t, "new binary")
	sum := sha256.Sum256(body)
	var s *httptest.Server
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			_, _ = fmt.Fprintf(w, `{"tag_name":"v1.2.3","assets":[{"name":"jevkit_1.2.3_linux_amd64.tar.gz","browser_download_url":"%s/a"},{"name":"checksums.txt","browser_download_url":"%s/c"}]}`, s.URL, s.URL)
		case "/a":
			_, _ = w.Write(body)
		case "/c":
			_, _ = fmt.Fprintf(w, "%x  jevkit_1.2.3_linux_amd64.tar.gz\n", sum)
		}
	}))
	defer s.Close()
	d := t.TempDir()
	c := Client{BaseURL: s.URL, GOOS: "linux", GOARCH: "amd64"}
	r, err := c.Latest()
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(d, "new")
	if err := c.Download(r, candidate); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(d, "jevkit")
	if err := os.WriteFile(target, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Replace(target, candidate); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new binary" {
		t.Fatal(string(got))
	}
}

func tarGz(t *testing.T, content string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "jevkit", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBadChecksumLeavesTarget(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "jevkit")
	if err := os.WriteFile(target, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Replace(target, filepath.Join(d, "missing")); err == nil {
		t.Fatal("expected error")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Fatal("target changed")
	}
	if Managed("/usr/local/Cellar/jevkit/bin/jevkit") != "brew upgrade jevkit" {
		t.Fatal("brew")
	}
}
