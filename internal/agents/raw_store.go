package agents

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func storeRawResult(stateDir, runtime, body string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("state directory unavailable")
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	dir := filepath.Join(stateDir, "tool-results", runtime)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hex.EncodeToString(id)+".log")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func rawResultTrailer(body, path string) string {
	return strings.TrimRight(body, "\n") + "\n[jevkit: shell output compacted; original: " + path + "]\n"
}

func joinToolStreams(stdout, stderr string) string {
	if stdout != "" && stderr != "" {
		return strings.TrimRight(stdout, "\n") + "\n" + stderr
	}
	return stdout + stderr
}
