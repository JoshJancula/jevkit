package plugins_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/plugins"
)

func TestVersionMatchesBinaryConstant(t *testing.T) {
	// cmd/jevkit main.version defaults to "dev"; keep plugins.Version aligned.
	const binaryVersionDefault = "dev"
	if plugins.Version != binaryVersionDefault {
		t.Fatalf("plugins.Version=%q want %q (must match cmd/jevkit main.version)", plugins.Version, binaryVersionDefault)
	}
}

func TestGenerateAndValidate(t *testing.T) {
	root, err := plugins.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	results, err := plugins.Generate(plugins.Options{RepoRoot: root, OutRoot: out})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(plugins.Hosts) {
		t.Fatalf("got %d hosts, want %d", len(results), len(plugins.Hosts))
	}
	if err := plugins.ValidateAll(root, out, plugins.Version); err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		boot := filepath.Join(r.Dir, "shared", "jevkit-plugin-bootstrap.sh")
		raw, err := os.ReadFile(boot)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), plugins.InstallCommand()) {
			t.Fatalf("%s bootstrap missing install command %q", r.Host, plugins.InstallCommand())
		}
		run := filepath.Join(r.Dir, "shared", "jevkit-run.sh")
		if _, err := os.Stat(run); err != nil {
			t.Fatalf("%s missing jevkit-run.sh: %v", r.Host, err)
		}
	}
}

func TestCommittedPackagesMatchGenerator(t *testing.T) {
	root, err := plugins.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	committed := filepath.Join(root, "plugins", "jevkit")
	if _, err := os.Stat(committed); os.IsNotExist(err) {
		t.Skip("plugins/jevkit not generated yet; run make plugins")
	}
	if err := plugins.ValidateAll(root, committed, plugins.Version); err != nil {
		t.Fatalf("committed packages invalid (run make plugins): %v", err)
	}

	tmp := t.TempDir()
	if _, err := plugins.Generate(plugins.Options{RepoRoot: root, OutRoot: tmp}); err != nil {
		t.Fatal(err)
	}
	for _, host := range plugins.Hosts {
		wantDir := filepath.Join(tmp, host)
		gotDir := filepath.Join(committed, host)
		if err := filepath.WalkDir(wantDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(wantDir, path)
			if err != nil {
				return err
			}
			gotPath := filepath.Join(gotDir, rel)
			wantBytes, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			gotBytes, err := os.ReadFile(gotPath)
			if err != nil {
				return err
			}
			wantNorm := strings.ReplaceAll(string(wantBytes), "\r\n", "\n")
			gotNorm := strings.ReplaceAll(string(gotBytes), "\r\n", "\n")
			if wantNorm != gotNorm {
				return &mismatchError{host: host, rel: rel}
			}
			return nil
		}); err != nil {
			t.Fatalf("drift in committed package: %v (run make plugins)", err)
		}
	}
}

type mismatchError struct {
	host, rel string
}

func (e *mismatchError) Error() string {
	return e.host + ": " + e.rel + " differs from regenerated output"
}

func TestInstallCommandReferencesJevkit(t *testing.T) {
	cmd := plugins.InstallCommand()
	if !strings.Contains(cmd, "jevkit") {
		t.Fatalf("install command %q must reference jevkit", cmd)
	}
	if strings.Contains(strings.ToLower(cmd), "api_key") {
		t.Fatal("install command must not contain secrets")
	}
}
