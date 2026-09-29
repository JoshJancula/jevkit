package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
)

func TestDefaultStateDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := defaultStateDir(env(map[string]string{"JEVKIT_STATE_DIR": "/s"}), "linux"); got != "/s" {
		t.Errorf("override: %q", got)
	}
	if got := defaultStateDir(env(map[string]string{"XDG_STATE_HOME": "/x"}), "linux"); got != filepath.Join("/x", "jevkit") {
		t.Errorf("xdg: %q", got)
	}
	if got := defaultStateDir(env(map[string]string{"LOCALAPPDATA": "/l"}), "windows"); got != filepath.Join("/l", "jevkit") {
		t.Errorf("windows: %q", got)
	}
}

// TestStatePathsAgreeAcrossOverrideForms confirms every jevkit-owned path
// resolves under the same root regardless of whether StateDir came from the
// default (already ends in "jevkit") or an explicit JEVKIT_STATE_DIR that
// doesn't. auditPath/reviewPath used to bypass stateHome() and read
// a.StateDir directly, which silently split state across two directories
// whenever JEVKIT_STATE_DIR omitted the "jevkit" suffix.
func TestStatePathsAgreeAcrossOverrideForms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stateDir string
		want     string // the resolved <root>/jevkit directory
	}{
		{"default-shape (ends in jevkit)", filepath.Join("/home/u", ".local", "state", "jevkit"), filepath.Join("/home/u", ".local", "state", "jevkit")},
		{"override without jevkit suffix", "/custom/state", filepath.Join("/custom/state", "jevkit")},
		{"override already ending in jevkit", "/custom/state/jevkit", filepath.Join("/custom/state", "jevkit")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app.App{StateDir: tc.stateDir}
			jevkitDir := filepath.Join(a.StateHome(), "jevkit")
			if jevkitDir != tc.want {
				t.Fatalf("stateHome()+jevkit = %q, want %q", jevkitDir, tc.want)
			}
			if got := a.AuditPath(); filepath.Dir(got) != tc.want {
				t.Errorf("auditPath() dir = %q, want %q", filepath.Dir(got), tc.want)
			}
			if got := a.ReviewPath(); filepath.Dir(got) != tc.want {
				t.Errorf("reviewPath() dir = %q, want %q", filepath.Dir(got), tc.want)
			}
			if got := a.SDLCRunsDir(); !strings.HasPrefix(got, tc.want+string(filepath.Separator)) {
				t.Errorf("sdlcRunsDir() = %q, want prefix %q", got, tc.want)
			}
		})
	}
}
