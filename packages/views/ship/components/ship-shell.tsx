"use client";

import { useEffect, useMemo } from "react";
import { Bot, X } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useDefaultLayout, usePanelRef } from "react-resizable-panels";
import { channelsListOptions } from "@multica/core/channels";
import { useShipConciergeDrawer } from "@multica/core/ship";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { ChannelMessageList } from "../../channels/components/channel-message-list";
import { ChannelComposer } from "../../channels/components/channel-composer";
import { useT } from "../../i18n";
import { ConciergeEmptyState } from "./ship-concierge-panel";

/**
 * Ship Shell — wraps every Ship view (board + release detail) with a
 * persistent right-side Concierge drawer. Mounted once by the web Ship
 * layout so the drawer survives navigation between `/ship` and
 * `/ship/release/[id]`; only the `{children}` content panel swaps.
 *
 * Replaces the old per-page `Sheet`-based `ShipConciergePanel`: instead
 * of a slide-over that unmounts the channel chat (and resets scroll /
 * unread state) on every close, the drawer is a docked resizable panel
 * whose collapsed width is 0. The conversation stays mounted regardless
 * of open state — collapsing the panel hides it, expanding restores it.
 *
 * Open/closed state is persisted via `useShipConciergeDrawer` and synced
 * to the panel imperatively (store → panel via .expand()/.collapse();
 * panel → store via onResize). The layout split itself persists under
 * `multica_ship_concierge_layout`.
 */
export function ShipShell({ children }: { children: React.ReactNode }) {
  const { t } = useT("ship");
  const wsId = useWorkspaceId();
  const open = useShipConciergeDrawer((s) => s.open);
  const setOpen = useShipConciergeDrawer((s) => s.setOpen);

  // Channels list is cached at `staleTime: Infinity`; this never adds a
  // round-trip on Ship navigation. WS events invalidate it when the
  // operator changes the ambient-listener designation.
  const { data: channels = [] } = useQuery(channelsListOptions(wsId, true));
  const concierge = useMemo(
    () => (channels ?? []).find((c) => c.ambient_listener_agent_id !== null),
    [channels],
  );

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_ship_concierge_layout",
  });
  const panelRef = usePanelRef();

  // Store → panel sync. The store is the source of truth (it drives the
  // header toggle and the "Ask Pilot" chip); reflect its changes onto the
  // imperative panel handle. Guarded by isCollapsed() so we don't fight
  // the user's in-progress drag.
  useEffect(() => {
    const panel = panelRef.current;
    if (!panel) return;
    if (open && panel.isCollapsed()) {
      panel.expand();
    } else if (!open && !panel.isCollapsed()) {
      panel.collapse();
    }
  }, [open, panelRef]);

  return (
    <ResizablePanelGroup
      orientation="horizontal"
      className="h-full min-h-0"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel id="content" minSize="45%">
        {children}
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel
        id="concierge"
        defaultSize={open ? 420 : 0}
        minSize={320}
        maxSize={640}
        collapsible
        groupResizeBehavior="preserve-pixel-size"
        panelRef={panelRef}
        // Panel → store sync. Dragging the handle to 0 collapses the
        // drawer; reflect that back so the header toggle stays in step.
        onResize={(size) => setOpen(size.inPixels > 0)}
      >
        <div className="flex h-full min-h-0 flex-col border-l">
          <div className="flex items-center gap-2 border-b px-4 py-3">
            <Bot className="size-4 text-muted-foreground" />
            <span className="flex-1 truncate text-sm font-medium">
              {concierge
                ? concierge.display_name || concierge.name
                : t(($) => $.concierge_setup_dialog.default_display_name)}
            </span>
            <Button
              variant="ghost"
              size="icon"
              className="size-7"
              onClick={() => setOpen(false)}
              aria-label={t(($) => $.concierge_panel.collapse)}
            >
              <X className="size-4" aria-hidden />
            </Button>
          </div>

          {concierge ? (
            <div className="flex min-h-0 flex-1 flex-col">
              <ChannelMessageList channelId={concierge.id} enabled={open} />
              <ChannelComposer channel={concierge} />
            </div>
          ) : (
            <ConciergeEmptyState />
          )}
        </div>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}
