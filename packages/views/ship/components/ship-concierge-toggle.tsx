"use client";

import { useMemo } from "react";
import { Bot } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { channelsListOptions } from "@multica/core/channels";
import { useShipConciergeDrawer } from "@multica/core/ship";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

/**
 * Header button that toggles the docked Ship Concierge drawer
 * (`ShipShell`). Replaces the old `Sheet`-based `ShipConciergePanel`
 * trigger — the drawer body now lives in the shell, so this is a pure
 * toggle that flips `useShipConciergeDrawer().open`.
 *
 * It still scans the channels list for the Concierge channel to surface
 * the unread badge; the badge is hidden when no Concierge is configured
 * or there's nothing unread.
 */
export function ShipConciergeToggle() {
  const { t } = useT("ship");
  const wsId = useWorkspaceId();
  const toggle = useShipConciergeDrawer((s) => s.toggle);

  const { data: channels = [] } = useQuery(channelsListOptions(wsId, true));
  const concierge = useMemo(
    () => (channels ?? []).find((c) => c.ambient_listener_agent_id !== null),
    [channels],
  );

  return (
    <Button
      variant="outline"
      size="sm"
      className="h-7 gap-1.5 text-xs"
      onClick={toggle}
      data-testid="ship-concierge-toggle"
    >
      <Bot className="size-3.5" />
      {t(($) => $.concierge_panel.trigger)}
      {concierge && concierge.unread_count > 0 && (
        <span className="ml-0.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[10px] font-medium text-primary-foreground">
          {concierge.unread_count}
        </span>
      )}
    </Button>
  );
}
