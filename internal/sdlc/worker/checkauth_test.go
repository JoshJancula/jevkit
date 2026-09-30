package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/security"
)

// Planner-proposed commands must never run without an exact run-scoped
// authorization digest. --auto alone is not permission.
func TestProposedCheckExecutionRequiresAuthorization(t *testing.T) {
	dir := t.TempDir()
	_, err := security.RunArgv(context.Background(), security.ArgvRequest{
		Argv:             []string{"true"},
		Workspace:        dir,
		ChecksDigest:     "planned-checks",
		AuthorizedDigest: "",
		Timeout:          time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("untrusted plan executed: %v", err)
	}
	_, err = security.RunArgv(context.Background(), security.ArgvRequest{
		Argv:             []string{"true"},
		Workspace:        dir,
		ChecksDigest:     "planned-checks",
		AuthorizedDigest: "other-digest",
		Timeout:          time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("changed digest executed: %v", err)
	}
	result, err := security.RunArgv(context.Background(), security.ArgvRequest{
		Argv:             []string{"true"},
		Workspace:        dir,
		ChecksDigest:     "planned-checks",
		AuthorizedDigest: "planned-checks",
		Timeout:          time.Second,
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("authorized argv: %+v %v", result, err)
	}
}
