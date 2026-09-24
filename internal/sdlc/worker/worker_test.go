package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

func TestCommandUsesEnrolledRuntimeAndReadOnlyMode(t *testing.T) {
	req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: "codex", Model: "selected-model"}, Assignment: adaptive.Assignment{Role: "assessor"}}
	bin, args, err := command(req)
	if err != nil || bin != "codex" || strings.Join(args, " ") != "exec --model selected-model --sandbox read-only" {
		t.Fatalf("codex command: %s %v %v", bin, args, err)
	}
	req.Agent.Runtime = "cursor"
	bin, args, err = command(req)
	if err != nil || bin != "cursor-agent" || !strings.Contains(strings.Join(args, " "), "--mode ask") {
		t.Fatalf("cursor command: %s %v %v", bin, args, err)
	}
	req.Agent.Runtime = "opencode"
	req.Assignment.ReadOnly = true
	if _, _, err := command(req); err == nil {
		t.Fatal("OpenCode read-only assignment was accepted")
	}
}

func TestWorkspaceDiffIncludesUntrackedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, err := workspaceDiff(context.Background(), dir)
	if err != nil || !strings.Contains(string(patch), "+new content") {
		t.Fatalf("patch: %q %v", patch, err)
	}
}

func TestParseReplyRequiresStructuredArtifact(t *testing.T) {
	r, err := ParseReply([]byte("```json\n{\"outcome\":\"planned\",\"content\":\"Plan.\"}\n```"))
	if err != nil || r.Outcome != "planned" || r.Content != "Plan." {
		t.Fatalf("reply: %+v %v", r, err)
	}
	if _, err := ParseReply([]byte(`{"outcome":"changed"}`)); err == nil {
		t.Fatal("empty artifact accepted")
	}
	if _, err := ParseReply([]byte("changed")); err == nil {
		t.Fatal("unstructured reply accepted")
	}
}
