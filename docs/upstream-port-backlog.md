# Upstream port backlog

This document tracks upstream commits worth porting into this fork. The fork
diverged from `multica-ai/multica@b58567ed6` (a desktop icon fix). Today's
state of the divergence:

- 452 fork-side commits ahead of upstream
- 600 upstream commits ahead of the fork
- 14 migration number collisions (migrations 104–117) between the two histories
- Mobile app exists only on upstream
- Lark/Feishu integration exists only on upstream
- Ship Hub, memory artifacts, agent revisions, polling intervals, watchdog
  scripting all exist only on the fork

**Whole-branch merges are not feasible.** Treat the fork as its own product
and selectively port upstream changes that solve real problems here.

## Scoring methodology

For each candidate this doc records:

- **Value (1–5)** — how directly the upstream change addresses a problem the
  fork operator (or their agents) hits.
- **Conflict effort (S / M / L / XL)** — rough estimate of hand-porting time:
  - **S** ≤ 30 min, fork code is structurally similar to upstream
  - **M** 30 min – 2 h, helper functions to extract, light refactor
  - **L** 2 – 6 h, multi-file change, semantic differences in surrounding code
  - **XL** > 6 h, brings in a new subsystem (Lark, mobile, business metrics)
- **Status** — `unported`, `ported`, `wontfix`, `parked`.

## Already ported

| Upstream | Subject | Fork PR |
|---|---|---|
| `3708fb0f0` | fix(daemon): inactivity-based agent run timeout (MUL-3064) | [#190](https://github.com/prellr/multica/pull/190) |

## Top priority — port soon

### `905ebbdde` — fix(github): populate connected account name on install (MUL-3078)

- **Value:** 5 — directly fixes the `account_login = "unknown"` bug that
  forced us to disconnect the GitHub App on 2026-06-07, which in turn put
  all polling traffic on the PAT and triggered the 2026-06-08 rate-limit
  exhaustion. If we'd had this, we wouldn't have disconnected.
- **Conflict effort:** **M** (~2 h). Touches `.env.example` (8 fork commits
  ahead), `server/internal/handler/github.go` (5 ahead),
  `server/internal/handler/github_test.go` (2 ahead), 4 docs files. The
  handler.go file is heavily customized for our Ship Hub flow but the JWT
  signing helper is largely additive.
- **What to do:** Hand-port. Two new env vars (`GITHUB_APP_ID`,
  `GITHUB_APP_PRIVATE_KEY`), one new helper (`signGitHubAppJWT`), and add
  `EventGitHubInstallationCreated` publish to the webhook handler.

### `0da879ec8` — fix(runtime): pause autopilots inside the runtime-delete teardown transaction

- **Value:** 4 — prevents autopilot data corruption when a runtime is
  deleted. We use autopilots heavily.
- **Conflict effort:** **S–M**. Single function change in the runtime
  delete path.
- **What to do:** Read the upstream diff against current
  `server/internal/handler/runtime.go` and apply the same transaction-scoped
  pause logic.

### `2e34016f1` — fix(daemon): interrupt local agent on server-side terminal task states

- **Value:** 4 — cleaner agent shutdown when the server cancels a task. Our
  daemons have been seen lingering on cancelled tasks until the next
  watchdog tick.
- **Conflict effort:** **L**. The upstream diff hides behind a 122-line
  `acquireLocalDirectoryLockIfNeeded` addition the fork doesn't have. Need
  to surgically extract just the terminal-state interrupt logic.
- **What to do:** Don't cherry-pick. Read the actual fix (probably ~30 lines
  inside `runTask`) and port it standalone.

## Medium priority — port when convenient

### `380804936` — fix(codex): set semantic thread names

- **Value:** 3 — codex thread names show meaningful identifiers in logs
  instead of UUIDs, useful when debugging.
- **Conflict effort:** **M**. Conflicts on `agent.go`, `daemon.go`,
  `daemon_test.go`.
- **What to do:** Optional polish. Port if we hit a codex debugging session
  where named threads would have helped.

### `76dbb8776` — fix(agent): standardize model-discovery timeouts to 15s, stop caching empty results

- **Value:** 3 — agent startup reliability. When the provider's model list
  endpoint hangs, our daemon waits longer than necessary AND caches the empty
  result, requiring a restart to recover.
- **Conflict effort:** **S**. Conflicts on `models_test.go` only.
- **What to do:** Apply the 15 s timeout + skip-empty-cache change directly
  to whatever the fork's current model-discovery code looks like.

### `ef75f80d9` — fix(daemon): clean stale agent branches during repo gc (MUL-2550)

- **Value:** 3 — disk hygiene. Agent task workspaces accumulate branches that
  the GC loop doesn't currently delete; over months this fills the workspaces
  partition.
- **Conflict effort:** **M**. Touches `daemon/gc.go` and `gc_test.go`, both
  fork-modified.
- **What to do:** Read the upstream branch-pruning logic and add an
  equivalent step to the fork's GC pass. Test against a workspaces dir with
  ~50 task branches.

### `d6e00e090` — fix(daemon): fail loudly when self-restart spawn fails

- **Value:** 3 — when the daemon tries to self-restart after a panic or
  config reload, a failed `exec` currently exits silently with no log line
  visible to the operator. The fix surfaces the spawn error.
- **Conflict effort:** **S**. Single file (`cmd_daemon.go`), 3 fork commits
  ahead on that file but the change is additive (one error-log call).
- **What to do:** Trivial port. Skim the upstream diff and add the equivalent
  `slog.Error` + non-zero exit to the fork's self-restart path.

### `14f89bc08` — Fix Claude control request handling

- **Value:** 3 — Claude integration robustness. Specifics depend on the bug;
  worth reading the linked Anthropic SDK changelog to understand.
- **Conflict effort:** **M**. Conflicts on `claude.go` + tests.

## Low priority — port only on demand

| Upstream | Subject | Why low |
|---|---|---|
| `dfc159e1a` | feat: skip agent triggering on `/note`-prefixed comments | Nice-to-have UX; trivial to add a workspace-level setting if we want it |
| `1ddf89a8f` | feat(daemon): enable Antigravity per-agent model selection | We don't use Antigravity |
| `b83b41ff4` `28de8b8bd` | feat(cli): per-status error copy + central error translation | Improves CLI error messages; useful if we expand fleet of CLI users |
| `18a5224fe` | feat(cli): `--mcp-config` flags on agent create/update | Nice for scripting agent setup |
| `f5db77340` | feat(web): native notification banners for the web app | Cosmetic UX |
| `a9a9e9390` | fix(core): scope inbox notification mute to source workspace | Multi-workspace polish; only matters if a user belongs to >1 workspace |
| `0c80c33c6` | feat(issues): brand border beam on active agent header chip | Visual polish |
| `072404d91` | fix(issues): header chip "is queued" wording | Copy fix |

## Won't port

| Upstream | Reason |
|---|---|
| All `feat(lark): ...` / `feat(feishu): ...` (~15 commits) | We don't use Lark/Feishu |
| `de900b2ba` `9c9afd4a6` `feat(server): PostHog pairing + BusinessSamplerCollector` | Adds tracking we don't want; introduces metrics deps |
| All `feat(mobile): ...` / `fix(mobile): ...` (~80 commits) | The fork doesn't ship a mobile app; pulling it in means signing on to maintaining 283 files of code we don't use |
| `1abd0e33a` `fix(transcript): close dialog on desktop navigation` | Desktop-only behavior; we don't ship our own desktop build |
| Anything migration-numbered 104–117 | Migration collision with fork-side migrations of the same numbers — would require renumbering ours, which is its own project |

## Working with this doc

When you decide to port a candidate:

1. Branch off `main` as `chore/port-<short-name>`.
2. Read the upstream commit in full. Understand the BEHAVIOR change, not
   just the diff. The fork has refactored enough surrounding code that
   blind copy-paste rarely works.
3. Apply the equivalent logic to the fork's current files.
4. Preserve the upstream commit reference in the fork commit message
   (`Ported from upstream commit <sha> (MUL-NNNN, PR #NNNN)`) so future
   readers can trace.
5. Run `go build ./...`, `go vet ./...`, and at least the relevant
   `go test` package. Full `make check` requires Docker which is awkward
   on this dev box.
6. Update this doc — move the entry from `unported` to `ported` with a
   link to the fork PR.

## Periodic refresh

Re-run the upstream survey roughly monthly so the backlog doesn't go stale.
Quick refresh commands:

```bash
git fetch upstream
git log upstream/main ^origin/main --since=30.days --oneline \
  --pretty=format:"%h %s" \
  | grep -iE "^[a-f0-9]+ fix\(daemon|fix\(agent|fix\(github|fix\(runtime"
```

Adjust the grep to match your interests.
