// Package plugins generates distributable host plugin packages for Claude Code
// and OpenCode from templates under plugins/templates/.
//
// Each package registers hooks/MCP that call the jevkit binary, and ships a
// bootstrap script that checks PATH (and can print or apply a pinned install).
// Run `make plugins` to regenerate plugins/jevkit/<host>/.
package plugins
