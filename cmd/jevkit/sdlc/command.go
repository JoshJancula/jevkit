package sdlc

import (
	"sync"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

// App carries the shared CLI state and helpers for sdlc commands, plus the
// per-invocation choices and dashboard state the sdlc flows share.
type App struct {
	*app.App
	sdlcDelegateChoice *bool
	sdlcSessionChoice  string
	sdlcRuntimeChoice  *ledger.RuntimeIntegration
	sdlcAutoChoice     bool
	sdlcProgress       *sdlcProgress
	sdlcAllowRead      []string
	sdlcInputBytes     chan byte
	sdlcPendingByte    byte
	sdlcHasPending     bool
	outputMu           *sync.Mutex
}

// Outf serializes output while concurrent reviews share the same CLI writer.
// A pointer keeps cloned SDLC apps using the same output lock.
func (a *App) Outf(format string, args ...any) {
	if a.outputMu != nil {
		a.outputMu.Lock()
		defer a.outputMu.Unlock()
	}
	a.App.Outf(format, args...)
}

// Command returns the sdlc command.
func Command(a *app.App) *cobra.Command {
	return (&App{App: a}).sdlcCmd()
}

// clone copies the sdlc state and the shared App, so changing the copy's
// output or options never affects the original.
func (a *App) clone() *App {
	shared := *a.App
	c := *a
	c.App = &shared
	return &c
}
