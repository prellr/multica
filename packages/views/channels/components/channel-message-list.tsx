"use client";

import { Fragment, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { channelMessagesOptions } from "@multica/core/channels";
import { useTranscriptScroll } from "@multica/ui/hooks/use-transcript-scroll";
import { ChevronDown } from "lucide-react";
import { MessageRow } from "./message-row";
import { useT } from "../../i18n";

// Two messages are part of the same "group" — same author, in close
// succession — when the second one came in within this window. Slack
// uses ~5 minutes; tighter feels twitchy on slow conversations and
// looser starts merging unrelated bursts. 5 minutes is the right knob.
const GROUP_CONTINUATION_MS = 5 * 60 * 1000;

function formatDateLabel(iso: string, todayStr: string, yesterdayStr: string): string {
  const d = new Date(iso);
  const now = new Date();
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (d.toDateString() === now.toDateString()) return todayStr;
  if (d.toDateString() === yesterday.toDateString()) return yesterdayStr;
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleDateString(undefined, {
    weekday: "long",
    month: "long",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
  });
}

interface ChannelMessageListProps {
  channelId: string;
  enabled: boolean;
  onOpenThread?: (parentMessageId: string) => void;
  /**
   * Frozen-on-mount last_read_message_id for the active channel. The list
   * renders an "unread" divider before the first message newer than this
   * cursor, and on the first render with messages it scrolls that divider
   * into view. Null = no divider (everything has been read OR there are
   * unread messages but we have no anchor to bisect on).
   */
  initialUnreadCursor?: string | null;
}

/**
 * ChannelMessageList fetches and renders a channel's most recent messages.
 *
 * Phase 1: simple .map() of the latest 50 (server returns newest-first; we
 * reverse for display). Auto-scrolls to the bottom on new messages —
 * detected by tracking the previous message-count rather than using a
 * MutationObserver, since the only mutation we care about is `length`.
 *
 * Phase 5+: switch to a virtualized list (TanStack Virtual) if channels
 * routinely exceed ~500 visible messages.
 */
export function ChannelMessageList({
  channelId,
  enabled,
  onOpenThread,
  initialUnreadCursor,
}: ChannelMessageListProps) {
  const { t } = useT("channels");
  const wsId = useWorkspaceId();
  const { data: rawMessages = [], isLoading } = useQuery(
    channelMessagesOptions(channelId, enabled),
  );
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const todayLabel = t(($) => $.messages.date_today);
  const yesterdayLabel = t(($) => $.messages.date_yesterday);

  const memberById = new Map(members.map((m) => [m.user_id, m]));
  const agentById = new Map(agents.map((a) => [a.id, a]));

  // Server returns newest-first; reverse so we render oldest at top, newest
  // at bottom (chat convention).
  const messages = [...rawMessages].reverse();

  // Locate the divider position: we render the "New messages" divider
  // immediately BEFORE the first message that's newer than the cursor.
  // If the cursor isn't found (older message that's been retention-trimmed,
  // or this is a brand-new channel for the user) and there are messages,
  // the divider goes at the very top so every visible message reads as
  // unread. If the cursor matches the latest message we render no divider.
  const dividerBeforeIndex = useMemo<number | null>(() => {
    if (initialUnreadCursor === undefined) return null; // not yet hydrated
    if (initialUnreadCursor === null) {
      // Never read at all. Show divider at top only if there's actually
      // content; otherwise nothing to anchor on.
      return messages.length > 0 ? 0 : null;
    }
    const idx = messages.findIndex((m) => m.id === initialUnreadCursor);
    if (idx === -1) {
      // Cursor older than the loaded window — treat everything as unread.
      return 0;
    }
    if (idx === messages.length - 1) {
      // Cursor is the latest message; nothing unread.
      return null;
    }
    return idx + 1;
  }, [messages, initialUnreadCursor]);

  // Scroll behavior is owned by the shared transcript engine (ROA-1135):
  // intent-aware FOLLOWING/READING (text-selection, keyboard, wheel — not
  // just scrollTop), a live-edge sentinel, and jump-to-latest. Open policy
  // (#11): anchor to the first unread message when there's an unread divider,
  // else open at the live edge. resetKey=channelId re-anchors + resets state
  // on every channel switch.
  const anchorMessageId =
    dividerBeforeIndex !== null && messages[dividerBeforeIndex]
      ? messages[dividerBeforeIndex].id
      : null;
  const { containerRef, sentinelRef, hasNewBelow, jumpToLatest } = useTranscriptScroll({
    initialAnchor: anchorMessageId ? { messageId: anchorMessageId } : "bottom",
    resetKey: channelId,
  });

  if (isLoading && messages.length === 0) {
    return (
      <div className="flex-1 overflow-y-auto px-4 py-6 text-sm text-muted-foreground">
        {t(($) => $.messages.loading)}
      </div>
    );
  }
  if (messages.length === 0) {
    return (
      <div className="flex-1 overflow-y-auto px-4 py-12 text-center text-sm text-muted-foreground">
        {t(($) => $.messages.empty)}
      </div>
    );
  }
  return (
    <div className="relative min-h-0 flex-1">
      <div ref={containerRef} className="h-full overflow-y-auto py-1">
        {messages.map((m, i) => {
          const prev = i > 0 ? messages[i - 1] : null;
          // The unread divider visually breaks a group, so a continuation
          // immediately after the divider should re-introduce the avatar
          // header — feels weird otherwise. Hence the `dividerBeforeIndex`
          // check inside the predicate.
          const isContinuation =
            !!prev &&
            dividerBeforeIndex !== i &&
            prev.author_type === m.author_type &&
            prev.author_id === m.author_id &&
            new Date(m.created_at).getTime() - new Date(prev.created_at).getTime() <
              GROUP_CONTINUATION_MS;

          // Show a date divider when the calendar day changes between messages.
          const showDateDivider =
            !prev ||
            new Date(m.created_at).toDateString() !== new Date(prev.created_at).toDateString();

          return (
            <Fragment key={m.id}>
              {showDateDivider ? (
                <DateDivider label={formatDateLabel(m.created_at, todayLabel, yesterdayLabel)} />
              ) : null}
              {dividerBeforeIndex === i ? (
                <UnreadDivider
                  ariaLabel={t(($) => $.messages.new_messages_aria)}
                  label={t(($) => $.messages.new_messages)}
                />
              ) : null}
              <MessageRow
                message={m}
                channelId={channelId}
                member={m.author_type === "member" ? memberById.get(m.author_id) : undefined}
                agent={m.author_type === "agent" ? agentById.get(m.author_id) : undefined}
                onOpenThread={onOpenThread}
                isGroupContinuation={isContinuation}
              />
            </Fragment>
          );
        })}
        {/* Live-edge sentinel for the engine's IntersectionObserver. */}
        <div ref={sentinelRef} aria-hidden className="h-px w-full" />
      </div>
      {hasNewBelow && (
        <div className="pointer-events-none absolute bottom-2 left-0 right-0 flex justify-center">
          <button
            type="button"
            onClick={jumpToLatest}
            className="pointer-events-auto flex items-center gap-1.5 rounded-full bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground shadow-md hover:bg-primary/90"
          >
            <ChevronDown className="h-3.5 w-3.5" />
            {t(($) => $.messages.new_messages_below)}
          </button>
        </div>
      )}
    </div>
  );
}

const DateDivider = ({ label }: { label: string }) => (
  <div
    className="my-2 flex items-center gap-3 px-4 text-[11px] font-semibold text-muted-foreground"
    role="separator"
    aria-label={label}
  >
    <span className="h-px flex-1 bg-border" />
    <span>{label}</span>
    <span className="h-px flex-1 bg-border" />
  </div>
);

const UnreadDivider = ({
  ref,
  ariaLabel,
  label,
}: {
  ref?: React.Ref<HTMLDivElement>;
  ariaLabel: string;
  label: string;
}) => (
  <div
    ref={ref}
    className="my-2 flex items-center gap-3 px-4 text-[11px] font-semibold uppercase tracking-wide text-primary"
    aria-label={ariaLabel}
  >
    <span className="h-px flex-1 bg-primary/40" />
    <span>{label}</span>
    <span className="h-px flex-1 bg-primary/40" />
  </div>
);
