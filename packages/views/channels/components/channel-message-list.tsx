"use client";

import { Fragment, useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { channelMessagesInfiniteOptions } from "@multica/core/channels";
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
  useMessageScroller,
} from "@multica/ui/components/ui/message-scroller";
import { ArrowDown, Loader2 } from "lucide-react";
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
  /**
   * Force the transcript to open at the live edge (latest message) instead of
   * anchoring to the unread divider. For chat-assistant surfaces like the Ship
   * Concierge drawer, where "catch up from the first unread" is the wrong
   * model — you want the latest reply, not to be scrolled up into history
   * (which also keeps the load-older sentinel firing). The unread divider is
   * still rendered; only the open-scroll anchor changes.
   */
  openAtBottom?: boolean;
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
  openAtBottom = false,
}: ChannelMessageListProps) {
  const { t } = useT("channels");
  const wsId = useWorkspaceId();
  const {
    data,
    isLoading,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  } = useInfiniteQuery(channelMessagesInfiniteOptions(channelId, enabled));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const todayLabel = t(($) => $.messages.date_today);
  const yesterdayLabel = t(($) => $.messages.date_yesterday);

  const memberById = new Map(members.map((m) => [m.user_id, m]));
  const agentById = new Map(agents.map((a) => [a.id, a]));

  // Pages are newest-first across pages (page 0 = newest) and within a page;
  // flatten then reverse so we render oldest at top, newest at bottom (chat
  // convention). `hasNextPage` here means "there is OLDER history to load".
  const messages = useMemo(
    () => (data ? data.pages.flatMap((p) => p.messages).reverse() : []),
    [data],
  );

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

  // Scroll behavior is owned by shadcn's MessageScroller (ROA-1160). Open
  // policy (#11): the first unread message is scrolled to the top via
  // OpenAtUnread below; with no unread we open at the live edge
  // (defaultScrollPosition="end"). Place-keeping on older-history prepend
  // (#12) is the Viewport's preserveScrollOnPrepend — no manual capture /
  // restore. The Provider is keyed on channelId so switching channels fully
  // resets scroll state.
  const anchorMessageId =
    openAtBottom || dividerBeforeIndex === null || !messages[dividerBeforeIndex]
      ? null
      : messages[dividerBeforeIndex].id;

  // ─── Load older history (#10) ───────────────────────────────────────────
  // A top sentinel + IntersectionObserver fetches the next OLDER page as the
  // reader nears the top; preserveScrollOnPrepend keeps their place when the
  // rows render above. Live fetch flags are read through a ref so the observer
  // is set up once per channel, not re-subscribed on every fetch-state flip.
  const viewportRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement | null>(null);
  const loadStateRef = useRef({ hasNextPage, isFetchingNextPage });
  loadStateRef.current = { hasNextPage, isFetchingNextPage };
  // The viewport only mounts once the list renders (the loading/empty states
  // return earlier), so re-run when it becomes available.
  const listMounted = !isLoading && messages.length > 0;
  useEffect(() => {
    const root = viewportRef.current;
    const sentinel = topSentinelRef.current;
    if (!root || !sentinel || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(
      (entries) => {
        if (!entries.some((e) => e.isIntersecting)) return;
        const { hasNextPage: more, isFetchingNextPage: busy } = loadStateRef.current;
        if (more && !busy) void fetchNextPage();
      },
      // Preload ~1 viewport before the absolute top so the join is seamless.
      { root, rootMargin: "400px 0px 0px 0px", threshold: 0 },
    );
    io.observe(sentinel);
    return () => io.disconnect();
  }, [channelId, listMounted, fetchNextPage]);

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
    // Keyed on channelId so switching channels remounts the scroller with a
    // clean state (replaces the engine's resetKey).
    <MessageScrollerProvider key={channelId} autoScroll defaultScrollPosition="end">
      <MessageScroller className="min-h-0 flex-1">
        <MessageScrollerViewport ref={viewportRef} className="py-1">
          {/* gap-0: channel rows are contiguous (MessageRow owns its padding). */}
          <MessageScrollerContent className="gap-0">
            <OpenAtUnread anchorMessageId={anchorMessageId} />
            {/* Top sentinel: nearing it loads the older page (#10). */}
            <div ref={topSentinelRef} aria-hidden className="h-px w-full" />
            {hasNextPage ? (
              <div className="flex items-center justify-center py-2 text-xs text-muted-foreground">
                {isFetchingNextPage ? (
                  <span className="flex items-center gap-1.5">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    {t(($) => $.messages.loading_older)}
                  </span>
                ) : null}
              </div>
            ) : null}
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

              // Show a date divider when the calendar day changes.
              const showDateDivider =
                !prev ||
                new Date(m.created_at).toDateString() !==
                  new Date(prev.created_at).toDateString();

              return (
                <Fragment key={m.id}>
                  {showDateDivider ? (
                    <MessageScrollerItem>
                      <DateDivider
                        label={formatDateLabel(m.created_at, todayLabel, yesterdayLabel)}
                      />
                    </MessageScrollerItem>
                  ) : null}
                  {dividerBeforeIndex === i ? (
                    <MessageScrollerItem>
                      <UnreadDivider
                        ariaLabel={t(($) => $.messages.new_messages_aria)}
                        label={t(($) => $.messages.new_messages)}
                      />
                    </MessageScrollerItem>
                  ) : null}
                  <MessageScrollerItem messageId={m.id}>
                    <MessageRow
                      message={m}
                      channelId={channelId}
                      member={
                        m.author_type === "member" ? memberById.get(m.author_id) : undefined
                      }
                      agent={m.author_type === "agent" ? agentById.get(m.author_id) : undefined}
                      onOpenThread={onOpenThread}
                      isGroupContinuation={isContinuation}
                    />
                  </MessageScrollerItem>
                </Fragment>
              );
            })}
          </MessageScrollerContent>
        </MessageScrollerViewport>
        <MessageScrollerButton direction="end">
          <ArrowDown />
          <span className="sr-only">{t(($) => $.messages.new_messages_below)}</span>
        </MessageScrollerButton>
      </MessageScroller>
    </MessageScrollerProvider>
  );
}

/**
 * Open policy (#11): scroll the first unread message to the top of the
 * viewport. Lives inside the Provider so it can use the scroll context; a
 * layout effect runs before paint to avoid a flash from the default
 * "end" position. No-op when there's nothing unread.
 */
function OpenAtUnread({ anchorMessageId }: { anchorMessageId: string | null }) {
  const { scrollToMessage } = useMessageScroller();
  useLayoutEffect(() => {
    if (anchorMessageId) scrollToMessage(anchorMessageId, { align: "start" });
  }, [anchorMessageId, scrollToMessage]);
  return null;
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
