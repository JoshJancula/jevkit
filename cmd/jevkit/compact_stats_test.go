package main

import (
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/registry"
)

func TestCompactStatsCountsOneOutputPerTriage(t *testing.T) {
	a := newApp(t)
	chosen := "scattered"
	for _, id := range []string{"compaction.triage.v2", "compaction.salient-lines.v2"} {
		if err := registry.AppendDecision(a.StateDir, registry.Decision{
			QuestionSetID: id, Surface: "compaction", Chosen: &chosen,
			Decision: registry.Act, Confidence: 0.95,
			CommandFamily: "generic_large", BytesBefore: 1000, BytesAfter: 200,
		}); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := mustRun(t, a, "", 0, "compact", "stats")
	if !strings.Contains(out, "decisions: 1") || !strings.Contains(out, "bytes saved: 800") {
		t.Fatalf("second selection was counted as another output: %s", out)
	}
}
