package compact

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		cmd, want string
	}{
		{"git diff", FamilyGitDiff},
		{"git -C /tmp diff", FamilyGitDiff},
		{"git --git-dir=.git diff HEAD", FamilyGitDiff},
		{"git show HEAD", FamilyGitShow},
		{"git log --oneline", FamilyGitLog},
		{"rg MATCHME", FamilyGrep},
		{"grep -rn proxy .", FamilyGrep},
		{"egrep foo", FamilyGrep},
		{"find . -type f", FamilyFind},
		{"ls -la", FamilyLS},
		{"tree -L 3", FamilyTree},
		{"/usr/bin/git diff", FamilyGitDiff},
		{"FOO=1 git diff", FamilyGitDiff},
		{"env FOO=1 git diff", FamilyGitDiff},
		{"git status", ""},
		{"echo hi", ""},
		{"git status && git diff", ""},
		{"cd /tmp && git diff", ""},
		{"git diff | head", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Classify(tc.cmd); got != tc.want {
			t.Errorf("Classify(%q)=%q want %q", tc.cmd, got, tc.want)
		}
	}
}

func TestIsSourceFamily(t *testing.T) {
	for _, id := range []string{FamilyGitDiff, FamilyGitShow, FamilyGitLog, FamilyGrep, FamilyFind, FamilyLS, FamilyTree} {
		if !IsSourceFamily(id) {
			t.Errorf("%q should be source", id)
		}
	}
	if IsSourceFamily(FamilyGenericLarge) || IsSourceFamily("git_status") {
		t.Fatal("unexpected source family")
	}
}

func TestCollapseRuns(t *testing.T) {
	t.Run("progress", func(t *testing.T) {
		lines := []string{"Downloading a", "Downloading b", "Downloading c", "Downloading d", "ok done"}
		out, omitted := CollapseRuns(lines)
		if omitted["progress"] != 4 {
			t.Fatalf("omitted=%v out=%v", omitted, out)
		}
		if len(out) != 2 || !strings.Contains(out[0], "progress/spinner") || out[1] != "ok done" {
			t.Fatalf("out=%v", out)
		}
	})
	t.Run("below min run", func(t *testing.T) {
		lines := []string{"Downloading a", "Downloading b", "ok"}
		out, omitted := CollapseRuns(lines)
		if len(omitted) != 0 || len(out) != 3 {
			t.Fatalf("omitted=%v out=%v", omitted, out)
		}
	})
	t.Run("stack frames keep first two", func(t *testing.T) {
		lines := []string{
			"Traceback (most recent call last):",
			`  File "a.py", line 1`,
			`  File "b.py", line 2`,
			`  File "c.py", line 3`,
			`  File "d.py", line 4`,
			"ValueError: boom",
		}
		out, omitted := CollapseRuns(lines)
		if omitted["stack_frame"] != 3 { // traceback + 4 frames = 5; keep 2 → drop 3
			t.Fatalf("omitted=%v out=%v", omitted, out)
		}
		if out[0] != lines[0] || out[1] != lines[1] {
			t.Fatalf("kept frames wrong: %v", out)
		}
		if !strings.Contains(out[2], "stack trace frame") {
			t.Fatalf("missing marker: %v", out)
		}
		if out[len(out)-1] != "ValueError: boom" {
			t.Fatalf("lost error: %v", out)
		}
	})
	t.Run("preserve blocks collapse", func(t *testing.T) {
		// SUMMARY_RE matches ^BUILD, so "Building ..." is preserved and never
		// collapses even though it also matches the progress class.
		lines := []string{"Building a", "Building b", "Building c", "Building d", "Building e"}
		out, omitted := CollapseRuns(lines)
		if len(omitted) != 0 || len(out) != 5 {
			t.Fatalf("omitted=%v out=%v", omitted, out)
		}
	})
}

func TestAllowsGenericFallback(t *testing.T) {
	if !allowsGenericFallback("") {
		t.Fatal("empty should allow")
	}
	if !allowsGenericFallback("custom-build-tool --verbose") {
		t.Fatal("unknown should allow")
	}
	if allowsGenericFallback("cat file") {
		t.Fatal("cat must deny")
	}
	if allowsGenericFallback("git diff") {
		t.Fatal("git diff must deny")
	}
	if allowsGenericFallback("git status && make") {
		t.Fatal("&& must deny")
	}
	if !allowsGenericFallback("make | tee log") {
		t.Fatal("pure pipeline of unknowns should allow")
	}
}
