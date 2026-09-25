package main

import (
	"fmt"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

func sdlcLimitSummary(p enrollment.Policy) string {
	return fmt.Sprintf("%d revisions, %d assignments, %s per agent, %s total",
		p.MaxRevisions, p.MaxAssignments,
		(time.Duration(p.MaxInvocationSeconds) * time.Second).String(),
		(time.Duration(p.MaxRunSeconds) * time.Second).String())
}
