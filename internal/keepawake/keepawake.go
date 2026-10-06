// Package keepawake stops the host from idle-sleeping while long work runs.
//
// On macOS it holds a caffeinate assertion tied to this process's PID, so the
// assertion ends even if jevkit is killed. Elsewhere it is a no-op. Keeping the
// machine awake is best effort and never fails the caller.
package keepawake

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
)

// DisableEnv turns keep-awake off when set to a non-empty value.
const DisableEnv = "JEVKIT_NO_CAFFEINATE"

// assertion is a running keep-awake holder.
type assertion interface{ Stop() }

var (
	mu      sync.Mutex
	holders int
	active  assertion

	goos  = runtime.GOOS
	start = startCaffeinate
)

// Hold keeps the machine awake until every returned release func has been
// called. Nested holds share one assertion. Release is safe to call twice.
func Hold() (release func()) {
	mu.Lock()
	defer mu.Unlock()
	holders++
	if holders == 1 && goos == "darwin" && os.Getenv(DisableEnv) == "" {
		active = start()
	}
	var once sync.Once
	return func() { once.Do(releaseOne) }
}

func releaseOne() {
	mu.Lock()
	defer mu.Unlock()
	holders--
	if holders == 0 && active != nil {
		active.Stop()
		active = nil
	}
}

type caffeinate struct{ cmd *exec.Cmd }

func (c caffeinate) Stop() {
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
}

// startCaffeinate prevents idle sleep (-i) and system sleep on AC power (-s)
// while this process lives (-w). The display may still sleep.
func startCaffeinate() assertion {
	path, err := exec.LookPath("caffeinate")
	if err != nil {
		return nil
	}
	cmd := exec.Command(path, "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil
	}
	return caffeinate{cmd: cmd}
}
