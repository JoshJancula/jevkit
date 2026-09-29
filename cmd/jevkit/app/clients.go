package app

import (
	"context"
	"path/filepath"

	"github.com/JoshJancula/jevkit/internal/compact"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/keystore"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
)

// Asker is the slice of the jev client `key test` uses.
type Asker interface {
	Ask(ctx context.Context, req jev.Request) (*jev.Response, error)
}

// store is the injected keystore, or one built from the app's directories.
func (a *App) Store() *keystore.Store {
	if a.Keystore != nil {
		return a.Keystore
	}
	return &keystore.Store{
		Getenv:    a.Getenv,
		Workspace: a.WorkDir,
		ConfigDir: a.ConfigDir,
		Keyring:   a.keyring(),
		Warn:      a.Stderr,
	}
}

func (a *App) keyring() keystore.Keyring {
	if a.Keyring != nil {
		return a.Keyring
	}
	return keystore.OSKeyring{}
}

func (a *App) JevClient(cfg jev.Config, key func() (string, error)) Asker {
	if a.NewJev != nil {
		return a.RecordJev(a.NewJev(cfg, key), cfg)
	}
	return a.RecordJev(jev.New(cfg, key), cfg)
}

func (a *App) CompactionClient(ctx context.Context) (compact.Asker, *compact.Policy) {
	key, _, err := a.Store().Resolve(ctx)
	if err != nil {
		return nil, nil
	}
	cfg, err := a.JevConfig()
	if err != nil {
		return nil, nil
	}
	client := a.JevClient(cfg, func() (string, error) { return key, nil })
	policy, _ := compact.LoadPolicy(a.CompactionPolicyPath("", false), false)
	if project, err := compact.LoadPolicy(a.CompactionPolicyPath("", true), true); err == nil {
		if policy == nil {
			policy = project
		} else {
			policy.Rules = append(policy.Rules, project.Rules...)
		}
	}
	return client, policy
}

func (a *App) CompactionPolicyPath(path string, project bool) string {
	if path != "" {
		return path
	}
	if project {
		return filepath.Join(a.WorkDir, ".jevkit", "compaction.yaml")
	}
	return filepath.Join(a.ConfigDir, "compaction.yaml")
}

func (a *App) SecurityAsker(ctx context.Context) Asker {
	if a.Getenv("JEVKIT_TRANSPORT") == jev.TransportFixture {
		cfg, err := a.JevConfig()
		if err != nil {
			return nil
		}
		return a.JevClient(cfg, nil)
	}
	client, _ := a.CompactionClient(ctx)
	return client
}

func (a *App) SecurityLoadOptions() securityconfig.LoadOptions {
	environ := append([]string(nil), a.Environ...)
	if a.Yolo {
		environ = append(environ, "JEVKIT_YOLO=1")
	}
	return securityconfig.LoadOptions{ConfigDir: a.ConfigDir, StateDir: a.StateHome(), WorkDir: a.WorkDir, Name: a.SecurityPolicy, Environ: environ}
}
