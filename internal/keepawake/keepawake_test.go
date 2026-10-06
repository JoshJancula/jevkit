package keepawake

import (
	"os/exec"
	"runtime"
	"testing"
)

type fakeAssertion struct{ stops *int }

func (f fakeAssertion) Stop() { *f.stops++ }

func stub(t *testing.T, os string) (starts, stops *int) {
	t.Helper()
	starts, stops = new(int), new(int)
	oldGOOS, oldStart := goos, start
	goos = os
	start = func() assertion { *starts++; return fakeAssertion{stops: stops} }
	t.Cleanup(func() { goos, start = oldGOOS, oldStart })
	return starts, stops
}

func TestNestedHoldsShareOneAssertion(t *testing.T) {
	starts, stops := stub(t, "darwin")
	outer := Hold()
	inner := Hold()
	if *starts != 1 {
		t.Fatalf("starts = %d, want 1", *starts)
	}
	inner()
	inner()
	if *stops != 0 {
		t.Fatalf("stopped before last release")
	}
	outer()
	if *stops != 1 {
		t.Fatalf("stops = %d, want 1", *stops)
	}
	Hold()()
	if *starts != 2 || *stops != 2 {
		t.Fatalf("second hold: starts=%d stops=%d, want 2/2", *starts, *stops)
	}
}

func TestDisabledByEnv(t *testing.T) {
	starts, _ := stub(t, "darwin")
	t.Setenv(DisableEnv, "1")
	Hold()()
	if *starts != 0 {
		t.Fatalf("started despite %s", DisableEnv)
	}
}

func TestNoOpOffDarwin(t *testing.T) {
	starts, _ := stub(t, "linux")
	Hold()()
	if *starts != 0 {
		t.Fatalf("started on linux")
	}
}

func TestCaffeinateRunsAndStops(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("caffeinate is macOS only")
	}
	if _, err := exec.LookPath("caffeinate"); err != nil {
		t.Skip("caffeinate not on PATH")
	}
	a := startCaffeinate()
	if a == nil {
		t.Fatal("caffeinate did not start")
	}
	c := a.(caffeinate)
	a.Stop()
	if c.cmd.ProcessState == nil {
		t.Fatal("caffeinate still running after Stop")
	}
}
