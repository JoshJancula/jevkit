package compact

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Port of ralph tests/bats/compactors.bats source-passthrough cases and the
// python characterization table for SOURCE_OUTPUT_FAMILIES.
func TestSourceFamiliesPassthrough(t *testing.T) {
	cases := []struct {
		name, family string
	}{
		{"git-diff-hunks", FamilyGitDiff},
		{"git-diff-stat-only-large", FamilyGitDiff},
		{"jev-source-git-diff", FamilyGitDiff},
		{"grep-matches", FamilyGrep},
		{"grep-already-summarized-large", FamilyGrep},
		{"find-many", FamilyFind},
		{"no-match-find-few", FamilyFind},
		{"ls-listing", FamilyLS},
		{"tree-listing", FamilyTree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertPreserved(t, tc.name, tc.family)
		})
	}
}

func TestSourceFamilySynthetic(t *testing.T) {
	// Mirrors ralph tests/python/test_compaction_family_defaults.py builders
	// for families without pinned bats fixtures (git show / git log / rg).
	cases := []struct {
		name, cmd, family string
		stdout            func() string
	}{
		{"git_show", "git show HEAD", FamilyGitShow, func() string {
			lines := []string{
				"commit abcdef0123456789",
				"Author: Test <test@example.com>",
				"Date:   Thu Sep 10 12:00:00 2026 -0400",
				"",
				"    test commit",
				"",
				"diff --git a/src/a.py b/src/a.py",
				"--- a/src/a.py",
				"+++ b/src/a.py",
			}
			for i := 0; i < 40; i++ {
				lines = append(lines, "+line "+strconv.Itoa(i))
			}
			return strings.Join(lines, "\n")
		}},
		{"git_log", "git log", FamilyGitLog, func() string {
			var b strings.Builder
			for i := 0; i < 30; i++ {
				b.WriteString("commit ")
				b.WriteString(strings.Repeat("0", 39))
				b.WriteString(strconv.Itoa(i))
				b.WriteString("\nAuthor: Test <test@example.com>\nDate: Thu Sep 10\n\n    msg ")
				b.WriteString(strconv.Itoa(i))
				b.WriteByte('\n')
			}
			return b.String()
		}},
		{"rg", "rg proxy .", FamilyGrep, func() string {
			var lines []string
			for i := 0; i < 40; i++ {
				lines = append(lines, "./src/file"+strconv.Itoa(i)+".py:10:proxy match "+strconv.Itoa(i))
			}
			return strings.Join(lines, "\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := tc.stdout()
			// Inflate past the default threshold so passthrough is meaningful.
			for len(stdout) < DefaultThresholdBytes+100 {
				stdout += "\n" + stdout
			}
			got := Compact(tc.cmd, stdout, "", 0, Options{})
			if got.Compacted || got.Status != StatusNotCompacted {
				t.Fatalf("compacted=%v status=%q", got.Compacted, got.Status)
			}
			if got.Family != tc.family {
				t.Fatalf("family=%q want %q", got.Family, tc.family)
			}
			if got.Stdout != stdout {
				t.Fatal("stdout mutated")
			}
		})
	}
}

func TestBinaryNullBytePassthrough(t *testing.T) {
	stdout := "vis\x00ible"
	got := Compact("cat some-binary-file", stdout, "", 0, Options{ThresholdBytes: 1})
	if got.Compacted || got.Stdout != stdout {
		t.Fatalf("binary mutated: compacted=%v out=%q", got.Compacted, got.Stdout)
	}
	// Pinned bats fixture is small text (historically named binary-null-byte).
	assertPreserved(t, "binary-null-byte", "")
}

func TestNoMatchPassthrough(t *testing.T) {
	assertPreserved(t, "no-match-unknown", "")
	// Compound commands are never classified; tiny output stays raw.
	assertPreserved(t, "no-match-compound", "")
	// Known-but-unsupported command with diff-shaped stdout: shape detection
	// only runs when the command string is empty, so this stays unlabelled.
	assertPreserved(t, "shape-unknown-git-diff", "")
}

func TestEmptyCommandShapePassthrough(t *testing.T) {
	fx := loadFixture(t, "git-diff-hunks")
	got := Compact("", fx.Stdout, "", 0, Options{ThresholdBytes: 1})
	if got.Family != FamilyGitDiff || got.Compacted {
		t.Fatalf("family=%q compacted=%v", got.Family, got.Compacted)
	}
	if got.Stdout != fx.Stdout {
		t.Fatal("stdout mutated")
	}

	// Ambiguous shapes must not invent a family (ralph detect_output_shape).
	ambiguous := strings.Join([]string{
		"diff --git a/f b/f",
		"./a/b:1:match",
		"./a/c:2:match",
		"./a/d:3:match",
	}, "\n")
	got = Compact("", ambiguous, "", 0, Options{ThresholdBytes: 1})
	if got.Family != "" {
		t.Fatalf("ambiguous shape got family %q", got.Family)
	}
}

func TestGenericLargeWindowedGolden(t *testing.T) {
	// Port of ralph tests/bats/compactor-generic-fallback.bats above-threshold
	// cases; expected_stdout.txt is pinned from ralph's python formatter.
	fx := loadFixture(t, "generic-large-windowed")
	want, err := os.ReadFile(filepath.Join("testdata", "compactors", "generic-large-windowed", "expected_stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := Compact(fx.Command, fx.Stdout, fx.Stderr, fx.Exit, Options{ThresholdBytes: 200})
	if !got.Compacted || got.Status != StatusCompacted || got.Family != FamilyGenericLarge {
		t.Fatalf("family=%q status=%q compacted=%v", got.Family, got.Status, got.Compacted)
	}
	if got.Stdout != string(want) {
		t.Fatalf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", got.Stdout, want)
	}
	if got.Stderr != "" {
		t.Fatalf("stderr=%q", got.Stderr)
	}
	if !strings.Contains(got.Stdout, "ERROR: module xyz failed to compile") {
		t.Fatal("missing extracted error line")
	}
	if !strings.Contains(got.Stdout, "FINAL SUMMARY: build complete with warnings") {
		t.Fatal("missing tail summary")
	}
	if !strings.Contains(got.Stdout, "line(s) omitted") {
		t.Fatal("missing omission marker")
	}
}

func TestGenericLargeBelowThreshold(t *testing.T) {
	got := Compact("custom-build-tool --verbose", "small output for unknown command", "", 0, Options{})
	if got.Compacted || got.Family != "" || got.Stdout != "small output for unknown command" {
		t.Fatalf("%+v", got)
	}
}

func TestSourceBinaryDeniesGenericFallback(t *testing.T) {
	// cat is not a source *family* but still denies size truncation.
	big := strings.Repeat("file-body-line:"+strings.Repeat("x", 64)+"\n", 200)
	got := Compact("cat large-file.txt", big, "", 0, Options{ThresholdBytes: 200})
	if got.Compacted || got.Stdout != big {
		t.Fatalf("cat output truncated: compacted=%v", got.Compacted)
	}
}

func TestCompoundSourceDeniesGenericFallback(t *testing.T) {
	big := strings.Repeat("detail chunk padding "+strings.Repeat("x", 60)+"\n", 100)
	got := Compact("cd /tmp && custom-build-tool --verbose", big, "", 0, Options{ThresholdBytes: 200})
	if got.Compacted {
		t.Fatalf("&& chain must not use generic_large: compacted=%v status=%q family=%q", got.Compacted, got.Status, got.Family)
	}
}
