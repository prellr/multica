# Fork CLI Distribution — Spec

**Status:** proposed
**Date:** 2026-05-20
**Author:** Ship Hub rebuild session

## Problem

`prellr/multica` is a fork of `multica-ai/multica`. The fork has its own
CLI commands that upstream doesn't have — `squad`, `channel`, `memory`,
`mention`, and (as of #130) `ship`. But the fork has **no working
distribution path** for its CLI. Every distribution reference points at
upstream:

| Reference | Location | Points at |
|---|---|---|
| Release pipeline | `.github/workflows/release.yml:70` — `if: github.repository_owner == 'multica-ai'` | The whole GoReleaser + Homebrew job is **gated off for forks**. A `git tag` on `prellr/multica` runs tests, then skips release entirely. No binaries, no formula. |
| Update check | `server/internal/cli/update.go:76,102` | `multica update` polls `api.github.com/repos/multica-ai/multica/releases` — a fork user gets **upstream's** binary. |
| Install source | `scripts/install.sh:20,68` | `brew tap multica-ai/tap` + `BREW_PACKAGE="multica-ai/tap/multica"`. |

### Observed consequence

The Mac mini production host (`192.168.2.14`) runs `multica` v0.2.20 via
Homebrew. Its `multica --help` lists **none** of the fork commands
(`squad`, `channel`, `memory`, `mention`). That binary is **upstream's
CLI**, not the fork's. The fork has never shipped its own CLI to that
host — or anywhere.

Running `multica update` on the mini today refreshes it to a newer
*upstream* build — still zero fork commands. There is currently no way
for an operator to get the fork's CLI onto a machine except building it
by hand from source.

## Why this matters

- Every fork-specific CLI command (`ship introspect-pipelines`, the
  squad commands, etc.) is unreachable on any machine that installed
  via the documented path.
- The Ship Hub rebuild's `multica ship introspect-pipelines` (#130)
  cannot be run on the Mac mini until this is fixed.
- It silently degrades: an operator who runs `multica update`
  *replaces* a hand-built fork CLI with upstream's, losing every fork
  command, with no warning.

## Goals

1. The fork can cut its own CLI releases (`git tag` → binaries +
   Homebrew formula) without depending on upstream secrets or repos.
2. `multica update` on a fork-installed CLI pulls **fork** releases.
3. The source still merges cleanly from upstream — the distribution
   config must be the *only* fork-specific delta, ideally isolated so
   upstream syncs never conflict on it.
4. No regression for upstream: upstream's release path is untouched.

## Non-goals

- Republishing upstream's CLI. The fork ships its own binary that
  *includes* upstream's commands (they're all in `server/cmd/multica/`)
  plus the fork additions.
- A separate CLI codebase. The CLI stays one tree; only distribution
  diverges.

## Proposed design

### 1. Fork Homebrew tap

Create `prellr/homebrew-multica` (a public repo — Homebrew taps must be
public or the consumer needs auth). GoReleaser writes the formula there
on each fork release.

### 2. Un-gate `release.yml` for the fork

Two viable shapes:

**Option A — owner-keyed config (recommended).** Replace the hard
`if: github.repository_owner == 'multica-ai'` gate with a job that runs
on *any* owner, and have GoReleaser's config select the tap by owner:

```yaml
# release.yml — release job runs for both upstream and fork.
# GoReleaser's brews.repository.owner is set from a repo variable.
```

`.goreleaser.yaml` reads `${{ github.repository_owner }}` (or a repo
variable `HOMEBREW_TAP_OWNER`) so upstream publishes to
`multica-ai/homebrew-tap` and the fork to `prellr/homebrew-multica`
with no per-fork edit beyond the variable + the `HOMEBREW_TAP_GITHUB_TOKEN`
secret.

**Option B — parallel fork job.** Leave the upstream `release` job as-is
and add a second job gated `if: github.repository_owner == 'prellr'`.
Simpler diff, but it's a fork-specific block that every upstream sync
has to skip over. Option A keeps the workflow identical between repos —
preferred for sync hygiene.

Either way the fork needs a `HOMEBREW_TAP_GITHUB_TOKEN` repo secret
with write access to the fork tap.

### 3. Make the update-check repo configurable

`server/internal/cli/update.go` hardcodes `multica-ai/multica` in two
URLs. Replace with a single package-level constant — or better, a
build-time `-ldflags` value — so the fork's released binary checks the
fork's releases:

```go
// update.go
// updateRepo is the GitHub repo the CLI polls for new releases.
// Overridable at build time via -ldflags so a fork's binary checks
// the fork's releases instead of upstream's.
var updateRepo = "multica-ai/multica"
```

GoReleaser sets `-X .../cli.updateRepo=prellr/multica` for the fork
build. Upstream's build keeps the default. **This is the one source
change** — and because it's a single `var` with an upstream-correct
default, an upstream sync never conflicts on it (upstream has no reason
to touch it, and if they do, the merge is a one-line resolve).

### 4. install.sh / install.ps1

Point `REPO_URL`, `REPO_WEB_URL`, `BREW_PACKAGE`, and the `brew tap`
line at the fork. These are pure fork-config edits. To keep upstream
syncs clean, consider extracting them to variables at the top of the
script with a comment marking them as fork-config — so a sync conflict
(if upstream edits install.sh) is an obvious, localized resolve.

## Sequencing

1. **PR-D1** — create `prellr/homebrew-multica` tap repo + add the
   `HOMEBREW_TAP_GITHUB_TOKEN` secret to `prellr/multica`.
2. **PR-D2** — `update.go` configurable `updateRepo` var + `.goreleaser.yaml`
   `-ldflags` + owner-keyed `brews.repository`. Un-gate `release.yml`
   (Option A). This is the load-bearing PR.
3. **PR-D3** — `install.sh` / `install.ps1` fork-config edits.
4. Cut the first fork CLI release: `git tag v0.3.4` (patch bump from the
   last upstream-shared tag v0.3.3) → fork GoReleaser runs → binaries +
   formula land in the fork tap.
5. On the Mac mini: re-tap to the fork (`brew untap multica-ai/tap`,
   install from `prellr/homebrew-multica`) — a ONE-TIME migration. After
   that, `multica update` stays on the fork.

## Risk / watch-items

- **Tag collision.** The fork and upstream share a tag namespace
  conceptually (both use `vX.Y.Z`). Once the fork cuts `v0.3.4`,
  upstream's next `v0.3.4` would diverge. Recommend the fork either
  (a) use a fork-distinct suffix (`v0.3.4-roa`) or (b) move to its own
  numbering line. Decide before the first fork release.
- **The Mac mini re-tap is manual and one-time.** Until it's done, the
  mini stays on upstream's CLI. Document it in the deploy runbook.
- **Homebrew tap must be public** OR every consumer configures auth.
  Public is the pragmatic choice; the formula carries no secrets.

## Until this lands

Fork CLI commands can only run via `go run ./server/cmd/multica ...`
from a source checkout, or a hand-built binary. The
`ship introspect-pipelines` seed was run that way (from the dev
checkout, using the operator's local `~/.multica/config.json` auth).
