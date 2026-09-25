//go:build !windows

package worker

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeCancellationKillsSpawnedChild(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "(echo started > ready; sleep 1; echo survived > marker) & wait")
	cmd.Dir = dir
	prepareRuntimeCommand(cmd)
	done := make(chan error, 1)
	go func() { done <- runRuntimeCommand(cmd) }()
	waitForFile(t, filepath.Join(dir, "ready"))
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Fatal("spawned child survived runtime cancellation")
	}
}

func TestRuntimeExitKillsSpawnedChild(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "(sleep 1; echo survived > marker) >&1 & exit 0")
	cmd.Dir = dir
	cmd.Stdout = &bytes.Buffer{}
	prepareRuntimeCommand(cmd)
	start := time.Now()
	if err := runRuntimeCommand(cmd); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("runtime exit waited for a spawned child")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Fatal("spawned child survived runtime exit")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("spawned child did not start")
}
