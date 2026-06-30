"use client";

import { Bot } from "lucide-react";
import { useActorName } from "@multica/core/workspace/hooks";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { cn } from "@multica/ui/lib/utils";
import { Markdown } from "../../common/markdown";
import { useT, useTimeAgo } from "../../i18n";

/**
 * The shared "who said what, when" message row for the Drafts surfaces. Both an
 * annotation thread message ({@link DraftAnnotationMessage}) and a conversation-
 * rail message ({@link DraftMessage}) satisfy this prop shape, so this single
 * row renders both (No-Duplication Rule) — extracted from the annotation
 * thread's former inline `ThreadMessage`.
 *
 * Renders the sender (Aye vs the authoring member), a relative timestamp, and
 * the body as markdown — so a structured multi-point reply reads as prose rather
 * than a raw wall of text.
 *
 * Aye (author_type "agent") is styled distinctly: a robot affordance and a
 * primary-accent avatar/name, mirroring how agent assignees are set apart
 * elsewhere. We avoid resolving the agent by id (draft messages carry no
 * author_user_id for agents) and label Aye from the shared `turn.agent_name`
 * string. An unknown author_type falls through to the member branch (enum-drift
 * rule — render generically, never crash).
 */
export interface MessageRowProps {
  /** Open author-type enum: "user" | "agent" | unknown. */
  author_type: string;
  /** Empty string when author_type !== "user" (e.g. an agent author). */
  author_user_id: string;
  body: string;
  /** ISO timestamp. */
  created_at: string;
}

export function MessageRow({ author_type, author_user_id, body, created_at }: MessageRowProps) {
  const { t } = useT("drafts");
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();

  const isAgent = author_type === "agent";
  // Members are resolved by id; "user" is the draft author-type spelling of the
  // workspace "member" actor. Fall back to a neutral "you" label when the member
  // isn't in the cache (own message, just-invited author, drift).
  const resolvedName = isAgent
    ? t(($) => $.turn.agent_name)
    : (() => {
        const name = getActorName("member", author_user_id);
        return name === "Unknown" ? t(($) => $.annotations.you) : name;
      })();

  return (
    <div className="flex gap-2.5">
      {isAgent ? (
        <ActorAvatar
          name={resolvedName}
          initials="A"
          isAgent
          size={24}
          className="mt-0.5 bg-primary/10 text-primary"
        />
      ) : (
        <ActorAvatar
          name={resolvedName}
          initials={initialsOf(resolvedName)}
          size={24}
          className="mt-0.5"
        />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-1.5">
          <span
            className={cn(
              "text-xs font-medium",
              isAgent ? "text-primary" : "text-foreground",
            )}
          >
            {resolvedName}
          </span>
          {isAgent && <Bot className="h-3 w-3 shrink-0 text-primary" aria-hidden />}
          <span className="text-[11px] text-muted-foreground">{timeAgo(created_at)}</span>
        </div>
        {/* Reuse the app's shared markdown renderer in its chat-message mode so
            bold/lists/headings/code render with tasteful, token-driven
            typography — consistent with how channel + chat messages render. */}
        <Markdown mode="minimal" className="mt-1 text-sm leading-relaxed text-foreground">
          {body}
        </Markdown>
      </div>
    </div>
  );
}

/** Up-to-two-letter initials from a display name, for the member avatar. */
function initialsOf(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);
}
