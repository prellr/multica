"use client";

import { useState } from "react";
import { GitBranch, RefreshCw } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  useAcceptPipelineProposal,
  useRefreshPipeline,
  useRejectPipelineProposal,
} from "@multica/core/ship";
import { ApiError } from "@multica/core/api";
import type { PipelineConfig, PipelineDiff } from "@multica/core/types";
import { useT } from "../../i18n";

interface PipelineProposalBannerProps {
  projectId: string;
  /** The project's current live pipeline config — its `shape` is shown
   *  as the "from" side when the proposal changes the shape. */
  pipelineConfig?: PipelineConfig;
  /** A pending introspected config awaiting Accept / Reject. When
   *  undefined / null, the banner shows only the "Refresh pipeline from
   *  repo" affordance. */
  pipelineConfigProposed?: PipelineConfig | null;
}

/**
 * PR8 — pipeline auto-refresh affordance.
 *
 * Two states, both self-contained:
 *  - No pending proposal → a single "Refresh pipeline from repo" button
 *    that re-runs the introspector on demand.
 *  - Pending proposal → a "Pipeline change detected" banner that lists
 *    what the introspected config would change (added / removed /
 *    renamed stages, shape change) with Accept / Reject controls.
 *
 * Additive changes are auto-applied server-side and never reach this
 * banner; only destructive proposals are parked for review here.
 */
export function PipelineProposalBanner({
  projectId,
  pipelineConfig,
  pipelineConfigProposed,
}: PipelineProposalBannerProps) {
  const { t } = useT("ship");
  const refresh = useRefreshPipeline();
  const accept = useAcceptPipelineProposal();
  const reject = useRejectPipelineProposal();

  const [notice, setNotice] = useState<{
    tone: "info" | "error";
    title: string;
    description?: string;
  } | null>(null);

  const busy = refresh.isPending || accept.isPending || reject.isPending;

  const handleRefresh = async () => {
    setNotice(null);
    try {
      const out = await refresh.mutateAsync(projectId);
      switch (out.kind) {
        case "unchanged":
          setNotice({
            tone: "info",
            title: t(($) => $.pipeline_proposal.refresh_unchanged),
          });
          break;
        case "applied_additive":
          setNotice({
            tone: "info",
            title: t(($) => $.pipeline_proposal.refresh_applied),
          });
          break;
        case "skipped_no_repo":
          setNotice({
            tone: "info",
            title: t(($) => $.pipeline_proposal.refresh_skipped),
          });
          break;
        // "proposed_destructive" needs no notice — the project list
        // refetch re-renders this banner into its proposal state.
        default:
          break;
      }
    } catch (e) {
      setNotice(refreshError(e));
    }
  };

  const handleAccept = async () => {
    setNotice(null);
    try {
      await accept.mutateAsync(projectId);
    } catch (e) {
      // 409 — an in-flight release blocks the destructive change.
      if (e instanceof ApiError && e.status === 409) {
        setNotice({
          tone: "error",
          title: t(($) => $.pipeline_proposal.blocked_title),
          description: t(($) => $.pipeline_proposal.blocked_description, {
            count: affectedReleaseCount(e),
          }),
        });
        return;
      }
      setNotice(refreshError(e));
    }
  };

  const handleReject = async () => {
    setNotice(null);
    try {
      await reject.mutateAsync(projectId);
    } catch (e) {
      setNotice(refreshError(e));
    }
  };

  function refreshError(e: unknown) {
    return {
      tone: "error" as const,
      title: t(($) => $.pipeline_proposal.error_title),
      description: t(($) => $.pipeline_proposal.error_description, {
        message: e instanceof Error ? e.message : String(e),
      }),
    };
  }

  const hasProposal = !!pipelineConfigProposed;

  return (
    <div className="space-y-2">
      {hasProposal && (
        <div
          className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3"
          role="alert"
        >
          <div className="flex items-start gap-2">
            <GitBranch className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
            <div className="min-w-0 space-y-1.5">
              <p className="text-sm font-medium text-amber-700 dark:text-amber-300">
                {t(($) => $.pipeline_proposal.title)}
              </p>
              <p className="text-xs text-muted-foreground">
                {t(($) => $.pipeline_proposal.description)}
              </p>
              <ProposalChangeList
                current={pipelineConfig}
                proposed={pipelineConfigProposed ?? undefined}
              />
              <div className="flex flex-wrap gap-2 pt-1">
                <Button size="sm" onClick={handleAccept} disabled={busy}>
                  {accept.isPending
                    ? t(($) => $.pipeline_proposal.accepting)
                    : t(($) => $.pipeline_proposal.accept)}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={handleReject}
                  disabled={busy}
                >
                  {reject.isPending
                    ? t(($) => $.pipeline_proposal.rejecting)
                    : t(($) => $.pipeline_proposal.reject)}
                </Button>
              </div>
            </div>
          </div>
        </div>
      )}

      <div className="flex items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          onClick={handleRefresh}
          disabled={busy}
        >
          <RefreshCw
            className={`size-3 ${refresh.isPending ? "animate-spin" : ""}`}
          />
          {refresh.isPending
            ? t(($) => $.pipeline_proposal.refreshing)
            : t(($) => $.pipeline_proposal.refresh)}
        </Button>
        {notice && (
          <p
            className={
              notice.tone === "error"
                ? "text-xs text-destructive"
                : "text-xs text-muted-foreground"
            }
          >
            {notice.title}
            {notice.description ? ` ${notice.description}` : ""}
          </p>
        )}
      </div>
    </div>
  );
}

/**
 * Renders the human-readable list of what a proposal would change. The
 * diff is recomputed client-side from the current vs proposed configs so
 * the banner doesn't depend on the server having stamped the diff onto
 * the project row — the proposal columns only carry the config itself.
 */
function ProposalChangeList({
  current,
  proposed,
}: {
  current?: PipelineConfig;
  proposed?: PipelineConfig;
}) {
  const { t } = useT("ship");
  if (!proposed) return null;
  const diff = computeProposalDiff(current, proposed);
  const lines: string[] = [];

  if (diff.shape_changed) {
    lines.push(
      t(($) => $.pipeline_proposal.shape_changed, {
        from: diff.old_shape ?? "",
        to: diff.new_shape ?? "",
      }),
    );
  }
  for (const s of diff.added_stages) {
    lines.push(t(($) => $.pipeline_proposal.added_stage, { name: s.name }));
  }
  for (const s of diff.removed_stages) {
    lines.push(t(($) => $.pipeline_proposal.removed_stage, { name: s.name }));
  }
  for (const r of diff.renamed_stages) {
    lines.push(
      t(($) => $.pipeline_proposal.renamed_stage, {
        from: r.old_name,
        to: r.new_name,
      }),
    );
  }
  if (diff.reordered_stages.length > 0) {
    lines.push(t(($) => $.pipeline_proposal.reordered));
  }
  if (
    diff.triggers_added_stages.length > 0 ||
    diff.triggers_removed_stages.length > 0
  ) {
    lines.push(t(($) => $.pipeline_proposal.triggers_changed));
  }

  if (lines.length === 0) return null;
  return (
    <ul className="list-inside list-disc text-xs text-muted-foreground">
      {lines.map((line) => (
        <li key={line}>{line}</li>
      ))}
    </ul>
  );
}

/**
 * Client-side reimplementation of the server's diff classifier, scoped
 * to the fields the banner displays. Kept intentionally simple — the
 * authoritative classification (and the auto-apply / park decision)
 * already happened server-side; this only describes a parked proposal.
 */
function computeProposalDiff(
  current: PipelineConfig | undefined,
  proposed: PipelineConfig,
): PipelineDiff {
  const cur = current ?? { shape: "", stages: [] };
  const curById = new Map(cur.stages.map((s) => [s.id, s]));
  const propById = new Map(proposed.stages.map((s) => [s.id, s]));

  const diff: PipelineDiff = {
    kind: "destructive",
    shape_changed: cur.shape !== proposed.shape,
    old_shape: cur.shape,
    new_shape: proposed.shape,
    added_stages: [],
    removed_stages: [],
    renamed_stages: [],
    reordered_stages: [],
    triggers_added_stages: [],
    triggers_removed_stages: [],
  };

  for (const s of proposed.stages) {
    if (!curById.has(s.id)) {
      diff.added_stages.push({ id: s.id, name: s.name });
    }
  }
  for (const s of cur.stages) {
    if (!propById.has(s.id)) {
      diff.removed_stages.push({ id: s.id, name: s.name });
    }
  }
  for (const s of proposed.stages) {
    const prev = curById.get(s.id);
    if (!prev) continue;
    if (prev.name !== s.name) {
      diff.renamed_stages.push({
        id: s.id,
        old_name: prev.name,
        new_name: s.name,
      });
    }
    if (prev.position !== s.position) {
      diff.reordered_stages.push(s.id);
    }
    const prevTriggers = (prev.triggers ?? []).length;
    const nextTriggers = (s.triggers ?? []).length;
    if (nextTriggers > prevTriggers) diff.triggers_added_stages.push(s.id);
    if (nextTriggers < prevTriggers) diff.triggers_removed_stages.push(s.id);
  }
  return diff;
}

/** Pulls the affected-release count out of a 409 ApiError body. */
function affectedReleaseCount(e: ApiError): number {
  const body = e.body as { affected_release_ids?: unknown } | undefined;
  const ids = body?.affected_release_ids;
  return Array.isArray(ids) ? ids.length : 0;
}
