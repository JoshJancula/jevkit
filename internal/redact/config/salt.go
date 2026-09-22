package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const saltBytes = 32

// LoadSalt returns the per-install salt for stable placeholders. The file
// holds hex and must be 0600; it is created with fresh random bytes the first
// time. A missing path, a permissive mode or a malformed file fails closed.
func LoadSalt(file string) ([]byte, error) {
	if file == "" {
		return nil, fail("(salt)", "placeholder", "stable placeholders need a salt file path")
	}
	for attempt := 0; attempt < 2; attempt++ {
		data, missing, err := readRegular(file, true)
		if err != nil {
			return nil, err
		}
		if !missing {
			salt, err := hex.DecodeString(strings.TrimSpace(string(data)))
			if err != nil || len(salt) != saltBytes {
				return nil, fail(file, "", "salt file is malformed (want %d bytes of hex)", saltBytes)
			}
			return salt, nil
		}
		if err := createSalt(file); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fail(file, "", "creating salt: %v", err)
		}
	}
	return nil, fail(file, "", "could not create salt")
}

func createSalt(file string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(hex.EncodeToString(salt) + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(file)
		return err
	}
	return f.Close()
}
