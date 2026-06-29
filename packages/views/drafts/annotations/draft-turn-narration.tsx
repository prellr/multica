"use client";

import { useMemo } from "react";
import { Send } from "lucide-react";
import { useStartDraftTurn, useDraftTurnMessages } from "@multica/core/drafts/turn-mutations";
import type { TaskMessagePayload } from "@multica/core/types";
import { useWSEvent } from "@multica/core/realtime";
import { Button } from "@multica/ui/components/ui/button";
import {
  ThinkingSteps,
  type ThinkingStepItem,
} from "@multica/ui/components/ui/thinking-steps";
import { ThinkingIndicator } from "@multica/ui/components/ui/thinking-indicator";
import { toast } from "sonner";
import type { TFunction } from "i18next";
import { useT } from "../../i18n";

/**
 * The Send-turn affordance + narration rail (Drafts slice 2).
 *
 * - DraftSendControl is the header button: it POSTs /api/drafts/:id/turn,
 *   records the returned task id (lifted to the parent so the narration rail can
 *   read it), and shows a working state until the turn completes.
 * - DraftTurnNarration is the rail: while a turn runs it renders the live
 *   task:message stream as Fluid Functionalism ThinkingSteps, with a
 *   ThinkingIndicator tail; on draft:turn_completed it clears.
 *
 * The active-turn task id is transient UI state (one turn per open draft) owned
 * by the editor component — not a store and not server data — so it's passed in
 * as a prop pair rather than read from a cache.
 */

interface DraftSendControlProps {
  draftId: string;
  /** The active turn's task id, or null when idle. Owned by the parent. */
  activeTaskId: string | null;
  /** Called with the new task id when a turn starts (or null on completion). */
  onTurnChange: (taskId: string | null) => void;
}

export function DraftSendControl({ draftId, activeTaskId, onTurnChange }: DraftSendControlProps) {
  const { t } = useT("drafts");
  const startTurn = useStartDraftTurn(draftId);
  const working = activeTaskId !== null || startTurn.isPending;

  const handleSend = () => {
    startTurn.mutate(undefined, {
      onSuccess: (res) => {
        // An empty task_id means the schema fell back (drifted/garbled
        // response) — treat it as "didn't start" rather than entering a turn
        // state we can't narrate.
        if (res.task_id) onTurnChange(res.task_id);
        else toast.error(t(($) => $.turn.start_failed));
      },
      onError: () => toast.error(t(($) => $.turn.start_failed)),
    });
  };

  return (
    <Button
      size="sm"
      variant="default"
      onClick={handleSend}
      disabled={working}
      // Keep the window-drag region clickable on desktop (header is in the
      // top strip); harmless on web.
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
    >
      <Send className="mr-1 h-3.5 w-3.5" />
      {startTurn.isPending
        ? t(($) => $.turn.sending)
        : working
          ? t(($) => $.turn.working, { name: t(($) => $.turn.agent_name) })
          : t(($) => $.turn.send)}
    </Button>
  );
}

interface DraftTurnNarrationProps {
  draftId: string;
  /** The active turn's task id, or null when idle. */
  activeTaskId: string | null;
  /** Called when the turn completes so the parent can clear activeTaskId. */
  onTurnChange: (taskId: string | null) => void;
}

export function DraftTurnNarration({ draftId, activeTaskId, onTurnChange }: DraftTurnNarrationProps) {
  const { t } = useT("drafts");
  const messages = useDraftTurnMessages(activeTaskId);

  // draft:turn_completed clears the active turn (and the rail). Filter to this
  // draft — the event is workspace-scoped and may be for another open draft.
  useWSEvent("draft:turn_completed", (payload) => {
    const p = (payload ?? {}) as { draft_id?: string; task_id?: string };
    if (p.draft_id === draftId && (p.task_id === activeTaskId || activeTaskId === null)) {
      onTurnChange(null);
    }
  });

  const steps = useMemo(() => toSteps(messages, t), [messages, t]);

  if (activeTaskId === null) return null;

  return (
    <div className="border-t border-border bg-muted/30 px-3 py-2">
      <ThinkingSteps
        steps={steps}
        header={t(($) => $.turn.narration_title, { name: t(($) => $.turn.agent_name) })}
      />
      {/* The live tail: while the turn is open, show the working indicator
          below the streamed steps. */}
      <ThinkingIndicator
        label={t(($) => $.turn.working, { name: t(($) => $.turn.agent_name) })}
      />
    </div>
  );
}

type TFn = TFunction<"drafts">;

/**
 * Map the raw task:message stream into ThinkingSteps. The message `type` is a
 * server-driven enum; the switch has a `default` so a drifted/new type renders
 * a generic step instead of throwing (enum-drift rule). The newest message is
 * the "active" step; the rest are "complete".
 */
function toSteps(messages: TaskMessagePayload[], t: TFn): ThinkingStepItem[] {
  const ordered = [...messages].sort((a, b) => a.seq - b.seq);
  return ordered.map((m, i) => {
    const isLast = i === ordered.length - 1;
    let label: string;
    let description: string | undefined;
    switch (m.type) {
      case "thinking":
        label = m.content?.trim() || t(($) => $.turn.thinking);
        break;
      case "text":
        label = m.content?.trim() || t(($) => $.turn.thinking);
        break;
      case "tool_use":
        label = m.tool
          ? t(($) => $.turn.tool, { tool: m.tool })
          : t(($) => $.turn.tool_generic);
        description = summarizeInput(m.input);
        break;
      case "tool_result":
        label = m.tool
          ? t(($) => $.turn.tool, { tool: m.tool })
          : t(($) => $.turn.tool_generic);
        description = m.output?.trim() || undefined;
        break;
      case "error":
        label = m.content?.trim() || t(($) => $.turn.tool_generic);
        break;
      default:
        // Unknown/new server message type — downgrade to a generic step.
        label = m.content?.trim() || t(($) => $.turn.tool_generic);
        break;
    }
    return {
      id: String(m.seq),
      label,
      description,
      status: isLast ? "active" : "complete",
    };
  });
}

function summarizeInput(input: Record<string, unknown> | undefined): string | undefined {
  if (!input) return undefined;
  // Show the first string-valued arg as a one-line summary (e.g. the draft id
  // or quote a draft tool was called with). Best-effort — never throws.
  for (const v of Object.values(input)) {
    if (typeof v === "string" && v.trim() !== "") {
      return v.length > 80 ? v.slice(0, 77) + "…" : v;
    }
  }
  return undefined;
}
