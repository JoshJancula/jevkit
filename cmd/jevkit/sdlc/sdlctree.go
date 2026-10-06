package sdlc

import (
	"fmt"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

func (a *App) rootRun(run ledger.Run) (ledger.Run, error) {
	for run.ParentRunID != "" {
		var err error
		run, err = ledger.Open(a.SDLCRunsDir(), run.ParentRunID).ReadRun()
		if err != nil {
			return ledger.Run{}, err
		}
	}
	return run, nil
}

func (a *App) treeRemaining(run ledger.Run, p enrollment.Policy) (time.Duration, error) {
	b, err := a.budgetView(run, p)
	return time.Duration((b.Limits.Seconds - b.Usage.Seconds) * float64(time.Second)), err
}

func (a *App) chargeTree(run *ledger.Run, p enrollment.Policy, kind string) error {
	id := fmt.Sprintf("%s/%s/%s", run.RunID, kind, run.AutoTarget)
	if run.StageFlow != nil {
		id = fmt.Sprintf("%s/%s/%d", run.RunID, kind, run.StageFlow.Steps)
	}
	b, err := a.updateBudget(*run, p, func(b *ledger.Budget) error { return b.Reserve(id, run.RunID, kind) })
	if err == nil {
		syncTreeUsage(run, b)
	}
	return err
}
