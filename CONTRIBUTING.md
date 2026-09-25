# Contributing to jevkit

## Before you start

See [Build and development](docs/BUILD.md) for the Go toolchain, Make targets,
Docker, and the dev container. Run the checks before opening or updating a
pull request:

```bash
make lint test
```

## Branching

Start from the latest `main` and use a branch for each focused change:

```bash
git fetch origin
git switch main
git pull --ff-only origin main
git switch -c <short-topic>
```

Prefer small pull requests that do one thing and are easy to review.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/):

```text
<type>[optional scope][!]: <short imperative description>
```

The types that contribute release notes and versioning are `feat`, `fix`,
`perf`, `refactor`, `docs`, and `security`. Maintenance types are still useful,
but `chore`, `ci`, `test`, `style`, and `build` are skipped in the generated
release notes. Examples:

```text
feat(compact): preserve the wrapped command exit status
fix: avoid sending unredacted tool output
docs: explain the contributor build environment
chore: refresh generated plugin fixtures
```

For a breaking change, add `!` to the type or scope, such as `feat!:` or
`feat(api)!:`. You may also add a `BREAKING CHANGE:` footer. Keep the subject
concise and add a one- or two-sentence body explaining why when the change is
not self-evident.

## Rebasing

Before pushing or updating a pull request, bring your branch up to date with
`main`:

```bash
git fetch origin
git rebase origin/main
make lint test
git push
```

Resolve conflicts, stage the resolutions, and run `git rebase --continue`.
Merging `origin/main` is also acceptable when a rebase is impractical. Do not
rewrite shared branches. If you need to update history on your own pull
request branch, use `git push --force-with-lease`, never plain `--force`.

## Pull requests

Open the pull request against `main`. The pull request title must itself use
the Conventional Commits format; CI enforces this on titles, not on every
individual commit. In the description, explain what changed and why, link any
related issue, and call out user-facing or compatibility effects.

Before requesting review, check:

- [ ] `make lint test` passes (or the equivalent Docker Compose command).
- [ ] Documentation is updated for user-facing or contributor-facing changes.
- [ ] The pull request title follows Conventional Commits.
- [ ] Generated files and unrelated changes are excluded.

## Release commits

When the release workflow is enabled in a future change, maintainers should
use the `jevkit-release` GitHub App for the generated changelog commit:

```text
chore(release): vX.Y.Z [skip ci]
```

Its installation token will need permission to bypass required checks on
`main`; that bypass must not be granted to personal tokens or general branch
writes.
