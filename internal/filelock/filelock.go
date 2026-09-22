// Package filelock provides an exclusive advisory lock on a file, shared by
// concurrent goroutines and processes.
package filelock

import "os"

// Lock is a held exclusive lock.
type Lock struct{ f *os.File }

// Acquire creates path if needed (mode 0600) and blocks until it holds an
// exclusive lock on it. Callers lock a dedicated lock file, never the data
// file, so atomic rename of the data file cannot orphan the lock.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}
