/**
 * Headless Yjs primitives for the Drafts co-editing floor (slice 3a spike).
 *
 * This module is PURE Yjs: no React, no DOM, no ProseMirror, no editor schema.
 * It owns the CRDT document, the awareness channel, and the update transport —
 * everything that is independent of how the document is rendered. The editor
 * binding (Collaboration extension, schema-aware seeding) lives in
 * `packages/views/drafts/annotations`, because that needs the Tiptap schema and
 * therefore cannot live under the `packages/core` boundary (zero UI libraries).
 *
 * The transport here is an *in-process* relay: it wires two `Y.Doc`s together by
 * forwarding each side's incremental updates to the other via `Y.applyUpdate`.
 * This is the spike's stand-in for the real binary-WS backend (slice 3a proper):
 * the merge semantics it exercises are identical to a networked provider, since
 * a y-websocket provider ultimately does the same `update` → `applyUpdate` dance.
 * Building the Go `/api/drafts/{id}/yjs` endpoint is out of scope for the spike.
 */

import * as Y from "yjs";
import { Awareness } from "y-protocols/awareness";

/** The Y.js fragment name the Collaboration extension binds to by default. */
export const DRAFT_YJS_FRAGMENT = "default";

/**
 * A draft's collaborative document: the `Y.Doc` plus its awareness channel.
 * Awareness carries the ephemeral per-peer cursor/selection state that the
 * CollaborationCaret extension renders; it is never persisted.
 */
export interface DraftYContext {
  doc: Y.Doc;
  awareness: Awareness;
}

/**
 * Create a fresh draft `Y.Doc` and its awareness channel. The doc starts empty;
 * seed it from a draft body via the view-layer `seedYDocFromMarkdown` before (or
 * just after) mounting the editor — see that helper for the single-seed rule.
 */
export function createDraftYContext(): DraftYContext {
  const doc = new Y.Doc();
  const awareness = new Awareness(doc);
  return { doc, awareness };
}

/** Tear down a context's awareness channel and destroy its doc. */
export function destroyDraftYContext(ctx: DraftYContext): void {
  ctx.awareness.destroy();
  ctx.doc.destroy();
}

/**
 * A live, in-process two-way update relay between two `Y.Doc`s. Calling the
 * returned function detaches both listeners.
 */
export type DocRelay = () => void;

/**
 * Wire two `Y.Doc`s so each one's incremental updates are applied to the other,
 * exactly as a real provider would over the wire. This is the spike's transport:
 * it lets a test drive a "remote" edit on `b` and observe it merge into `a`
 * (and vice-versa) without standing up a WS server.
 *
 * The `origin` tag on `applyUpdate` is what marks an incoming change as REMOTE:
 * downstream (the editor's ySyncPlugin) uses the transaction origin to tell a
 * local keystroke apart from a relayed update. We tag with the *source* doc so
 * the relay never echoes an update back to where it came from (which would loop).
 */
export function connectDocs(a: Y.Doc, b: Y.Doc): DocRelay {
  const aToB = (update: Uint8Array, origin: unknown) => {
    // Don't echo a change that originated from `b` back into `b`.
    if (origin === b) return;
    Y.applyUpdate(b, update, a);
  };
  const bToA = (update: Uint8Array, origin: unknown) => {
    if (origin === a) return;
    Y.applyUpdate(a, update, b);
  };
  a.on("update", aToB);
  b.on("update", bToA);

  // Bring both sides to a common state immediately (initial handshake), so a doc
  // that was seeded before connecting propagates its content to the peer.
  const sv = Y.encodeStateVector(b);
  const diff = Y.encodeStateAsUpdate(a, sv);
  Y.applyUpdate(b, diff, a);
  const sv2 = Y.encodeStateVector(a);
  const diff2 = Y.encodeStateAsUpdate(b, sv2);
  Y.applyUpdate(a, diff2, b);

  return () => {
    a.off("update", aToB);
    b.off("update", bToA);
  };
}
