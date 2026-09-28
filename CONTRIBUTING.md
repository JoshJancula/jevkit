# Contributing to Jevkit

Use a branch and open a pull request against `main`. Keep changes focused and update user-facing docs when behavior changes. The [build guide](docs/BUILD.md) has toolchain details.

```bash
git fetch origin
git switch -c my-change origin/main
make lint test
```

PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/), which CI checks. For example, `feat: add a command` or `docs: explain redaction`. Describe what changed, why, and any compatibility effect in the PR. Before requesting review, run `make lint test` and check generated files with `make plugins` if you changed plugin templates.

## Releases

Release code and documentation go through a PR like any other change. After the PR is merged and CI passes on `main`, a maintainer chooses the version and tags that commit:

```bash
git switch main
git pull --ff-only origin main
git tag vX.Y.Z
git push origin vX.Y.Z
```

The tag starts the [release workflow](.github/workflows/release.yml), which builds six macOS, Linux, and Windows archives, produces SBOMs, signs the checksum file, and publishes a GitHub Release. It also runs the macOS/Linux installer as a smoke test.

Review the version and commit before pushing the tag: the GitHub Release becomes public when the workflow runs. Afterward, check the six archives, `checksums.txt`, `checksums.txt.sigstore.json`, and the installer step in the workflow log. The workflow uses `GITHUB_TOKEN` and GitHub OIDC; it needs no custom publishing secret.
