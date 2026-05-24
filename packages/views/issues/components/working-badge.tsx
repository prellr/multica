"use client";

import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { useActorName } from "@multica/core/workspace/hooks";
import type { AgentTask } from "@multica/core/types/agent";
import { useT } from "../../i18n";

/**
 * Compact "an agent is working on this issue" badge for board cards and
 * list rows. Brand-color pulse dot + optional count of distinct agents.
 * The hover tooltip lists the agent names.
 *
 * The badge is intentionally non-interactive — it sits inside the issue
 * link target, so clicking it should follow the same destination as the
 * rest of the card.
 */
export function WorkingBadge({ tasks }: { tasks: AgentTask[] }) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  if (tasks.length === 0) return null;

  const uniqueAgentIds = Array.from(
    new Set(tasks.map((t) => t.agent_id).filter(Boolean)),
  );
  const tooltipLabel =
    uniqueAgentIds.length === 0
      ? t(($) => $.scope.working_label)
      : uniqueAgentIds.map((id) => getActorName("agent", id)).join(", ");

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            aria-label={t(($) => $.scope.working_label)}
            className="inline-flex items-center gap-1 rounded-full bg-brand/10 px-1.5 py-0.5 text-[10px] font-medium text-brand"
          >
            <span className="relative inline-flex size-1.5">
              <span className="absolute inset-0 animate-ping rounded-full bg-brand opacity-60" />
              <span className="relative inline-flex size-1.5 rounded-full bg-brand" />
            </span>
            {uniqueAgentIds.length > 1 && (
              <span className="tabular-nums">{uniqueAgentIds.length}</span>
            )}
          </span>
        }
      />
      <TooltipContent side="top">{tooltipLabel}</TooltipContent>
    </Tooltip>
  );
}
