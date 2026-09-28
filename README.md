# Jevkit

Jevkit is a local Go CLI that connects coding agents to TypeSafe AI's [Jev](https://typesafe.ai/) service. Agents can use it to classify requests, rank useful lines in noisy output, and ask structured questions. Jevkit redacts configured patterns on your machine before sending text to Jev.

## Install

On macOS or Linux, the installer downloads the latest [GitHub Release](https://github.com/JoshJancula/jevkit/releases), checks its checksum, and places `jevkit` in `~/.local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/JoshJancula/jevkit/main/install.sh | sh
```

On Windows, download the matching release ZIP or run [install.ps1](install.ps1) in PowerShell. If there is no release yet, build from source with Go:

```bash
make install
jevkit version
```

`make install` also uses `~/.local/bin`. Add that directory to your `PATH` if your shell does not already use it. See [build and development](docs/BUILD.md) for prerequisites and other build commands.

## Get started

From a project directory:

```bash
jevkit key set             # securely store your TypeSafe API key
jevkit key test            # check that it works
jevkit install codex       # or claude, opencode, cursor, antigravity, all
jevkit doctor              # check the local setup
```

`jevkit install` adds hooks and an MCP server for the selected agent. Compaction is optional and starts off; set `JEVKIT_COMPACT=1` when you want Jevkit to shorten supported tool output. The original output stays available locally. See the [agent guide](docs/AGENT-INTEGRATIONS.md) for the supported behavior and settings.

Redaction is pattern based, so check it against your own data before using Jevkit with sensitive output: `jevkit redact test --diff <file>`. Read the [redaction guide](docs/REDACTION.md) for its limits.

## More to explore

- [Ask Jev directly](docs/ASK.md) from the terminal.
- [Run an agent workflow](docs/SDLC.md) with planning and review.
- [Browse all guides and references](docs/README.md).
- [Contribute](CONTRIBUTING.md) or read the [MIT License](LICENSE).
