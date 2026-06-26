import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

// Workspace scoping note: `wsId` is part of the queryKey for cache isolation
// only. The actual workspace context is supplied by ApiClient's
// X-Workspace-Slug header set by the [workspaceSlug] layout.

export const channelKeys = {
  all: (wsId: string) => ["channels", wsId] as const,
  list: (wsId: string) => [...channelKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) => [...channelKeys.all(wsId), "detail", id] as const,
  members: (channelId: string) => ["channels", "members", channelId] as const,
  messages: (channelId: string) => ["channels", "messages", channelId] as const,
  // Phase 4 — per-parent thread payload (parent + replies, batch-hydrated
  // with reactions). Keyed by message id so the panel can be opened
  // independently of which channel the user is currently viewing.
  thread: (messageId: string) => ["channels", "thread", messageId] as const,
  search: (wsId: string, q: string, channelId: string | null) =>
    ["channels", "search", wsId, q, channelId ?? "all"] as const,
};

export function channelsListOptions(wsId: string, enabled: boolean) {
  return queryOptions({
    queryKey: channelKeys.list(wsId),
    queryFn: () => api.listChannels(),
    staleTime: Infinity,
    // Skip the request entirely when the workspace flag is off — the
    // backend would 404 anyway.
    enabled,
  });
}

export function channelDetailOptions(wsId: string, channelId: string, enabled: boolean) {
  return queryOptions({
    queryKey: channelKeys.detail(wsId, channelId),
    queryFn: () => api.getChannel(channelId),
    staleTime: Infinity,
    enabled: enabled && !!channelId,
  });
}

export function channelMembersOptions(channelId: string, enabled: boolean) {
  return queryOptions({
    queryKey: channelKeys.members(channelId),
    queryFn: () => api.listChannelMembers(channelId),
    staleTime: Infinity,
    enabled: enabled && !!channelId,
  });
}

export function channelMessageThreadOptions(channelId: string, messageId: string, enabled: boolean) {
  return queryOptions({
    queryKey: channelKeys.thread(messageId),
    queryFn: () => api.getChannelMessageThread(channelId, messageId),
    // Belt-and-suspenders against missed WS invalidations: opening the
    // panel for an existing thread should always refetch. Reply-bearing
    // events that don't carry parent_message_id (e.g. older agent
    // publishers, or any new event we add later) would otherwise leave
    // the panel stuck at its first-fetch state — the bug surfaced as
    // "1 reply" badge with "No replies yet." in the panel.
    staleTime: 0,
    enabled: enabled && !!channelId && !!messageId,
  });
}

export function channelSearchOptions(
  wsId: string,
  q: string,
  channelId: string | null,
  enabled: boolean,
) {
  return queryOptions({
    queryKey: channelKeys.search(wsId, q, channelId),
    queryFn: () =>
      api.searchChannelMessages({
        q,
        ...(channelId ? { channelId } : {}),
        limit: 50,
      }),
    // Search results aren't useful to retain across query changes; the
    // user typing a new term should kick off a fresh fetch rather than
    // hand back a stale page.
    staleTime: 0,
    enabled: enabled && q.trim().length > 0,
  });
}

/** Page size for the channel timeline (newest page + each older page). */
export const CHANNEL_PAGE_SIZE = 50;

/**
 * Infinite timeline for a channel. Page 0 is the newest {@link CHANNEL_PAGE_SIZE}
 * messages; each `fetchNextPage()` loads the OLDER slice before it via the
 * `before` cursor (`next_cursor` from the previous page). Messages are
 * newest-first both across pages (page 0 newest) and within a page — the view
 * flattens + reverses for display and prepends older pages above with the
 * engine's scroll compensation so loading history never moves the reader.
 *
 * Keyed on `channelKeys.messages(channelId)` — the SAME key the WS
 * `channel:message` invalidator targets, so a new message refetches the
 * loaded pages (page 0 = newest) and surfaces without extra wiring.
 *
 * 30s staleTime + refetchOnWindowFocus backstop WS gaps (a tab returning from
 * sleep refetches instead of sitting on a stale timeline); the global default
 * is `staleTime: Infinity` since most workspace data is small + WS-driven, but
 * channel messages are the noisy ones where missed events are visible bugs.
 */
export function channelMessagesInfiniteOptions(channelId: string, enabled: boolean) {
  return infiniteQueryOptions({
    queryKey: channelKeys.messages(channelId),
    queryFn: ({ pageParam }) =>
      api.listChannelMessagesPage(channelId, {
        ...(pageParam ? { before: pageParam } : {}),
        limit: CHANNEL_PAGE_SIZE,
      }),
    initialPageParam: null as string | null,
    // "next" page = the older slice. Stop once history is exhausted.
    getNextPageParam: (lastPage) =>
      lastPage.has_more ? lastPage.next_cursor : undefined,
    staleTime: 30_000,
    refetchOnWindowFocus: true,
    enabled: enabled && !!channelId,
  });
}
