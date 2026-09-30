package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/usage"
)

func TestJevUsageRecordsHookTransportRetries(t *testing.T) {
	a := testkit.NewApp(t)
	cfg := jev.Config{Transport: jev.TransportFixture, FixtureDir: filepath.Join("..", "..", "..", "testdata", "jev"), MaxRetries: 1}
	client := jev.New(cfg, nil)
	client.Sleep = func(context.Context, time.Duration) {}
	asker := app.UsageOrigin(a.RecordJev(client, cfg), "hook", "claude")
	_, err := asker.Ask(context.Background(), jev.Request{QuestionSetID: "retry-429-then-success", State: "s", Questions: map[string]jev.Question{"q": jev.NoulQuestion{Instructions: "?"}}})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := usage.ReadRecords(usage.Path(a.StateHome()))
	if err != nil || len(recs) != 2 {
		t.Fatalf("records=%+v err=%v", recs, err)
	}
	if recs[0].Status != "http-429" || recs[1].Status != "ok" || recs[0].Origin != "hook" || recs[1].Agent != "claude" {
		t.Fatalf("records=%+v", recs)
	}
	sum := usage.Aggregate(recs, usage.Filter{IncludeFixture: true}, nil)
	if sum.Calls != 1 || sum.Attempts != 2 || sum.FailedAttempts != 1 || sum.InputTokens != 140 || sum.ByOrigin["hook"].Attempts != 2 {
		t.Fatalf("summary=%+v", sum)
	}
}
