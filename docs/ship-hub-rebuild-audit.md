# Ship Hub Rebuild Audit

**Date:** 2026-05-20
**Auditor:** Background research session (no code changes)
**Source tree:** `prellr/multica` @ `chore/sync-upstream-batch-6-tier1`
**Scope:** `server/internal/{handler,service}/ship*`, `server/pkg/db/queries/{pull_request*,ship_release*,deploy*}.sql`, `packages/{core,views}/ship/`, `packages/core/api/schemas.ts`, `.github/workflows/deploy-production.yml`, `server/cmd/server/ship_hub_*.go`

---

## TL;DR

Two distinct problems, one rebuild.

**Problem 1 — DB-as-truth for ephemeral GH state.** Ship Hub treats its own Postgres tables as the source of truth for state that physically lives at GitHub (PR CI status, mergeable, review decisions, deploy SHA, workflow_run conclusions). The reconciler and the "Sync Now" button both call **the same** `upsertPR` writer, and that writer hard-codes `ci_status = ""`/ `review_decision = ""` on every call (`server/internal/service/ship/service.go:224-225`). Webhooks repair the fields a few seconds after they fire — and then the very next 5-minute reconciler tick blanks them again. The release stage column is written at point-in-time during merge/promotion and never re-derived from "are the PRs merged + has the deploy SHA propagated?", so a release sitting in `in_staging` after a manual cancel-by-DB stays under Active forever.

**Problem 2 — One-size-fits-all kanban + lifecycle.** The 6 repos Ship Hub manages have **5 distinct pipeline shapes** (see Section 4.2: direct-to-prod CD, staged with strict manual prod gate, fully manual cross-repo, library/image publish, manual SSH+compose). The `project.pipeline_kind` enum has 2 values. The kanban columns are hardcoded. Result: Ship walks operators through the multica-style flow (`In Review → Promoting → In Production → Done`) on a manual-compose repo where "Promoting" does nothing useful, and hangs `library` repos in `promoting` forever waiting on a deploy event that never arrives.

**Rebuild target:** DB is a cache, GitHub is truth, stage is a pure function of (PRs + deploys + pipeline-config + acks), and the pipeline config itself is re-introspected from the repo's workflows rather than hand-edited. Each repo declares its own pipeline shape; kanban renders from that shape; pipeline drift gets caught by the same observation loop everything else uses. 9 sequenced PRs. PR1+PR2 alone fix the operator-most-painful pair (CI status clobbering + Sync Now); PR4 + PR5 are the two architectural payloads (read-time stage derivation + per-repo pipeline config); PR8 closes the "observe, don't assume" loop for the pipeline shape itself; PR6, PR7, PR9 are independently mergeable refinements.

---

## 1. Read-path inventory

Every Ship Hub UI surface, end to end.

### 1.1 Per-surface table

| UI surface | Data shown | API endpoint | Source within endpoint | Staleness source |
|---|---|---|---|---|
| **Ship landing — project list** | Project rows + open-PR count + env count | `GET /api/ship/projects` (`server/internal/handler/ship.go:274`) | DB only — `ListProjects` + `CountOpenPullRequestsForProjects` + `CountDeployEnvironmentsForProjects`. No live GH calls. | Project count + env count cached only via the same `upsertPR` sync; merged-PR rows linger as open until the next reconciler tick if GH state changed without a webhook |
| **Per-project Kanban (open + recently merged)** | PR rows with `state`, `ci_status`, `review_decision`, `mergeable`, `risk_level`, `active_release` decoration | `GET /api/projects/{id}/pull_requests?state=open` (`ship.go:361`) | DB read of `pull_request` table + bulk decoration via `ListActiveReleasesForPullRequests` (`ship.go:773`). No live GH. | **Bug 1**: `ci_status` blanked by every `upsertPR` write. **Bug 7**: cached pull_request_check conclusion is "latest run for head_sha", not "did the PR ever pass". |
| **PR card (Kanban tile)** | Author, title, additions/deletions, `ci_status`, `review_decision`, `mergeable === "CONFLICTING"` warning, risk badge | Same query as above — card just reads its slice of the response | DB | **Bug 6**: `mergeable === "UNKNOWN"` (GH "computing" state) renders no chip at all. UI never re-polls; user has to refresh to learn the answer settled. |
| **PR detail drawer** | Full PR row + checks + `active_release` | `GET /api/pull_requests/{id}/details` (`ship_pr_details.go`) | DB — wraps the same PR row + `ListChecksForPullRequest`. No live GH. | Same blanking + computing bugs as above |
| **Sync Now (per project)** | Triggered by chip; returns SyncResult `{repo, upserted, errors}` | `POST /api/projects/{id}/pull_requests/sync` (`ship.go:407`) | Calls `Service.SyncProject` → `Service.upsertPR` → blank `ci_status` write | **Bug 2**: Sync Now is the worst possible button — it actively *wipes* the freshest CI data the webhook just wrote. The 30-second `staleTime` would invisibly recover via the next webhook event, but a Sync Now click forces a regression. |
| **Workspace summary widget** (ambient sidebar) | `in_staging`, `awaiting_review`, `failing`, `in_production_24h`, `promotion_pending` counts | `GET /api/workspaces/{id}/ship_hub/summary` (`ship_summary.go:52`) | DB — folds `ci_status`, `mergeable`, `risk_level` from `pull_request` rows; `failing` segment reads `ci_status === "failure" OR mergeable === "CONFLICTING"` | Same blanking bug propagates: a PR with a webhook-confirmed failing CI moves out of "failing" the next reconciler tick because `ci_status` becomes empty string |
| **Active Releases rail** (landing page) | One card per non-terminal release | `GET /api/workspaces/{id}/releases/active` (`ship_release.go:585`) | DB-only — `ListActiveReleasesByWorkspace` filters `WHERE stage NOT IN ('done', 'rolled_back', 'cancelled')` (`server/pkg/db/queries/ship_release.sql:32`) | **Bug 3**: stage transitions are write-time only. A release marked done in one place (or stuck after a server restart between `mergeTrainComplete` and the deploy webhook) stays in the rail. There is no read-time reconciliation against `merged_main_sha === env.current_sha`. |
| **Release detail page** | Release row + PR membership + signoffs + events | `GET /api/releases/{id}` (`ship_release.go:488`) | DB-only — `GetReleaseInWorkspace` + `ListReleasePullRequests` + `ListReleaseEvents`. Polls every 5s when stage in `("merging", "promoting")`; otherwise no polling. | Stage shown is whatever was last written; if the orchestrator crashed mid-train, `merge_paused` may not be set and the user has no signal |
| **Deploy swimlanes** (per-env pills row) | Recent deploys (≤50) with status/SHA | `GET /api/deploy_environments/{id}/deploys` (`ship.go:716`) | DB read of `deploy` table. `current_sha` on `deploy_environment` is recomputed via `RecomputeEnvCurrentFromDeploys`. | Webhook-driven (`deployment_status`) or poller-driven (`ship_hub_adapter_poller`). When neither fires (Vercel/Netlify/Cloudflare), the env row is permanently stale until the user clicks "Mark deployed". |
| **Release history** | Recent terminal releases | `GET /api/projects/{id}/releases?status=all` | DB | Static |
| **PR conversation channel badge** | "This channel is the conversation for PR #N" | DB lookup (`GetPullRequestByConversationChannel`) | DB | Static |
| **PR chip row** (merge/comment/rebase/nudge/etc.) | Buttons gated by client-derived `usePrChips({ state, ci_status, review_decision, mergeable, ... })` | Pure client compute over the PR row | Inherits all PR-row staleness | If `ci_status === ""` (blanked by reconciler), Merge chip's "passing CI" gate is wrong (it currently treats empty as "passing-eligible" with a `requireGreenCI` guard — see `use-pr-chips.ts:261`) |

### 1.2 Where staleness creeps in (pattern summary)

Every UI value falls into one of four buckets:

1. **DB-cached PR row written by `upsertPR`.** `ci_status`, `review_decision`, `mergeable`. Reset to empty on every sync.
2. **DB-cached PR row written by `UpdatePullRequest*` field-targeted updates.** `state`, `pr_merged_at`, `head_sha`, `ci_status` (when set via `UpdatePullRequestCIStatus`). These survive — until the next blanking upsert overwrites them.
3. **DB-stamped release columns.** `stage`, `merged_at`, `staged_at`, `promoted_at`. Written once, never re-derived.
4. **DB-cached deploy row + env.current_sha.** Updated on `deployment_status` webhook or adapter poll. If the env's deploy provider is silent (Mac mini self-hosted CD is silent until the workflow run completes), the env shows yesterday's SHA.

Pattern: **every read path is "SELECT from cache"; no read path consults live GH; freshness is implicit.**

---

## 2. Sync-writer audit

Every function that writes ship state, where it's invoked from, and what assumptions it makes.

### 2.1 Pull request writers

| Function | File:line | Writes | State-conditional skips | Triggered from |
|---|---|---|---|---|
| `Service.upsertPR` | `server/internal/service/ship/service.go:191` | Full PR row via `UpsertPullRequest` | **None** — fires on every state. Hard-codes `ci_status = ""` and `review_decision = ""` (lines 224-225). | (a) `Service.SyncProject` (reconciler tick + Sync Now); (b) `processPullRequest` webhook (`webhook.go:129`) — every `pull_request.*` event including `closed`/`merged`/`synchronize`; (c) `processPush` indirectly via SyncProject |
| `Queries.UpsertPullRequest` | `server/pkg/db/queries/pull_request.sql:48` | `pull_request` ON CONFLICT update — overwrites `ci_status`/`review_decision`/`mergeable` from EXCLUDED on every conflict (lines 74-76) | None | Only via `upsertPR` above |
| `Queries.MarkPullRequestMerged` | `server/pkg/db/queries/pull_request.sql:25` | `state='merged'`, `pr_merged_at`, `fetched_at` | None | (a) `actionMerge` after a successful GH MergePullRequest (`actions.go:300`); (b) `release_merge.processMergeJob` (`release_merge.go:629`). Preserves `ci_status` (does not touch the column). |
| `Queries.MarkPullRequestClosed` | `pull_request.sql:38` | `state='closed'`, `pr_closed_at`, `fetched_at` | None | `actions.go:512,537` for close + close-as-stale chips |
| `Queries.UpdatePullRequestStateFromWebhook` | `pull_request_check.sql:53` | Title/state/draft/refs/mergeable/timestamps | **Unused** — defined but no caller. The webhook path goes through `upsertPR` instead. | n/a |
| `Queries.UpdatePullRequestCIStatus` | `pull_request_check.sql:37` | Only `ci_status` + `fetched_at` | None | `processCheckRun` (`webhook.go:463`) and `processStatus` (`webhook.go:534`) after a check/status webhook |
| `Queries.UpsertPullRequestCheck` | `pull_request_check.sql:1` | Single check row (per pr, head_sha, name) | None | `processCheckRun` + `processStatus` webhook handlers |
| `Queries.UpdatePullRequestLinkage` | `pull_request.sql:100` | `originating_issue_id`, `originating_agent_task_id`, etc. | nargs preserve unset columns | `webhook.go:147` `ApplyLinkage` |
| `Queries.UpdatePullRequestRiskProfile` | `pull_request.sql:151` | `risk_level`, `risk_reasons`, `risk_classified_at` | None | `risk.go` classifier called from `webhook.go:167` on opened/synchronize/edited/ready_for_review (skipped on close — see comment at `webhook.go:165`) |
| `Queries.UpdatePullRequestStackParent` | `pull_request.sql:116` | `stack_parent_pr_id` | None | `recomputeStackParent` (`webhook.go:271`) |

**The critical bug**: `upsertPR` is the only writer for new-PR rows AND it runs on every webhook AND every sync tick. There are surgical writers for individual fields (`UpdatePullRequestCIStatus`, `UpdatePullRequestReviewDecision`, `UpdatePullRequestStateFromWebhook`) — they just aren't used by the sync/webhook PR path. `processPullRequest` calls `upsertPR` for simplicity, then later re-reads the row (`webhook.go:132`); the surgical CI/review updates only land via separate `check_run` and `pull_request_review` events.

The sequence of writes for a typical PR life is:
1. `pull_request.opened` webhook → `upsertPR` writes row with `ci_status=""`, `review_decision=""`.
2. `check_run.completed` webhook (a few seconds later) → `UpdatePullRequestCIStatus` writes `ci_status="success"`.
3. `pull_request_review` webhook → `UpdatePullRequestReviewDecision` writes `review_decision="APPROVED"`.
4. PR merges. `pull_request.closed` webhook → `upsertPR` writes row again, BLANKS `ci_status` and `review_decision` to "".
5. UI shows merged PR with no CI status.
6. (Or: 5-minute reconciler tick → `upsertPR` blanks `ci_status` even if the PR is still open.)

### 2.2 Release writers

| Function | File:line | Writes | Triggers |
|---|---|---|---|
| `Service.CreateRelease` | `release.go:115` | Insert `ship_release`, attach PRs, optionally transition to `in_staging` (`release.go:280`) when all selected PRs already merged | `POST /api/projects/{id}/releases` |
| `Service.CancelRelease` | `release.go:550` | Update stage to `cancelled` | `POST /api/releases/{id}/cancel` |
| `Service.StartMergeTrain` | `release_merge.go:277` | Update stage to `merging` | `POST /api/releases/{id}/start_merge` |
| `Service.AbortMergeTrain` | `release_merge.go:438` | Update stage to `cancelled` | `POST /api/releases/{id}/abort_merge` |
| `Service.completeMergeTrain` | `release_merge.go:793` | Update stage to `in_staging` or `promoting` (per `project.pipeline_kind`), stamp `merged_at`, set `merged_main_sha` | End of merge train loop in `release_merge.go` |
| `Service.handleStagingDeployLanded` | `release_staging.go:147` | Stage `in_staging` → may auto-trigger smoke; on smoke pass → stage `verifying` (`release_staging.go:173`) | Deploy adapter webhook + manual `MarkReleaseStagingDeployed` |
| `Service.MarkVerified` | `release_promotion.go:81` → `UpdateReleaseStage` to `verifying` etc. | Stage transitions | Promote chip flow |
| `Service.PromoteRelease` | `release_promotion.go:112` | Stage `verifying` → `promoting` | `POST /api/releases/{id}/promote` |
| `Service.markProductionDeployed` | `release_promotion.go:281` | Stage `promoting` → `in_production` | Production deploy_status webhook + manual button |
| `Service.MarkReleaseDone` | `release_promotion.go:468` | Stage `in_production` → `done` | Manual or 24h post-deploy auto |
| `Service.RollbackRelease` | `release_promotion.go:402` | Stage to `rolled_back` | Rollback chip |
| `Service.syncReleasePRMergeState` | `webhook.go:207` | Updates `ship_release_pull_request.merge_state` from `merging` to `merged` when a stale row is observed merged on GH | After PR merge webhook |

**Pattern**: every stage transition is a one-shot SQL write keyed off the prior stage. There's no idempotent "given current world, what stage should this be?" reconciler. If any write fails halfway, the release is permanently in an inconsistent state. Operationally observed: Pilot's "Active Releases doesn't clear" bug is exactly this — somewhere a release got into `in_staging`/`in_production` but never advanced to `done`, and nothing periodically asks "should this be done now?"

### 2.3 Deploy writers

| Function | File:line | Writes | Triggers |
|---|---|---|---|
| `Queries.InsertDeploy` | `deploy.sql` | New `deploy` row | (a) `LogDeploy` (`ship.go:649`) — user/CI manual; (b) `PollDeployEnvironment` (`ship_deploy_adapter.go:183`); (c) `processDeployment` / `processDeploymentStatus` webhooks; (d) `RollbackDeployEnvironment` (`ship_deploy_adapter.go:255`) |
| `Queries.RecomputeEnvCurrentFromDeploys` | `deploy.sql` | `deploy_environment.current_sha`/`current_deployed_at` from latest succeeded deploy | After every `InsertDeploy` that lands a succeeded status |
| `Service.linkStagingDeployForRelease` (handler-side `linkStagingDeployForRelease`) | `ship_release_staging.go:?` | Matches `deploy.sha` to a release's `merged_main_sha`, advances stage | Webhook + manual log-deploy path |

### 2.4 Workflow run / adapter pollers (background)

| Poller | File:line | What it does |
|---|---|---|
| `runShipHubReconciler` (5 min) | `server/cmd/server/ship_hub_reconciler.go:30` | Calls `Service.SyncWorkspace` → blanks ci_status across the workspace |
| `runShipHubDeployWorkflowPoller` (2 min) | `server/cmd/server/ship_hub_deploy_workflow_poller.go:87` | For each workspace + env with a configured deploy workflow filename: list last 10 completed runs, match `head_sha === release.merged_main_sha`, synthesize a deploy row |
| `runShipHubAdapterPoller` (5 min) | `server/cmd/server/ship_hub_adapter_poller.go` | For each env with an adapter that `SupportsPoll`: ask "what SHA is live?" |
| `runShipHubHealthMonitor` | `server/cmd/server/ship_hub_health_monitor.go` | Periodic release health rollup |

### 2.5 Summary — sources of drift

| Drift source | Cause | Symptom |
|---|---|---|
| 5-min reconciler blanking | `upsertPR` writes `ci_status=""` for every PR | PR cards show "CI unknown" within 5 min of webhook |
| Closed-PR sync blanking | `SyncProject` lists last 25 closed/merged PRs and calls `upsertPR` for each | Merged PRs in "Recently Merged" never show their final CI status |
| Stage written, never re-derived | `UpdateReleaseStage` is one-shot | Releases stuck at `in_staging` / `in_production` |
| Mergeable=UNKNOWN never re-fetched | `mapMergeable(nil) → "UNKNOWN"`; no follow-up poll | "mergeable: computing" on GH forever |
| `pull_request_check` shows latest run for head_sha | `recomputeCIStatus` folds every check on current head_sha; a flake-pass sequence shows whichever finished last per name | Retried-and-passed CI may show as failed if a late-arriving rerun row mutated it back |
| Direct merges to main bypass releases | `processPush` fires `SyncProject` but does NOT create or attach a release record | Direct deploys never appear in release history; production CD still fires |

---

## 3. Stage derivation today vs. proposed

### 3.1 Today: every stage transition is a manual SQL UPDATE

`server/pkg/db/queries/ship_release.sql:74-88` — `UpdateReleaseStage` sets `stage = $2` with an optional accompanying timestamp. The service layer is responsible for picking the right next stage and the right timestamp at each point in the lifecycle. The transitions table:

| Stage transition | Service function | Pre-condition checked | Failure mode if it doesn't fire |
|---|---|---|---|
| `assembling` → `merging` | `StartMergeTrain` (`release_merge.go:277`) | `release.Stage == assembling` | Release stuck in `assembling` |
| `merging` → `in_staging`/`promoting` | `completeMergeTrain` (`release_merge.go:793`) | All PRs merged + `project.pipeline_kind` consulted | Release stuck `merging` if server killed mid-loop |
| `in_staging` → `verifying` | `handleStagingDeployLanded` (`release_staging.go:147`) + smoke pass logic | `merged_main_sha === deploy.sha` AND smoke conclusion === success | Release stuck `in_staging`, this is the most common stuck state per Pilot |
| `verifying` → `promoting` | `PromoteRelease` (`release_promotion.go:112`) | `release.Stage == verifying` | Manual; only happens via UI click |
| `promoting` → `in_production` | `markProductionDeployed` (`release_promotion.go:281`) | Production deploy_status webhook or manual mark | Release stuck `promoting` if Vercel/Netlify/Cloudflare don't fire deployment_status |
| `in_production` → `done` | `MarkReleaseDone` (`release_promotion.go:468`) | Manual button OR 24h post-deploy timer (if implemented) | Release stays in `in_production` ≈ in "Active Releases" forever |
| Any → `cancelled`/`rolled_back` | `CancelRelease`/`RollbackRelease` | Manual | n/a |

The brittleness of this: every stage requires a write to fire at exactly the right moment. The system has multiple write triggers (webhooks, adapter polls, workflow run poller, manual buttons), but no "audit pass" that says "given the data I see now, where SHOULD this release be?"

### 3.2 Proposed: `derive_stage(release) → stage` as a pure function

```
derive_stage(release, prs_in_release, deploys_for_repo, env_state):
  if release.stored_stage in ("cancelled", "rolled_back", "done"):
    # terminal — never re-derive
    return release.stored_stage

  if any pr.state in ("open", "draft") for pr in prs_in_release:
    return "assembling"

  if any pr.membership.merge_state == "merging" for pr in prs_in_release:
    return "merging"

  # All PRs landed. Find their canonical "merged onto main" SHA.
  merged_shas = [pr.head_sha for pr in prs_in_release if pr.state == "merged"]

  staging_env = env_state.staging
  prod_env    = env_state.production

  # If a prod deploy is already done with the release's merge SHAs,
  # we're already in production. (Catches direct-to-prod projects
  # and tracking-only releases.)
  if prod_env and prod_env.current_sha in merged_shas:
    age = now() - prod_env.current_deployed_at
    if age > 24h:
      return "done"
    return "in_production"

  # If a prod deploy is in flight with one of these SHAs:
  if any deploy.env == "production" and deploy.sha in merged_shas and deploy.status == "in_progress" for deploy in deploys_for_repo:
    return "promoting"

  # Verifying = staging passed smoke + we're awaiting promotion click.
  if release.qa_verified_at is not None and not (prod_env and prod_env.current_sha in merged_shas):
    return "verifying"

  # In staging = deploy landed, awaiting verify.
  if staging_env and staging_env.current_sha in merged_shas:
    return "in_staging"

  # No staging env (direct_to_prod project) + no prod deploy yet =
  # we're between merge and prod.
  if project.pipeline_kind == "direct_to_prod":
    return "promoting"

  # Default: still post-merge, awaiting staging deploy.
  return "in_staging"
```

Properties of this approach:

- **Idempotent** — calling it 1000 times yields the same answer.
- **Self-healing** — a release stuck in `in_staging` because a webhook missed will advance the moment `env.current_sha` is updated by the next poll.
- **Race-free** — no two writers competing to set the same stage; the "store" is just `(merged_at, qa_verified_at, promoted_at, done_at, rollback_reason)`, all monotone facts.
- **Reads more, writes less** — every fetch of a release detail runs the function; writes only stamp the monotone timestamps.

The transition writes that remain:
- `start_merge`: stamps `merge_started_at` (new column).
- `mark_verified`: stamps `qa_verified_at`.
- `promote`: stamps `promoted_at`.
- `rollback`: writes `rolled_back_at` + `rollback_reason`.
- `mark_done`: writes `done_at` (overrides the 24h auto-derivation).
- `cancel`: writes `cancelled_at` + the user's reason.

The `stage` enum column becomes either (a) cached, regenerated by a trigger or a periodic reconciler, or (b) dropped entirely — replaced with the derived value in the response.

---

## 4. Per-repo pipeline introspection

### 4.1 multica-prod (`prellr/multica`)

**Stored pipeline kind**: not directly inspected at DB level, but `project.pipeline_kind` was added in migration `095` defaulting to `staged`. The presence of `~/.../deploy-production.yml` + the workspace `ship_hub_deploy_workflow_production = deploy-production.yml` setting (per the workflow header comment) tells me this project is tracked.

**Actual deploy story** (`.github/workflows/deploy-production.yml`):
- Trigger: `on.push.branches: [main]` AND `workflow_dispatch`.
- Runs on self-hosted `mac-mini-prod` runner.
- Steps:
  1. Checkout (shallow).
  2. Compute build SHA, write to `MULTICA_BUILD_COMMIT`.
  3. Materialize operator `.env`.
  4. **GitHub deployment API call** — `createDeployment` with `environment: 'production'` and `production_environment: true`. This fires GH's `deployment` and `deployment_status` webhooks.
  5. Sweep orphan containers.
  6. Preflight: free ports + cloudflared config match.
  7. `docker compose up -d --build`.
  8. Health check (poll `:FRONTEND_PORT/` for 200).
  9. Verify built SHA matches running container's `/build_info`.
  10. Mark deployment success/failure via GH API.

**Ship Hub assumptions vs reality**:
- ✅ Workflow does fire `deployment_status` events. The release-staging linkage and the release-promotion `markProductionDeployed` SHOULD work for this repo.
- ❌ **There is no staging environment for multica-prod.** The workflow only has `environment: 'production'`. But migration 095's backfill defaulted projects to `staged` UNLESS a `kind='staging'` env row existed. Whether multica-prod has a staging `deploy_environment` row is the deciding factor — if it does (from earlier dev or a test artifact), `release.completeMergeTrain` will route it to `in_staging`, and the release will wait forever for a staging deploy that never comes.
- ❌ **CD trigger reality**: any direct push (not via Ship) deploys production. Ship has no record of this. The fork's `processPush` webhook handler calls `SyncProject` (pull request refresh) but doesn't create a synthetic release. So a developer pushing a hotfix directly to main produces a production deploy with zero release tracking — exactly Pilot's observed bug.
- ⚠ **Concurrency policy** (`concurrency: deploy-production / cancel-in-progress: false`) means rapid-fire merges queue. The release_merge train can issue 6 merges in 14s; the original behavior (PR #46/#47) was to take prod down for ~2.5 minutes. The current workflow no longer does `compose down`, so it survives. Ship Hub doesn't model this either way — it just sees `deployment_status` events trickle in.

### 4.2 Other tracked repos — actual pipelines

Pulled directly from each repo's `.github/workflows/*.yml` via `gh api`. **Five distinct pipeline shapes are in production right now.** Ship Hub's `project.pipeline_kind` enum (migration 095) models exactly two of them (`staged`, `direct_to_prod`).

#### 4.2.1 `imjenaro/safra-360` — staged with strict manual prod gate
- `promote-main-to-dev.yml`: `on: push: branches: [main]` → fast-forwards `dev`
- `deploy-staging.yml`: `on: workflow_run: workflows: ["Promote main → dev"]` → CDK deploy to STAGING with backend build inlined (Lambda + S3 + CloudFront)
- `deploy-prod.yml`: `on: workflow_dispatch:` **only** — manual confirmation required
- `deploy-backend.yml`, `deploy-frontend.yml`: explicitly DISARMED (header comment: *"previously triggered on push: branches: [main], which made any merge an auto-deploy"*); now `workflow_dispatch` only
- **Pipeline shape**: `in_review → merged_to_main → in_staging → staging_verified → in_production → done`
- **What Ship gets wrong**: Maps to `staged`, but the `staging_verified` stage doesn't exist — Ship goes directly from `in_staging` to `promoting` once a deploy webhook fires for the production environment. There's no representation of "staging is up and someone has eyeballed it."

#### 4.2.2 `imjenaro/control-panel` — fully manual + cross-repo env dependency
- `ci.yml`: `on: pull_request:` and `on: push: branches: [main]` — typecheck/lint/synth only, no deploy
- `deploy-backend.yml`: `on: workflow_dispatch:` only, requires confirmation string `"deploy-prod"` for prod environment. Header comment: *"Manual-only — no auto-deploy on push, no per-merge fire. Mirrors the safra-360 deploy-backend.yml shape."*
- **Cross-repo gate**: requires GitHub Environment secrets that are sourced from a SEPARATE repo's (`safra-360`) CDK outputs (VPC ID, subnet IDs, Aurora secret ARN, Lambda SG ID — all per-env)
- **Pipeline shape**: `in_review → merged_to_main → manual_deploy_pending → in_staging → staging_verified → in_production → done`
- **What Ship gets wrong**: Doesn't model the cross-repo env dependency at all. Ship would mark a control-panel release as `merged` and never advance — because nothing automatic ever fires. Operator has to know "now I open the deploy-backend.yml dispatch UI." Ship Hub never says that.

#### 4.2.3 `prellr/Hermes-Multi` — library/image, no deploy stage
- `ci.yml`: `on: push: branches: [main, production]` and `on: pull_request: branches: [main, production]` — build + lint
- `docker-publish.yml`: `on: push: branches: [main], tags: ['v*']` — builds + publishes Docker image to a registry
- **No deploy step exists**. Downstream consumers pull the image; this repo's pipeline ends at "image published."
- **Pipeline shape**: `in_review → merged → image_published → done` (release = a published image, not a deploy)
- **What Ship gets wrong**: Wants to derive `in_production` from a `deployment_status` event that will never come. Hermes-Multi releases would hang in `promoting` forever. There's no concept here of "consumed by a downstream deploy."

#### 4.2.4 `prellr/multica` — direct-to-prod CD (this fork)
Already documented in 4.1. Maps cleanly to `direct_to_prod`. **The one shape Ship's logic was actually built for.**

#### 4.2.5 `prellr/WineryManager` and `prellr/pulse` — no automation, manual SSH+compose
- Neither has a `.github/workflows/` directory at all
- WineryManager has docker-compose.yml + `mac-mini-setup.sh`; pulse has `infra/docker/` — both are human-driven deploys
- **Pipeline shape**: `in_review → merged → manually_deployed → done`
- **What Ship gets wrong**: Wants to track a deploy event that will never arrive from GitHub. There's no webhook to listen to; the operator has to mark deployed by hand. Ship offers a "Mark Deployed" button but the path to it is buried inside the staged-pipeline UI.

#### 4.2.6 Shape summary

| Repo | Trigger to staging | Trigger to prod | Ship's enum value (today) | Actual shape |
|---|---|---|---|---|
| `prellr/multica` | n/a (no staging) | `push: main` (auto) | `direct_to_prod` ✅ | `direct_to_prod` |
| `imjenaro/safra-360` | `workflow_run` from promote | `workflow_dispatch` | `staged` ⚠ | `staged_strict` (extra verify gate) |
| `imjenaro/control-panel` | `workflow_dispatch` | `workflow_dispatch` (with confirmation) | `staged` ❌ | `manual_only` (cross-repo env dep) |
| `prellr/Hermes-Multi` | n/a | n/a (no deploy) | (unknown) ❌ | `library` (publish, no deploy) |
| `prellr/WineryManager` | n/a | manual SSH | (unknown) ❌ | `manual_compose` |
| `prellr/pulse` | n/a | manual SSH | (unknown) ❌ | `manual_compose` |

Two enum values, six real shapes. **Three of the six don't have an automated deploy event Ship can listen for at all** — yet Ship's whole release-stage state machine is built around receiving one.

**What this means architecturally:**

1. The hardcoded kanban columns (`In Review → Promoting → In Production → Done`) only make sense for `direct_to_prod`. For `manual_only` and `manual_compose`, the natural columns are different (`Merged → Awaiting Manual Deploy → Verified`). For `library`, there's no "deploy" column at all — it's `Built → Published → Consumed`.
2. The "walks you through unnecessary steps" complaint is real: showing a `manual_compose` release a "Promote to production" button is misleading. The button does nothing useful because there's nothing to promote — the operator has to SSH and run docker-compose.
3. `pipeline_kind` as a two-value enum is the wrong shape. Either bigger enum (5-6 named shapes) OR a structured config (ordered stages + per-stage trigger declaration). PR5 below picks the latter — it composes better with future repos.

---

## 5. Target architecture

The hypothesis is correct: **stop treating the Ship DB as truth for ephemeral GH state.** Concretely:

### 5.1 Conceptual model change

- **Authoritative state for "right now" lives at GitHub** (PR open/closed/merged, check_run conclusions, mergeable, review decisions) **and at the deploy provider** (current_sha, last deployed_at).
- **Authoritative state for "what we decided" lives in Ship DB**: a release was created, the user approved promotion, the user clicked rollback. These are monotone facts that don't need re-syncing.
- **The DB cache exists for performance + offline read access** — never as a source of truth for state that can change without our knowledge.

### 5.2 Schema changes

**Add freshness metadata to every cached table**:

```sql
ALTER TABLE pull_request
    ADD COLUMN last_synced_at TIMESTAMPTZ,
    ADD COLUMN sync_etag TEXT,
    ADD COLUMN ci_status_synced_at TIMESTAMPTZ,
    ADD COLUMN mergeable_synced_at TIMESTAMPTZ;
-- fetched_at already exists; semantically rename it to "row_synced_at"
-- via a derived view rather than column rename.

ALTER TABLE deploy_environment
    ADD COLUMN current_sha_synced_at TIMESTAMPTZ,
    ADD COLUMN current_sha_source TEXT; -- "webhook" | "poll" | "manual_log" | "workflow_run_match"
```

**Drop the `stage` write-time enum from being authoritative**:

- Option A (preferred): leave `stage` as a stored cache column, but recompute on read in the API handler via `derive_stage`. A nightly reconciler also rewrites it for consistency.
- Option B (more invasive): drop `stage` entirely from `ship_release` and compute on every read. Saves writes; costs reads.

**Replace `project.pipeline_kind` enum with structured per-project pipeline config** (PR5):

```sql
-- Either as a JSONB column on the existing project table:
ALTER TABLE project ADD COLUMN pipeline_config JSONB;
-- Or as a normalized table for queryability:
CREATE TABLE project_pipeline_stage (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    position INT NOT NULL,            -- order in the kanban
    name TEXT NOT NULL,               -- "Staging deployed", "Image published", ...
    is_terminal BOOLEAN NOT NULL DEFAULT false,
    requires_human_ack BOOLEAN NOT NULL DEFAULT false,
    triggers JSONB NOT NULL,          -- [{kind:"workflow_run", workflow:"deploy-staging.yml", env:"staging"}, ...]
    deploy_environment_id UUID NULL REFERENCES deploy_environment(id),
    UNIQUE (project_id, position)
);
```

The trigger union: `push_branch`, `workflow_run`, `workflow_dispatch`, `deployment_status`, `manual_ack`, `image_publish_tag`. This covers all 5 observed pipeline shapes. New shapes get a new trigger kind, not a new enum value.

Migration seeds the existing projects from Section 4.2's findings. Old `pipeline_kind` column stays read-only during transition; deleted once dynamic kanban (PR5b) ships.

### 5.3 Sync writers — which die, which live as cache-warmers

| Writer | Disposition |
|---|---|
| `Service.upsertPR` (full row blank on every call) | **Replace** with `upsertPRSparse` that only writes immutable identity columns (`id`, `repo_url`, `pr_number`) + uses COALESCE on the mutable ones (`ci_status = COALESCE($input, ci_status)`). State change writes (`state`, `is_draft`) come from a separate `UpdatePullRequestStateFromWebhook` call. CI status is touched only by check_run/status webhooks via `UpdatePullRequestCIStatus`. |
| Closed-PR list scan in `SyncProject` | **Delete**. Merged/closed PRs don't churn; we already get pull_request webhook events for them. Reading the closed page is wasted GH budget that produces blanked rows. |
| `UpdateReleaseStage` calls scattered across `release_*.go` | **Delete**. Replaced by `derive_stage` on read. The monotone timestamps (`promoted_at`, `qa_verified_at`, etc.) stay; the `stage` enum is computed. |
| Reconciler `runShipHubReconciler` | **Keep, but make idempotent for cache hits**. Only refresh PRs whose `last_synced_at > 5min` AND `state == 'open'`. Use `If-None-Match: <sync_etag>` to skip unchanged PRs. |
| Workflow run poller | **Keep**. Already does the right thing (read GH, write deploy row, never blanks PR state). |
| `processPullRequest` webhook | **Refactor**. Currently calls full `upsertPR`. Change to `UpdatePullRequestStateFromWebhook` + targeted linkage/risk updates. CI doesn't move via the PR event payload, so the path stops touching `ci_status`. |
| `processCheckRun` / `processStatus` | **Keep**. Already targeted writes via `UpdatePullRequestCIStatus`. |
| `Service.handleStagingDeployLanded` (one-shot stage write) | **Delete the stage write**. The deploy row insert is sufficient — `derive_stage` will see `env.current_sha === release.merged_main_sha` next read. |
| `Service.markProductionDeployed` (one-shot stage write) | Same. |
| Manual `MarkReleaseStagingDeployed` button | **Keep** as a "insert synthetic deploy row" path; the stage-write side disappears. |

### 5.4 Read endpoints

| Endpoint | Today | Proposed |
|---|---|---|
| `GET /api/projects/{id}/pull_requests` | DB-only | DB-only, but response includes `last_synced_at` per row + a per-row `is_stale` boolean (e.g. `now - last_synced_at > 30s` for open PRs). Older clients ignore. |
| `GET /api/pull_requests/{id}` (drawer) | DB-only | **Optional live-fetch flag** — `?live=true` triggers a synchronous GH GET, updates the cache, returns fresh. Drawer's manual Refresh button uses this. |
| `GET /api/workspaces/{id}/releases/active` | DB-only | DB read + `derive_stage` filter. A release whose derived stage is `done` drops out of the list automatically. |
| `GET /api/releases/{id}` | DB-only | Stage in response is `derive_stage(release, prs, deploys, envs)`. The stored `stage` column becomes a cache. |
| `POST /api/projects/{id}/pull_requests/sync` (Sync Now) | Full GH refetch via `upsertPR` (which blanks fields) | **True live refresh**: fetches PR list AND individual PR detail AND latest checks in one pass; writes via targeted `UpdatePullRequest*` calls; never blanks. The button promises freshness and now actually delivers. |
| `POST /api/pull_requests/{id}/refresh` (new) | n/a | **New endpoint** — per-PR live refresh. Cheaper than project-wide Sync Now. Powers the drawer's refresh button. |
| `POST /api/releases/{id}/reconcile` (new) | n/a | **New endpoint** — re-derives stage, polls all envs, returns a diff. Powers a "Refresh status" button on the release detail page. |

### 5.5 Frontend changes

- **Per-PR card**: add a tiny age indicator (e.g. tooltip on hover: "synced 12s ago"). If `is_stale === true`, render a subtle dot.
- **Sync Now**: change copy from "Sync Now" (which implies refresh) to "Force refresh from GitHub". On click, render an in-flight indicator until the response lands. Show what changed in the toast (5 PRs updated, 0 added).
- **Release detail**: a "Refresh status" affordance that calls `POST /reconcile`. Drop the 5s polling that fires whenever stage is `merging`/`promoting` — it's now redundant against the derive-on-read change.
- **Active releases rail**: rely on `derive_stage` filter in the API response. The rail will self-clear without any frontend logic.
- **Mergeable=UNKNOWN/computing**: derive-on-read or live-fetch resolves the value on next render. Backstop: a polling-with-backoff hook (`useMergeablePoll`) that refetches the PR every 10s while `mergeable === "UNKNOWN"`, capped at 6 attempts.

### 5.6 What we deliberately do NOT change

- **Webhook ingestion** stays. Webhooks are still the fastest path to fresh data; we just stop sabotaging that freshness with overwrites.
- **Adapter polling** for Vercel/Netlify/Cloudflare stays — adapter poll IS the live-fetch for those repos.
- **`merged_main_sha`** stays on `ship_release` as a stored fact (which commit landed on main as part of this release). It's not ephemeral — once a commit is on main, that's permanent.
- **Audit events** (`ship_release_event`) stay. They're the timeline.

---

## 6. Sequenced PR plan

Nine PRs, each independently mergeable. PR1 + PR2 alone kill 4 of the 7 named bugs (the operator-most-painful pair). PR4 + PR5 are the two architectural payloads — read-time stage derivation and per-repo pipeline config. PR5 is the one that fixes "every repo gets the same kanban + walks through unnecessary steps" (suggest landing as 5a schema/introspector/seed + 5b UI flip to reduce risk). PR8 closes the observation loop for pipeline shape itself — operator changes workflow files, Ship re-introspects, diffs against current config, asks operator to approve destructive changes (auto-applies additive ones). PR6, PR7, PR9 are independently mergeable refinements.

**Sequencing rationale**:
- PR1/PR2: lowest risk, highest immediate operator value (fix Pilot's daily pain). Land first.
- PR3: prerequisite for PR4-7 (freshness columns).
- PR4: pure derivation function; refactor with parity tests, no schema flip yet.
- PR5: the architectural keystone for multi-repo correctness. Riskiest single PR — split into 5a (schema+seed+back-compat) and 5b (UI flip + delete `pipeline_kind`).
- PR6: composes correctly with PR5 (synthetic releases use per-repo pipeline).
- PR7: independent freshness fixes.
- PR8: auto-refresh pipeline config from repo state — closes the "observe, don't assume" loop for pipeline shape itself (introspector reused from PR5a, runs on webhook + on-demand + scheduled).
- PR9: optional UX polish for the CD mental-model gap.

### PR1 — Stop blanking CI status and review_decision

**Scope**: minimal surgical fix.
**Description**: Change `Service.upsertPR` (`service.go:191`) to NOT pass `ci_status` and `review_decision` in the upsert params. Update `UpsertPullRequest` SQL to drop those two columns from the INSERT and from the EXCLUDED set in the ON CONFLICT update. New rows get the DB defaults (empty string); existing rows keep whatever was last written by `UpdatePullRequestCIStatus` / `UpdatePullRequestReviewDecision`. Add a regression test (run `upsertPR` after `UpdatePullRequestCIStatus("success")` → assert ci_status remains "success").
**Files**: `server/internal/service/ship/service.go`, `server/pkg/db/queries/pull_request.sql`, new test in `server/internal/handler/ship_service_integration_test.go`.
**Kills**: Bug 1 (CI status clobbering). The biggest single source of Pilot's pain. Visible win — within 5 minutes of deploy, merged PRs stop reverting to "CI unknown".

### PR2 — Make Sync Now a true refresh

**Scope**: targeted enhancement to `SyncProject`.
**Description**: After PR1, Sync Now stops actively harming state. Now make it a real escape hatch. Update `SyncProject` to (a) for each PR fetched, fetch the latest check_runs and call `UpdatePullRequestCIStatus` with the recomputed status; (b) similarly fetch the latest reviews and call `UpdatePullRequestReviewDecision`. The UI button keeps the existing label but the response carries `{fields_updated: ["ci_status", "review_decision", "mergeable"], ...}`. Frontend toast shows what changed.
**Files**: `server/internal/service/ship/service.go`, possibly extend `pkg/github` client with `ListPullRequestReviews`. Update tests.
**Kills**: Bug 2 (Sync Now doesn't actually re-fetch). Operator visible: clicking Sync Now now repairs the state instead of breaking it.

### PR3 — Add freshness metadata + UI staleness indicator

**Scope**: schema + small UI.
**Description**: Migration adds `ci_status_synced_at`, `mergeable_synced_at`, `current_sha_synced_at` to relevant tables (default NULL, set NOW() on writes). API response includes these. Frontend renders a subtle staleness dot when `now - synced_at > 60s` for open-PR rows.
**Files**: `server/migrations/099_ship_freshness.up.sql`, `server/pkg/db/queries/{pull_request,pull_request_check,deploy}.sql`, `server/internal/handler/ship.go` (response shapes), `packages/core/api/schemas.ts`, `packages/views/ship/components/ship-pr-card.tsx`.
**Kills**: provides the foundation for PR4-6. Operator gets first signal that "Multica thinks this is stale" exists.

### PR4 — Derive release stage on read

**Scope**: the headline architectural change.
**Description**: Implement `derive_stage(release, prs_in_release, deploys, envs)` in Go as a pure function. Add unit tests covering every transition (assembling→merging→in_staging→verifying→promoting→in_production→done) plus the "stuck-stage repair" scenarios (orphaned in_staging, merged-but-no-deploy direct_to_prod). Change `GET /api/releases/{id}` and `GET /api/workspaces/{id}/releases/active` to return the derived stage instead of the stored column. The stored column stays for now (still set by existing writes) — we just stop reading it. Active Releases rail self-clears.
**Files**: new `server/internal/service/ship/stage_derive.go`, updates to `server/internal/handler/ship_release.go`, comprehensive new tests.
**Kills**: Bug 3 (Active Releases doesn't clear). The release stuck-state bug class. Visible: releases that were stuck under Active for days vanish on the next page load.

### PR5 — Per-repo pipeline schema + dynamic kanban

**Scope**: the second architectural payload. Generalizes PR4 across repo shapes; this is the one that directly fixes "every repo shows the same kanban columns + walks through unnecessary steps."
**Description**:
Replace `project.pipeline_kind` enum (two values) with structured per-project pipeline config — either a `project.pipeline_config` JSONB column OR a new `project_pipeline_stage` table. Schema captures, per project, the ORDERED list of stages and per-stage metadata:
- `name` (matches what the kanban renders: "Merged to main", "Staging deployed", "Production", "Image published", etc.)
- `triggers`: list of `{kind: "push_branch"|"workflow_run"|"workflow_dispatch"|"deployment_status"|"manual_ack"|"image_publish_tag", config}` — what advances a release INTO this stage
- `is_terminal`: bool
- `requires_human_ack`: bool — surfaces a "Mark verified" button instead of waiting on automation
- `deploy_environment_id`: nullable — links to existing `deploy_environment` rows where applicable

`derive_stage` from PR4 becomes pipeline-aware: it walks the project's stage list, asks "what's the latest stage whose trigger has fired for this release?", returns that. Pure function of (release + PRs + deploys + acks + pipeline config).

Frontend (`packages/views/ship/components/ship-kanban.tsx` and `ship-repo-section.tsx`) renders columns from `project.pipeline.stages` instead of a hardcoded list. The "Promote to production" button only appears for stages whose triggers include `workflow_dispatch` and where it's the operator's job to fire it. A `library` repo has no "deploy" column — just `Built → Published → Done`. A `manual_compose` repo shows a single "Mark deployed" ack button on its terminal stage.

Migration seeds the six shapes documented in Section 4.2 against the existing projects:
- `prellr/multica` → `direct_to_prod` (single deploy stage, trigger = push_branch:main)
- `imjenaro/safra-360` → `staged_strict` (in_review, merged, in_staging via workflow_run, staging_verified via manual_ack, in_production via workflow_dispatch, done)
- `imjenaro/control-panel` → `manual_only` (in_review, merged, awaiting_deploy, in_staging via workflow_dispatch, staging_verified via manual_ack, in_production via workflow_dispatch, done)
- `prellr/Hermes-Multi` → `library` (in_review, merged, image_published via image_publish_tag, done)
- `prellr/WineryManager`, `prellr/pulse` → `manual_compose` (in_review, merged, manually_deployed via manual_ack, done)

Backward-compat shim: the old `pipeline_kind` column stays for now, and a migration step writes the equivalent structured config for every existing project row.

**Files**:
- new migration `100_project_pipeline_config.up.sql` (+`.down.sql`) — schema + seed data
- new `server/internal/service/ship/pipeline_config.go` — config loader, validation
- `server/internal/service/ship/stage_derive.go` — pipeline-aware derivation (extends PR4)
- new `server/internal/handler/ship_pipeline.go` — admin endpoint for editing pipeline config
- `packages/views/ship/components/ship-kanban.tsx` — render from config
- `packages/views/ship/components/ship-pr-card.tsx` — gate the merge/promote buttons on the project's actual pipeline
- new `packages/views/ship/components/pipeline-config-editor.tsx` — UI to edit per-project pipeline (admin only)
- comprehensive tests covering each of the 6 shapes

**Kills**:
- The "every repo gets the same kanban columns" complaint — addressed root-cause
- The "walks through unnecessary steps" complaint — manual-deploy repos no longer show automated promote buttons
- The "release hangs in `in_staging` forever" failure mode for `library` and `manual_*` shapes
- Composes correctly with PR6 below: synthetic releases for direct merges now use the project's actual pipeline rather than the hardcoded one.

**Risk**: this is the highest-risk PR in the plan because it touches schema + writers + readers + UI together. Suggest a two-step landing: (5a) schema + introspector + seed + backward-compat read shim, (5b) flip kanban to read from config. Each smaller PR is independently mergeable.

**The introspector** (built in 5a, reused by PR8): scans three sources to derive the pipeline shape rather than hand-writing it.

1. **`.github/workflows/*.yml`** — primary. Parse `on:` triggers (`push:`, `workflow_run:`, `workflow_dispatch:`, `deployment:`), `environment:` declarations, and the `workflow_run.workflows` graph to discover stage chains. Detects: `direct_to_prod` (push:main → deploy job with environment:production), `staged` (push:main → promote workflow → workflow_run → staging deploy → workflow_dispatch → prod), `library` (no environment + image publish step), `manual_only` (only workflow_dispatch triggers).
2. **`gh api repos/{repo}/environments`** — enumerates named GitHub Environments. Required to map workflow stages to `deploy_environment` rows and to distinguish "deploys to staging" vs "deploys to prod."
3. **Optional `.shiphub.yml` at repo root** — operator-declared overrides for what workflows can't tell you. Schema (sketch):
   ```yaml
   pipeline:
     stages:
       - name: "Staging verified"
         after: in_staging
         requires_human_ack: true
       - name: "Deployed to Mac mini"
         requires_human_ack: true
         marks_terminal: true
   shape_override: manual_compose   # forces shape for repos w/o workflows
   ```

The seed migration runs the introspector against every existing project row, builds the config, and stores it. Repos with no workflows AND no `.shiphub.yml` default to `manual_compose` (operator gets a single "Mark deployed" ack).

**Dependencies**: PR4 must ship first (derive_stage needs to exist as the central derivation function before it can be pipeline-aware).

### PR6 — Direct-merge release synthesis

**Scope**: new code path in webhook handler.
**Description**: When `processPush` fires for the default branch AND the push contains commits NOT associated with any in-flight release (`ship_release_pull_request` lookup), create a synthetic "direct-to-main" release row. Title = the merge commit subject; PRs = the merged-PR rows whose `merge_commit_sha` matches the pushed commits. This makes release history complete: every production deploy has a release record. Workflow_dispatch deploys (manual re-run) also synthesize on the next push event.

After PR5 lands, the synthetic release uses the project's actual pipeline config — so a direct merge to a `library` repo creates a release whose stages are `merged → image_published → done`, not the hardcoded multica-style flow.

**Files**: `server/internal/service/ship/webhook.go`, new tests in `webhook_test.go`. New SQL: `CreateSyntheticReleaseForDirectMerge`.
**Kills**: Bug 4 (direct merges to main bypass release records). Operator-visible: release history now shows every production change.

### PR7 — Mergeable=UNKNOWN polling + flaky CI repair

**Scope**: two related freshness fixes.
**Description**: Part A: frontend hook `useMergeablePoll(prId)` that calls `POST /api/pull_requests/{id}/refresh` (new endpoint introduced in this PR) every 10s up to 6 times while a PR's mergeable is UNKNOWN. The endpoint does a live `GetPullRequest` from GH and calls `UpdatePullRequestStateFromWebhook` (the unused writer that we now wake up). Part B: `recomputeCIStatus` (`webhook.go:572`) takes a "best-ever" flag: if any check_run with this name has historically conclusion=success on this head_sha, treat it as success even if a later rerun is failure. Add a new `pull_request_check.ever_succeeded` boolean tracked on insert/update.
**Files**: `packages/views/ship/hooks/use-mergeable-poll.ts`, new endpoint in `ship.go`, query in `pull_request_check.sql`, service updates.
**Kills**: Bug 6 (mergeable computing never resolves), Bug 7 (flaky CI hides retry-pass).

### PR8 — Auto-refresh pipeline config from repo state

**Scope**: re-run PR5's introspector when the operator changes their repo's deploy story, instead of requiring a hand-edit in Ship UI. Closes the rebuild's "observe, don't assume" loop for pipeline shape itself.

**Description**: A hand-edited pipeline config is just another assumption that drifts when the workflow files change. The fix is to make the introspector continuously authoritative.

Three trigger paths, all converging on the same diff/approve flow:

1. **On-demand button** — "Refresh pipeline from repo" in Ship's project settings. Calls the introspector synchronously and shows the diff.
2. **Webhook on push to the default branch** — if the push touched `.github/workflows/*.yml` or `.shiphub.yml`, re-run the introspector. This is the elegant default: Ship picks up pipeline changes the same way an operator would notice them.
3. **Daily scheduled poll** — covers cross-org repos or any case where webhook delivery is unreliable.

All three:
1. Run the introspector → produce proposed config
2. Diff against current `project.pipeline_config`
3. If no diff → update `last_introspected_at` and exit
4. If diff:
   - **Additive** (new stage at end, new trigger on existing stage): auto-apply with a notification banner
   - **Destructive** (stage renamed, stage dropped, trigger removed): present to operator as a pending change. Show: "Repo added `deploy-perf.yml` triggered by workflow_dispatch. I'd add a 'Performance' stage between Staging and Production. Accept / Reject / Edit." Block apply on operator decision. Any in-flight release at a stage that would be dropped blocks the change with a list of affected releases.

**Schema additions** (small):
```sql
ALTER TABLE project
    ADD COLUMN last_introspected_at TIMESTAMPTZ,
    ADD COLUMN pipeline_config_proposed JSONB,
    ADD COLUMN pipeline_config_proposed_at TIMESTAMPTZ;
```
The "pending change awaiting approval" is stored as the proposed config; on operator approval it's swapped into `pipeline_config`. Operator-direct edits write straight to `pipeline_config` and are flagged as override (the next introspection re-diffs against it).

**Files**:
- `server/internal/service/ship/pipeline_introspect.go` — already built in PR5a; this PR adds the diff + apply paths
- `server/internal/service/ship/webhook.go` — add the workflow-file-change detector to `processPush`
- new `server/internal/service/ship/pipeline_refresh_scheduler.go` — daily poll
- new endpoints in `server/internal/handler/ship_pipeline.go` — `POST /api/projects/{id}/pipeline/refresh`, `POST /api/projects/{id}/pipeline/proposal/accept`, `POST /api/projects/{id}/pipeline/proposal/reject`
- `packages/views/ship/components/pipeline-config-editor.tsx` (extended) — proposal diff view, accept/reject controls

**Kills**: the "pipeline config drifts silently when repo changes" failure mode that would otherwise re-emerge after PR5 lands. Without PR8, an operator editing their workflow file would have to also edit Ship's pipeline config by hand to keep them in sync — the exact "two sources of truth" trap the rebuild is trying to escape.

**Risk**: medium. The diff/apply logic has edge cases (in-flight releases at to-be-dropped stages, operator-direct overrides vs. introspected updates, racing webhook + scheduled poll). Suggest landing destructive-change blocking before auto-apply of additive changes — strict first, relax later if it becomes friction.

**Dependencies**: PR5 must ship first (this PR refreshes what PR5 created).

### PR9 (optional bonus) — Production-CD UI warning

**Scope**: one banner.
**Description**: When a project's `pipeline_kind === "staged"` AND there's an open PR whose base is `main` AND we detect that production CD fires on push to main (detected via workspace setting `ship_hub_deploy_workflow_production` populated + that workflow's trigger has `push: branches: [main]` — checked once at PR card render time, cached), show a banner on the merge chip: "Merging will auto-deploy to production." The detection logic is a small `gh api` call that runs server-side and is cached in `workspace.settings`.
**Files**: `server/internal/service/ship/workflow_detect.go` (new), small render in `ship-pr-card.tsx` or the merge chip.
**Kills**: Bug 5 (auto-deploy mental model gap). Mostly a UX improvement, but it's a Pilot pain point that the rebuild doesn't otherwise address.

---

## 7. The deploy-trigger question

**The two options in the prompt**:

(a) Flip `deploy-production.yml` to `workflow_dispatch`-only — kill auto-CD on push to main.
(b) Keep CD, surface "auto-deploy will fire on merge" in Ship Hub UI pre-merge.

**Recommendation: (b), with PR5 + PR7 combined.**

Reasoning:

1. **The CD is correct for this product.** Multica is a 2-10-person team's coordination tool. Continuous deployment is the default delivery model for that size of team — it's how the team learns the deploy pipeline is healthy, it surfaces breakage immediately, and it removes a manual gate that adds latency without adding safety. Branch protection on `main` is the real safety gate; CD-on-push converts "we merged it" into "we shipped it" automatically.

2. **The actual problem is the mental-model gap, not the trigger.** The operator's expectation is "Promote a release in Ship Hub causes the deploy to fire." The workflow being on `push:main` doesn't break that expectation — every Ship-Hub-driven merge still ends in a production deploy, just via the push trigger instead of `workflow_dispatch`. The bug is that Ship Hub doesn't *say* this. Adding a banner ("Merging fires production deploy") closes the gap without changing the delivery semantics.

3. **Option (a) regresses real workflows.** A `workflow_dispatch`-only setup means a hotfix push without going through Ship Hub never deploys. That's a worse failure mode than "we forgot Ship Hub knew about this" — broken production with no immediate fix path beats slightly-confusing deploy notification.

4. **Option (b) is small.** It's PR5 (synthesize a release for direct merges so they appear in history) plus PR7 (the pre-merge banner). Together that's ~200 LOC and a database read. Option (a) requires editing the workflow + writing a `workflow_dispatch` call into the Promote chip's code path AND handling the failure mode where the dispatch fails AND deprecating the existing push trigger across the deployed Mac mini.

5. **There's a third refinement worth considering**: the workflow already supports BOTH triggers (push + workflow_dispatch). The "workflow_dispatch with `sha` input" path is already wired and works. So a future PR can route Ship Hub's Promote action through `workflow_dispatch` while keeping `push: main` enabled as the safety net — best of both worlds. But that's a v2 polish, not the immediate fix.

**Concrete next step (if option (b) is accepted)**: ship PR5 then PR7 in that order. PR5 alone makes the operator stop missing direct-merge deploys; PR7 prevents accidental "I didn't realize that would deploy" merges.

---

## Appendix A — Quick wins not on the critical path

These are single-file fixes worth flagging but explicitly NOT requested to implement.

1. **`UpdatePullRequestStateFromWebhook` is dead code** (`pull_request_check.sql:53`). It was clearly written as the right path for `processPullRequest` webhook events but never adopted. PR1 partially revives it conceptually; deleting it now would prematurely strip the obvious target for PR6.

2. **Reconciler swallows partial errors silently** (`ship_hub_reconciler.go:84-88`). One bad workspace logs a warning then continues; on its face fine, but there's no aggregated "reconciler hasn't completed a full sweep in N hours" metric. Add a workspace-level `last_full_sync_at` and surface it in Ship Hub settings.

3. **`mapMergeable(nil)` flattens "computing" and "I don't know" into the same UNKNOWN** (`service.go:257`). Add a third state `PENDING` so the frontend can distinguish "GH is computing this right now" from "we have no signal". Then PR6's polling can be tighter.

4. **Sync closed PRs limit hard-coded at 25** (`service.go:141`). For a workspace with high merge volume, "Recently Merged" can show fewer recent PRs than expected. Make it configurable per project.

5. **`syncReleasePRMergeState` only fires when `release.Stage == merging`** (`webhook.go:212`). If a webhook arrives after the train completed but before the orchestrator updated state, the stale row stays. After PR4, this whole function becomes redundant (derive-on-read handles it).

6. **`pull_request_review` webhook never explicitly stamps `review_decision_synced_at`** even though it's the canonical source. Tie a `_synced_at` write into every `Update*` query so PR3's freshness columns aren't NULL-ish for the most recently-fetched data.

7. **The 5-minute `staleTime` on TanStack Query (`shipProjectsOptions`) plus the 30s `staleTime` on PR list queries are doing the wrong job.** They mask the bug: even with the blanking, the cache often hides it because the UI is reading from the in-memory cache, not a re-fetched row. After PR1+PR4 those staleTimes are fine; before, they amplify perceived consistency.

---

## Appendix B — File:line index for the rebuild PRs

For the implementing agent's convenience, the load-bearing line numbers in the current code:

- `server/internal/service/ship/service.go:224` — the literal `pgtype.Text{String: "", Valid: true}` line that needs to change for PR1.
- `server/internal/service/ship/service.go:225` — same for review_decision.
- `server/pkg/db/queries/pull_request.sql:55-86` — `UpsertPullRequest` SQL to update for PR1.
- `server/internal/service/ship/service.go:141` — closed-PR list limit.
- `server/internal/service/ship/release_merge.go:793` — `completeMergeTrain` stage write (the one PR4 replaces).
- `server/internal/service/ship/release_promotion.go:281,402,468` — other stage writes PR4 replaces.
- `server/pkg/db/queries/ship_release.sql:32-35` — `ListActiveReleasesByWorkspace`; replace with a query that returns all non-cancelled and let `derive_stage` filter, OR move filtering into the handler post-derive.
- `server/internal/service/ship/webhook.go:761-786` — `processPush`, the hook for PR5 synthetic releases.
- `server/internal/service/ship/webhook.go:572` — `recomputeCIStatus`, the function PR6 part B modifies.
- `server/internal/service/ship/service.go:257-265` — `mapMergeable`; PR6 part A may want to surface a PENDING state.
- `packages/core/api/schemas.ts:243-245` — `ci_status` / `review_decision` / `mergeable` schema fields; no change needed but useful reference.
- `packages/views/ship/components/ship-pr-card.tsx:366-377` — current CI/review/mergeable pill render; PR3 adds a staleness dot here.
- `packages/views/ship/hooks/use-pr-state.ts:233` — `mergeable === "CONFLICTING"` gate; PR6 adds the polling.

---

**End of audit.**
