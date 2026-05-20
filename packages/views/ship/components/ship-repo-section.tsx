"use client";

import { useMemo } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useCollapsedRepos } from "@multica/core/ship";
import type {
  PipelineConfig,
  ProjectPipelineKind,
  PullRequest,
} from "@multica/core/types";
import { ShipKanban } from "./ship-kanban";
import { useT } from "../../i18n";

interface ShipRepoSectionProps {
  repoUrl: string;
  prs: PullRequest[];
  isLoading?: boolean;
  projectId: string;
  pipelineKind?: ProjectPipelineKind;
  /** PR5b — structured pipeline config from the project response.
   *  Forwarded to ShipKanban which renders columns from
   *  config.stages when provided. Falls back to pipelineKind-derived
   *  columns when this is undefined. */
  pipelineConfig?: PipelineConfig;
}

export function ShipRepoSection({
  repoUrl,
  prs,
  isLoading,
  projectId,
  pipelineKind,
  pipelineConfig,
}: ShipRepoSectionProps) {
  const { t } = useT("ship");
  const repoName = repoUrl.split("/").at(-1) ?? repoUrl;
  const sectionId = `ship-repo-${encodeURIComponent(repoUrl)}`;

  const collapsed = useCollapsedRepos((s) => s.collapsed.has(repoUrl));
  const toggle = useCollapsedRepos((s) => s.toggle);

  const summary = useMemo(() => {
    const open = prs.filter((pr) => pr.state === "open");
    const draft = open.filter((pr) => pr.is_draft).length;
    const blocked = open.filter(
      (pr) => pr.ci_status === "failure" || pr.mergeable === "CONFLICTING",
    ).length;
    const readyToMerge = open.filter(
      (pr) =>
        !pr.is_draft &&
        pr.review_decision === "APPROVED" &&
        pr.ci_status === "success" &&
        pr.mergeable !== "CONFLICTING",
    ).length;
    return { open: open.length, draft, blocked, readyToMerge };
  }, [prs]);

  return (
    <section className="space-y-2 rounded-md border bg-muted/10 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => toggle(repoUrl)}
          className="flex size-5 shrink-0 items-center justify-center rounded hover:bg-muted"
          aria-expanded={!collapsed}
          aria-controls={sectionId}
          aria-label={
            collapsed
              ? t(($) => $.repo_section.expand)
              : t(($) => $.repo_section.collapse)
          }
        >
          <ChevronRight
            className={cn(
              "size-3.5 text-muted-foreground transition-transform",
              !collapsed && "rotate-90",
            )}
          />
        </button>
        <span className="text-sm font-medium">{repoName}</span>
        <span className="text-xs text-muted-foreground tabular-nums">
          {summary.open}
        </span>
        {summary.readyToMerge > 0 && (
          <span className="rounded border border-emerald-500/40 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-medium text-emerald-700 tabular-nums dark:text-emerald-300">
            {t(($) => $.project_summary.ready_pill, {
              count: summary.readyToMerge,
            })}
          </span>
        )}
        {summary.blocked > 0 && (
          <span className="rounded border border-destructive/40 bg-destructive/10 px-1.5 py-0.5 text-[10px] font-medium text-destructive tabular-nums">
            {t(($) => $.project_summary.blocked_pill, {
              count: summary.blocked,
            })}
          </span>
        )}
        {summary.draft > 0 && (
          <span className="rounded border border-muted-foreground/30 bg-muted/30 px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground tabular-nums">
            {t(($) => $.project_summary.draft_pill, { count: summary.draft })}
          </span>
        )}
      </div>

      {!collapsed && (
        <div id={sectionId}>
          <ShipKanban
            pullRequests={prs}
            isLoading={isLoading}
            projectId={projectId}
            pipelineKind={pipelineKind}
            pipelineConfig={pipelineConfig}
          />
        </div>
      )}
    </section>
  );
}
