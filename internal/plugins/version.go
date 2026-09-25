package plugins

// Version is the plugin package version. It must match the jevkit binary
// version constant (cmd/jevkit main.version default; overridden via
// -ldflags="-X main.version=..." at release). Keep these in sync.
const Version = "dev"

// PluginID is the marketplace / host plugin identifier.
const PluginID = "jevkit"

// CommandName is the CLI binary plugins expect on PATH.
const CommandName = "jevkit"

// ModulePath is the Go module path used in install remediation hints.
const ModulePath = "github.com/OWNER/jevkit"

// Hosts are the supported agent runtimes that receive a generated package.
var Hosts = []string{"claude", "opencode", "cursor", "antigravity"}

// InstallCommand returns the remediation users should run when jevkit is
// missing from PATH. Pinned when Version is a release tag; otherwise @latest.
func InstallCommand() string {
	return installCommandFor(Version)
}

func goInstallRef(version string) string {
	if version == "" || version == "dev" {
		return "latest"
	}
	if version[0] == 'v' {
		return version
	}
	return "v" + version
}

func installCommandFor(version string) string {
	return "go install " + ModulePath + "/cmd/jevkit@" + goInstallRef(version)
}
