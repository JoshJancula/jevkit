package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Client struct {
	BaseURL      string
	HTTP         *http.Client
	GOOS, GOARCH string
}
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (c Client) Latest() (Release, error) {
	var r Release
	err := c.getJSON(c.BaseURL+"/releases/latest", &r)
	return r, err
}
func (c Client) getJSON(url string, into any) error {
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	resp, err := h.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("release server: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
func (c Client) os() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}
func (c Client) arch() string {
	if c.GOARCH != "" {
		return c.GOARCH
	}
	return runtime.GOARCH
}

func (c Client) Download(r Release, dest string) error {
	osn := c.os()
	arch := c.arch()
	if osn == "windows" {
		osn = "windows"
	}
	ext := "tar.gz"
	if osn == "windows" {
		ext = "zip"
	}
	name := fmt.Sprintf("jevkit_%s_%s_%s.%s", strings.TrimPrefix(r.TagName, "v"), osn, arch, ext)
	var archive, checks string
	for _, a := range r.Assets {
		if a.Name == name {
			archive = a.BrowserDownloadURL
		}
		if a.Name == "checksums.txt" {
			checks = a.BrowserDownloadURL
		}
	}
	if archive == "" || checks == "" {
		return fmt.Errorf("release is missing %s or checksums.txt", name)
	}
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	resp, err := h.Get(archive)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	sumResp, err := h.Get(checks)
	if err != nil {
		return err
	}
	defer func() { _ = sumResp.Body.Close() }()
	sums, err := io.ReadAll(sumResp.Body)
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.TrimPrefix(f[1], "*") == name {
			want = f[0]
		}
	}
	got := sha256.Sum256(b)
	if want == "" || !strings.EqualFold(want, hex.EncodeToString(got[:])) {
		return fmt.Errorf("checksum verification failed")
	}
	if osn == "windows" {
		return extractZip(b, dest)
	}
	return extractTarGz(b, dest)
}

func extractTarGz(raw []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "jevkit" {
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			return closeErr
		}
	}
	return fmt.Errorf("archive has no jevkit binary")
}
func extractZip(raw []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != "jevkit.exe" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		w, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, r)
		closeErr := w.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return fmt.Errorf("archive has no jevkit.exe binary")
}

func Replace(target, candidate string) error {
	data, err := os.ReadFile(candidate)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".jevkit-upgrade-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(0o755)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, target)
}
func Managed(path string) string {
	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, "node_modules"):
		return "npm update -g jevkit"
	case strings.Contains(p, "homebrew") || strings.Contains(p, "/cellar/"):
		return "brew upgrade jevkit"
	case strings.Contains(p, "scoop"):
		return "scoop update jevkit"
	}
	return ""
}
