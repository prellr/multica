"use client";

import { useMemo } from "react";
import { AlertTriangle } from "lucide-react";
import { useDeployEnvironments, useRecentDeploys } from "@multica/core/ship";
import type {
  PipelineConfig,
  ProjectPipelineKind,
  PullRequest,
} from "@multica/core/types";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { useT } from "../../i18n";
import {
  bucketPullRequests,
  columnsForPipeline,
  type ShipDeploySnapshot,
  type ShipKanbanColumn,
} from "../hooks/use-pr-state";
import { ShipPRCard } from "./ship-pr-card";

interface ShipKanbanProps {
  /** All PRs for the project (any state). The component buckets locally
   *  via deriveShipKanbanColumn — no server-side state filtering required.
   *  Pass an empty array while loading; the empty-state messaging only
   *  fires once `isLoading` is false. */
  pullRequests: PullRequest[];
  isLoading?: boolean;
  /** Project id — required so the kanban can resolve the per-project deploy
   *  environments + recent deploys, which are needed for the merged → in
   *  staging → promoting → in production columns. Phase 1 didn't need this
   *  because there were only review-state columns. */
  projectId: string;
  /** The project's pipeline topology. Drives which stage columns are
   *  visible — `direct_to_prod` projects hide In Staging + Verifying.
   *  Undefined falls back to the full `staged` superset.
   *
   *  PR5b of the Ship Hub rebuild: superseded by `pipelineConfig` for
   *  callers that have it. Kept for backward compatibility with
   *  consumers that don't yet read the structured config from the
   *  project response. */
  pipelineKind?: ProjectPipelineKind;
  /** PR5b — the project's structured pipeline config. When provided,
   *  drives both column visibility AND column display names: each
   *  stage in `config.stages` becomes one kanban column in position
   *  order, with `stage.name` as the column header (no i18n lookup
   *  for custom stage names — the operator's repo-specific copy
   *  wins). Falls back to `pipelineKind`-derived columns when this
   *  prop is undefined.
   *
   *  Stage IDs that match a known `ShipKanbanColumn` get the existing
   *  bucketing + locale label; unknown stage IDs (e.g. library's
   *  `image_published`, manual_compose's `manually_deployed`) render
   *  as empty columns with the operator's chosen name until future
   *  PRs add the per-id derivation. The empty column is the
   *  "Multica sees this stage exists in your repo's pipeline" signal. */
  pipelineConfig?: PipelineConfig;
}

const COLUMN_ACCENT: Record<ShipKanbanColumn, string> = {
  // Semantic accent strips on top of each column for quick visual scanning.
  // Use semantic tokens where available; the saturated accent pulls from
  // the existing chart palette so dark mode still reads. Eight colors
  // chosen to remain distinguishable across the row (no two adjacent
  // columns share a hue).
  drafted: "bg-violet-500/40",
  in_review: "bg-blue-500/40",
  ready_to_land: "bg-emerald-500/40",
  merged_pre_staging: "bg-amber-500/40",
  in_staging: "bg-orange-500/40",
  verifying: "bg-yellow-500/40",
  promoting: "bg-rose-500/40",
  in_production: "bg-teal-500/40",
  done: "bg-zinc-500/40",
};

/** Maps a stage id from PipelineConfig to its color accent. Known ids
 *  (matching ShipKanbanColumn) get the curated palette; unknown ids
 *  (operator-defined or new shapes' stages like image_published)
 *  fall back to a neutral muted accent so the column still scans
 *  visually even before we add a dedicated color. */
function accentForId(id: string): string {
  if ((id as ShipKanbanColumn) in COLUMN_ACCENT) {
    return COLUMN_ACCENT[id as ShipKanbanColumn];
  }
  return "bg-slate-500/40";
}

function ColumnHeader({
  column,
  count,
  displayName,
}: {
  column: ShipKanbanColumn | string;
  count: number;
  /** Optional override for the header text. PR5b passes the
   *  stage.name from pipeline_config so operator-chosen stage labels
   *  (e.g. "Manually deployed" for compose-shape repos) show
   *  verbatim. Falls back to the i18n key lookup for known stage
   *  ids, then to the bare id as a last resort. */
  displayName?: string;
}) {
  const { t } = useT("ship");
  let label = displayName;
  if (!label) {
    if ((column as ShipKanbanColumn) in COLUMN_ACCENT) {
      // Known column id → use the existing locale key. The cast
      // is safe because the `in` check guarantees the id is one
      // of the legacy ShipKanbanColumn enum values.
      label = t(($) => $.kanban.column[column as ShipKanbanColumn]);
    } else {
      // Unknown id with no display name override — render the bare
      // id rather than crash. Operators of new pipeline shapes
      // (library, manual_compose) should always pass displayName.
      label = column;
    }
  }
  return (
    <div className="flex items-center justify-between gap-2 px-2 py-1.5">
      <div className="flex items-center gap-2">
        <span className={`size-2 rounded-full ${accentForId(column)}`} />
        <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {label}
        </span>
      </div>
      <span className="text-xs tabular-nums text-muted-foreground">
        {count}
      </span>
    </div>
  );
}

/** Computes the deploy snapshot the kanban needs to bucket merged PRs into
 *  the correct deploy column.
 *
 *  Phase 2 trade-off — we need recent deploys for both staging AND
 *  production to spot in-flight production deploys. We deliberately call
 *  the hook with both env ids in a stable order so React's hook rules
 *  aren't violated (the env IDs are stable per project; the hook order
 *  doesn't change between renders even when the IDs are missing).
 *
 *  Empty/missing environments fall back to "" so the hook receives a
 *  consistent string; the underlying query is gated by `enabled: !!id`
 *  so an empty string is a no-op.
 */
function useDeploySnapshot(projectId: string): ShipDeploySnapshot {
  const { data: envData } = useDeployEnvironments(projectId);
  const envs = envData?.environments ?? [];
  // Pull the staging + production rows. We only support one of each per
  // project today; future preview environments would need extending here.
  const staging = envs.find((e) => e.kind === "staging") ?? null;
  const production = envs.find((e) => e.kind === "production") ?? null;

  // The "Promoting" column needs to know which SHAs are currently being
  // deployed to production. Use the recent deploys query (already cached
  // by the swimlanes) so we don't double-fetch.
  const { data: prodDeploys } = useRecentDeploys(production?.id ?? "", 20);

  const productionInFlightShas = useMemo(() => {
    const set = new Set<string>();
    for (const d of prodDeploys?.deploys ?? []) {
      if (d.status === "pending" || d.status === "in_progress") {
        if (d.sha) set.add(d.sha);
      }
    }
    return set;
  }, [prodDeploys]);

  return useMemo<ShipDeploySnapshot>(
    () => ({
      staging,
      production,
      productionInFlightShas,
    }),
    [staging, production, productionInFlightShas],
  );
}

export function ShipKanban({
  pullRequests,
  isLoading,
  projectId,
  pipelineKind,
  pipelineConfig,
}: ShipKanbanProps) {
  const { t } = useT("ship");
  const isMobile = useIsMobile();
  // Snapshot is still required for *bucketing* merged PRs into the
  // in_staging / promoting / in_production columns via SHA matching.
  // Only the column *visibility* derivation moved to pipelineKind.
  const snapshot = useDeploySnapshot(projectId);

  // PR5b — when the project has a structured pipeline_config, drive
  // both column visibility AND display labels from it. Otherwise fall
  // back to the legacy pipelineKind-derived superset.
  //
  // Each entry in `columnSpecs` is { id, displayName, bucketKey }:
  // - `id` is the stage id from config (or the legacy ShipKanbanColumn)
  // - `displayName` is the stage.name override (only when pipelineConfig
  //   is present); the ColumnHeader renders it verbatim
  // - `bucketKey` is the ShipKanbanColumn used to look up PRs in
  //   `buckets`. Known stage ids reuse the existing bucket; unknown
  //   ids point at "" so the column always renders empty.
  const columnSpecs = useMemo<
    Array<{ id: string; displayName?: string; bucketKey: ShipKanbanColumn | null }>
  >(() => {
    if (pipelineConfig && pipelineConfig.stages.length > 0) {
      return pipelineConfig.stages.map((stage) => {
        const isKnown = (stage.id as ShipKanbanColumn) in COLUMN_ACCENT;
        return {
          id: stage.id,
          displayName: stage.name,
          bucketKey: isKnown ? (stage.id as ShipKanbanColumn) : null,
        };
      });
    }
    return columnsForPipeline(pipelineKind).map((col) => ({
      id: col,
      bucketKey: col,
    }));
  }, [pipelineConfig, pipelineKind]);

  const buckets = useMemo(
    () => bucketPullRequests(pullRequests, snapshot),
    [pullRequests, snapshot],
  );

  return (
    <div className="space-y-3">
      {/* Failing / blocked rail. Surfaces the same PRs that ALSO show up in
          their column below — this is a parallel "hey look at this" rail,
          not an exclusive bucket. We render it only when non-empty so we
          don't burn vertical space on a healthy project. */}
      {buckets.failing_blocked.length > 0 && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3">
          <div className="mb-2 flex items-center gap-2 text-xs font-medium text-destructive">
            <AlertTriangle className="size-3.5" />
            <span>
              {t(($) => $.kanban.failing_blocked)} ·{" "}
              <span className="tabular-nums">{buckets.failing_blocked.length}</span>
            </span>
          </div>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            {buckets.failing_blocked.map((pr) => (
              <ShipPRCard
                key={`fail-${pr.id}`}
                pr={pr}
                stagingEnv={snapshot.staging}
              />
            ))}
          </div>
        </div>
      )}

      {isMobile ? (
        <div className="space-y-2">
          {columnSpecs.map((spec) => {
            const colPRs = spec.bucketKey ? buckets[spec.bucketKey] : [];
            return (
              <details
                key={spec.id}
                open={colPRs.length > 0}
                className="group rounded-md border bg-muted/20"
              >
                <summary className="cursor-pointer list-none [&::-webkit-details-marker]:hidden">
                  <ColumnHeader
                    column={spec.id}
                    count={colPRs.length}
                    displayName={spec.displayName}
                  />
                </summary>
                <div className="flex max-h-[70vh] flex-col gap-2 overflow-y-auto p-2 pt-0">
                  {colPRs.length === 0 ? (
                    <div className="px-2 py-3 text-center text-xs text-muted-foreground">
                      {isLoading ? "" : t(($) => $.kanban.empty_column)}
                    </div>
                  ) : (
                    colPRs.map((pr) => (
                      <ShipPRCard key={pr.id} pr={pr} stagingEnv={snapshot.staging} />
                    ))
                  )}
                </div>
              </details>
            );
          })}
        </div>
      ) : (
        <div className="overflow-x-auto pb-2">
          <div className="flex gap-3 min-w-max">
            {columnSpecs.map((spec) => {
              const colPRs = spec.bucketKey ? buckets[spec.bucketKey] : [];
              return (
                <div
                  key={spec.id}
                  className="flex w-64 shrink-0 flex-col rounded-md border bg-muted/20"
                >
                  <ColumnHeader
                    column={spec.id}
                    count={colPRs.length}
                    displayName={spec.displayName}
                  />
                  <div className="flex max-h-[70vh] flex-col gap-2 overflow-y-auto p-2 pt-0">
                    {colPRs.length === 0 ? (
                      <div className="px-2 py-4 text-center text-xs text-muted-foreground">
                        {isLoading ? "" : t(($) => $.kanban.empty_column)}
                      </div>
                    ) : (
                      colPRs.map((pr) => (
                        <ShipPRCard key={pr.id} pr={pr} stagingEnv={snapshot.staging} />
                      ))
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
