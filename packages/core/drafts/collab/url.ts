/**
 * Derive a draft's Yjs relay WS URL from the app's base WS URL.
 *
 * The base WS URL is the realtime endpoint (e.g. `ws://localhost:8080/ws` or
 * `wss://api.multica.app/ws`). The draft relay lives on the SAME host at
 * `/api/drafts/{id}/yjs`, so we swap the path while preserving scheme + host +
 * any port. Keeping this derivation in one place means the provider stays
 * host-agnostic and both apps feed it the same base they already use for `/ws`.
 */
export function draftYjsWsUrl(baseWsUrl: string, draftId: string): string {
  const url = new URL(baseWsUrl);
  url.search = "";
  url.hash = "";
  url.pathname = `/api/drafts/${encodeURIComponent(draftId)}/yjs`;
  return url.toString();
}
