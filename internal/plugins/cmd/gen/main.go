// Command gen regenerates distributable host plugin packages under plugins/jevkit/.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/OWNER/jevkit/internal/plugins"
)

func main() {
	root, err := plugins.FindRepoRoot(".")
	if err != nil {
		fatal(err)
	}
	results, err := plugins.Generate(plugins.Options{RepoRoot: root})
	if err != nil {
		fatal(err)
	}
	outRoot := filepath.Join(root, "plugins", "jevkit")
	if err := plugins.ValidateAll(root, outRoot, plugins.Version); err != nil {
		fatal(err)
	}
	fmt.Printf("generated %d host plugin packages under plugins/jevkit (version %s)\n", len(results), plugins.Version)
	for _, r := range results {
		fmt.Printf("  %s (%d files)\n", r.Host, len(r.Files))
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "plugins gen: %v\n", err)
	os.Exit(1)
}
