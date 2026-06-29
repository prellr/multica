"use client";

import { useEffect, useRef, useState } from "react";
import type * as Y from "yjs";
import type { Awareness } from "y-protocols/awareness";
import { useWS } from "../../realtime/provider";
import { createLogger } from "../../logger";
import { DraftYjsProvider } from "./provider";
import { draftYjsWsUrl } from "./url";

const log = createLogger("draft-yjs");

/**
 * Headless hook that binds a draft's Y.Doc + awareness to the networked relay
 * (slice 3a). It reads the base WS URL and auth mode from the WS context, builds
 * a DraftYjsProvider for `/api/drafts/{draftId}/yjs`, connects on mount, and
 * tears it down on unmount or when the draft changes.
 *
 * Lives in core (headless): no editor schema, no DOM. The view layer owns the
 * Y.Doc and the editor; this only owns the provider lifecycle. Returns whether
 * the provider is currently connected so the view can surface presence/offline
 * state if it wants.
 */
export function useDraftYjs(
  draftId: string,
  doc: Y.Doc | null,
  awareness: Awareness | null,
): { connected: boolean } {
  const { wsUrl, cookieAuth, getToken } = useWS();
  const [connected, setConnected] = useState(false);
  const providerRef = useRef<DraftYjsProvider | null>(null);

  useEffect(() => {
    if (!doc || !awareness) return;

    const token = cookieAuth ? null : getToken();
    // Token mode with no token = not authenticated yet; skip until we have one.
    if (!cookieAuth && !token) return;

    const provider = new DraftYjsProvider({
      url: draftYjsWsUrl(wsUrl, draftId),
      doc,
      awareness,
      token,
      log: (level, msg, ...args) => log[level](msg, ...args),
    });
    providerRef.current = provider;

    const onStatus = (isConnected: boolean) => setConnected(isConnected);
    provider.onStatus.add(onStatus);
    provider.connect();

    return () => {
      provider.onStatus.delete(onStatus);
      provider.destroy();
      providerRef.current = null;
      setConnected(false);
    };
    // wsUrl / cookieAuth are app-boot constants; getToken is stable from the WS
    // context. Re-running only on draftId / doc / awareness identity is correct.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draftId, doc, awareness]);

  return { connected };
}
