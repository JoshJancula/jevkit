package main

import (
	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/mcp"
	"github.com/JoshJancula/jevkit/cmd/jevkit/redact"
	"github.com/JoshJancula/jevkit/cmd/jevkit/sdlc"
	"github.com/JoshJancula/jevkit/cmd/jevkit/security"
	"github.com/JoshJancula/jevkit/cmd/jevkit/usage"
	"github.com/JoshJancula/jevkit/cmd/jevkit/util"
)

// commands lists every top-level jevkit command.
func commands(a *app.App) []*cobra.Command {
	return append([]*cobra.Command{
		usage.Command(a),
		mcp.Command(a),
		redact.Command(a),
		security.Command(a),
		sdlc.Command(a),
	}, util.Commands(a)...)
}
