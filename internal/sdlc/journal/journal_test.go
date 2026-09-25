package journal

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAppendAndReadAll(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := Append(dir, Entry{RunID: "r1", NodeID: "a", Kind: "WorkCompleted"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	entries, err := ReadAll(dir)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	for _, e := range entries {
		if e.RunID != "r1" || e.NodeID != "a" || e.Kind != "WorkCompleted" || e.Timestamp == "" {
			t.Errorf("entry = %+v", e)
		}
	}
}

func TestReadAllMissingFileIsNotAnError(t *testing.T) {
	entries, err := ReadAll(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil, got %v", entries)
	}
}

func TestReadAllSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "{\"runId\":\"r1\",\"kind\":\"ok1\"}\nnot json at all\n\n{\"runId\":\"r1\",\"kind\":\"ok2\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadAll(dir)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 || entries[0].Kind != "ok1" || entries[1].Kind != "ok2" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestDetailRoundTrips(t *testing.T) {
	dir := t.TempDir()
	detail, _ := json.Marshal(map[string]any{"producedPaths": []string{"spec.md"}})
	if err := Append(dir, Entry{RunID: "r1", NodeID: "write-spec", Kind: "WorkCompleted", Detail: detail}); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(entries[0].Detail, &got); err != nil {
		t.Fatalf("Detail did not round-trip: %v", err)
	}
	if got["producedPaths"].([]any)[0] != "spec.md" {
		t.Errorf("got %v", got)
	}
}

// TestAppendConcurrent runs N goroutines appending simultaneously (run under
// -race) and requires every resulting line to be valid, complete JSON: no
// interleaved or torn lines from concurrent writers.
func TestAppendConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Append(dir, Entry{RunID: "r1", NodeID: "a", Kind: "WorkCompleted"}); err != nil {
				t.Errorf("Append: %v", err)
			}
		}(i)
	}
	wg.Wait()

	f, err := os.Open(Path(dir))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	var count int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		count++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != n {
		t.Fatalf("wrote %d lines, want %d", count, n)
	}
}
