# Contributing to Jevkit

Use a branch and open a pull request against `main`. Keep changes focused and update user-facing docs when behavior changes. The [build guide](docs/BUILD.md) has toolchain details.

```bash
git fetch origin
git switch -c my-change origin/main
make lint test
```

PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/), which CI checks. For example, `feat: add a command` or `docs: explain redaction`. Describe what changed, why, and any compatibility effect in the PR. Before requesting review, run `make lint test` and check generated files with `make plugins` if you changed plugin templates.

Squash merge feature PRs and use their conventional PR title as the squash commit message. Release automation reads commits on `main`; a conventional PR title alone does not fix nonconventional commits introduced by a regular merge. Write titles as useful release notes, describing the resulting behavior. Mark incompatible changes with `!` or a `BREAKING CHANGE:` footer.

## Releases

### First release: v0.1.0

The first entry in [CHANGELOG.md](CHANGELOG.md) uses conventional commits from the development history, grouped with the same configured categories as future releases. Each included commit keeps its message, scope, and commit link; hidden commit types and nonconventional messages are omitted. Keep this generated format for all releases. Set its date to the actual release date before merging this setup. Merge this setup with a `chore:` title so it does not itself request a new version.

The manifest is seeded at `0.1.0`, and `bootstrap-sha` excludes the existing development history from future generated notes. After this setup is merged and CI passes on `main`, establish the first release with a one-time tag:

```bash
git switch main
git pull --ff-only origin main
git tag -a v0.1.0 -m "Jevkit 0.1.0"
git push origin v0.1.0
```

Publish this first tag before merging further features or fixes. The tag starts the [release workflow](.github/workflows/release.yml), which builds six macOS, Linux, and Windows archives, produces SBOMs, signs the checksum file, and publishes a GitHub Release. It takes the notes for that exact version from `CHANGELOG.md`, then runs the macOS/Linux installer as a smoke test. Missing, duplicate, or empty entries fail the release rather than falling back to a commit dump.

### Subsequent releases

[Release Please](https://github.com/googleapis/release-please-action) maintains a release PR as conventional commits land on `main`. It updates `CHANGELOG.md` and `.github/release-manifest.json`; the version built into the CLI comes from the release tag. Review the proposed version and notes, and merge the release PR when ready to publish. There is no need to choose or push the tag yourself.

| Change | While below 1.0 | From 1.0 onward |
| --- | --- | --- |
| `fix:` | Patch (`0.1.0` → `0.1.1`) | Patch |
| `feat:` | Minor (`0.1.0` → `0.2.0`) | Minor |
| `feat!:` or `BREAKING CHANGE:` | Minor | Major |

Docs, tests, chores, and internal refactors are hidden from routine notes and do not ordinarily initiate a release. Use `fix(security):` for security fixes so they initiate a patch release. Breaking changes remain significant regardless of commit type. Promote to `1.0.0` deliberately when the public interface is ready: add a `Release-As: 1.0.0` footer to a conventional commit rather than permanently pinning `release-as` in the configuration.

Keep the generated changelog format consistent: improve future entries through clear conventional commit messages instead of rewriting or combining the generated bullets. Release Please may regenerate its PR when more commits land. Published entries are preserved as later entries are prepended. GoReleaser uses the committed changelog as the release-note source. The old git-cliff configuration has been retired so there is a single changelog writer.

The [auto-version workflow](.github/workflows/auto-version.yml) calls the build workflow directly after Release Please creates the tag and GitHub Release. This is necessary because tags created with `GITHUB_TOKEN` do not trigger another Actions run. The GitHub Release may be visible before its binaries finish uploading; verify the build completed before announcing it.

### GitHub setup and recovery

In **Settings → Actions → General → Workflow permissions**, enable **Allow GitHub Actions to create and approve pull requests**. Workflow files grant the needed job permissions; the default token permission can stay read-only. Automation uses `GITHUB_TOKEN` and GitHub OIDC and needs no custom publishing secret.

Release PRs created or updated with `GITHUB_TOKEN` do not automatically trigger PR workflows. After the bot's final update, a maintainer should close and reopen the release PR to start CI and the PR-title check. Wait for checks to pass before merging. Reopen it again if the bot updates it afterward. Keep the `autorelease: pending` label that Release Please uses to recognize a merged release PR.

If publishing fails after the tag exists, rerun the failed build job, or run **Release** from the Actions tab on `main` with that existing `release_tag`. Do not delete or move a published tag to retry. Manual tags remain supported for recovery, but must match a reviewed changelog entry and a commit on `main`; keep the manifest aligned if deliberately releasing outside Release Please.

Review the version and commit before pushing the first tag or merging a release PR: these actions publish a public GitHub Release. Afterward, check the six archives, `checksums.txt`, `checksums.txt.sigstore.json`, the SBOMs, and the installer step in the workflow log.
