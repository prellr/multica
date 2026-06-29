/**
 * The real (networked) Yjs provider for the Drafts co-editing floor (slice 3a).
 *
 * It connects a draft's `Y.Doc` + awareness channel to the Go binary-WS relay at
 * `/api/drafts/{id}/yjs`, speaking the de-facto y-websocket envelope the server
 * relays (see server/internal/draftyjs/protocol.go). This is the production
 * transport that replaces the in-process `connectDocs` relay (which stays as the
 * unit-test transport).
 *
 * Headless / platform-agnostic: the global `WebSocket` is the only platform
 * primitive used, and it exists in both web and Electron renderer. No React, no
 * DOM, no editor schema — that wiring lives in `packages/views`.
 *
 * Wire framing (each frame is one binary WebSocket message):
 *
 *   [ messageType: varUint ][ payload... ]
 *
 *   messageSync      (0) → payload is a y-protocols/sync sub-message
 *   messageAwareness (1) → payload is a y-protocols/awareness update
 *
 * The server is a DUMB RELAY: it never decodes the CRDT. So the client owns the
 * whole sync protocol. On open the client:
 *   1. (token mode) sends a first-message auth frame, identical to WSClient;
 *   2. sends SyncStep1 (its state vector) so any peer can diff against it;
 *   3. sends its full local state as an Update frame, so offline edits made
 *      before connecting (or a reconnect carrying unsynced local changes)
 *      propagate to the server log and peers — the server appends it, peers
 *      merge it idempotently.
 * The server replays its persisted log to the joining client as a sequence of
 * Update frames; the client applies them and converges.
 *
 * Reconnect: on an unexpected close the provider retries with backoff and
 * re-runs the open handshake. Because the server replays the log on every
 * connect and Yjs merges idempotently, reconnect is just "open again".
 */

import * as Y from "yjs";
import { Awareness, applyAwarenessUpdate, encodeAwarenessUpdate, removeAwarenessStates } from "y-protocols/awareness";
import * as syncProtocol from "y-protocols/sync";
import * as encoding from "lib0/encoding";
import * as decoding from "lib0/decoding";

/** Top-level envelope types — MUST match server/internal/draftyjs/protocol.go. */
const messageSync = 0;
const messageAwareness = 1;

/** Origin tag for transactions applied from the network, so the editor and the
 *  provider can tell a remote update apart from a local keystroke. */
export const REMOTE_ORIGIN = "draft-yjs-remote";

export interface DraftYjsProviderOptions {
  /** The full WS URL for this draft's relay, e.g.
   *  `ws://host:8080/api/drafts/<id>/yjs`. The caller derives it from the app's
   *  base WS URL — the provider stays host-agnostic. */
  url: string;
  doc: Y.Doc;
  awareness: Awareness;
  /** Token for first-message auth. Omit in cookie mode (the HttpOnly session
   *  cookie rides the upgrade automatically, as with WSClient). */
  token?: string | null;
  /** Reconnect backoff base in ms (default 1000). Capped at 30s. */
  reconnectBaseMs?: number;
  /** Injectable for tests; defaults to the global WebSocket. */
  WebSocketImpl?: typeof WebSocket;
  /** Optional structured logger; defaults to a no-op. */
  log?: (level: "debug" | "info" | "warn", msg: string, ...args: unknown[]) => void;
}

const MAX_BACKOFF_MS = 30_000;

/**
 * A binary WebSocket provider binding one draft's Y.Doc + awareness to the relay.
 * Construct, then call `connect()`; call `destroy()` to tear down.
 */
export class DraftYjsProvider {
  readonly doc: Y.Doc;
  readonly awareness: Awareness;
  private readonly url: string;
  private readonly token: string | null;
  private readonly WS: typeof WebSocket;
  private readonly reconnectBaseMs: number;
  private readonly log: NonNullable<DraftYjsProviderOptions["log"]>;

  private ws: WebSocket | null = null;
  private synced = false;
  private destroyed = false;
  private retries = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  /** Fires once per (re)connect after the initial sync handshake is sent. */
  readonly onStatus = new Set<(connected: boolean) => void>();

  constructor(opts: DraftYjsProviderOptions) {
    this.doc = opts.doc;
    this.awareness = opts.awareness;
    this.url = opts.url;
    this.token = opts.token ?? null;
    this.WS = opts.WebSocketImpl ?? WebSocket;
    this.reconnectBaseMs = opts.reconnectBaseMs ?? 1000;
    this.log = opts.log ?? (() => {});

    // Local doc updates → broadcast as a sync Update frame. Skip updates whose
    // origin is the network (we just applied those; re-broadcasting loops).
    this.doc.on("update", this.handleDocUpdate);
    // Local awareness changes → broadcast an awareness frame.
    this.awareness.on("update", this.handleAwarenessUpdate);
  }

  get isSynced(): boolean {
    return this.synced;
  }

  connect(): void {
    if (this.destroyed) return;
    this.openSocket();
  }

  private openSocket(): void {
    if (this.ws) return;
    let ws: WebSocket;
    try {
      ws = new this.WS(this.url);
    } catch (err) {
      this.log("warn", "draft-yjs: socket construction failed", err);
      this.scheduleReconnect();
      return;
    }
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onopen = () => {
      // Token mode: first frame is the JSON auth envelope, exactly as WSClient
      // sends it. Cookie mode: nothing to send, the upgrade already carried the
      // cookie.
      if (this.token) {
        ws.send(JSON.stringify({ type: "auth", payload: { token: this.token } }));
      }
      this.startSync(ws);
    };

    ws.onmessage = (event) => {
      // Binary frames are the protocol; a text frame is an auth/error payload.
      if (typeof event.data === "string") {
        this.log("debug", "draft-yjs: text frame", event.data);
        return;
      }
      this.handleMessage(new Uint8Array(event.data as ArrayBuffer));
    };

    ws.onclose = () => {
      this.ws = null;
      this.synced = false;
      this.emitStatus(false);
      // Clear remote awareness states: peers we learned about over this socket
      // are no longer known to be present.
      const states = Array.from(this.awareness.getStates().keys()).filter(
        (id) => id !== this.doc.clientID,
      );
      removeAwarenessStates(this.awareness, states, this);
      if (!this.destroyed) this.scheduleReconnect();
    };

    ws.onerror = () => {
      // onclose follows; reconnect is scheduled there. Just log.
      this.log("debug", "draft-yjs: socket error");
    };
  }

  /** Send the open-time sync handshake: SyncStep1 + a full-state Update. */
  private startSync(ws: WebSocket): void {
    this.retries = 0;

    // SyncStep1 — our state vector, so a peer can compute what we're missing.
    const step1 = encoding.createEncoder();
    encoding.writeVarUint(step1, messageSync);
    syncProtocol.writeSyncStep1(step1, this.doc);
    ws.send(encoding.toUint8Array(step1));

    // Full local state as an Update — pushes any pre-connect / offline edits to
    // the server log and peers. Idempotent everywhere.
    const stateUpdate = Y.encodeStateAsUpdate(this.doc);
    const updateFrame = encoding.createEncoder();
    encoding.writeVarUint(updateFrame, messageSync);
    syncProtocol.writeUpdate(updateFrame, stateUpdate);
    ws.send(encoding.toUint8Array(updateFrame));

    // Announce our local awareness state so peers render our cursor.
    const localState = this.awareness.getLocalState();
    if (localState !== null) {
      this.broadcastAwareness([this.doc.clientID]);
    }

    this.synced = true;
    this.emitStatus(true);
  }

  private handleMessage(data: Uint8Array): void {
    const decoder = decoding.createDecoder(data);
    const messageType = decoding.readVarUint(decoder);
    switch (messageType) {
      case messageSync: {
        // Peek the sync sub-type before readSyncMessage consumes it. A
        // SyncStep1 is a peer announcing "I just joined and want state" — the
        // signal to RE-ANNOUNCE our awareness. The server is a dumb relay that
        // doesn't track/replay awareness, so without this a late joiner never
        // learns existing peers' presence. readSyncMessage re-reads the sub-type
        // from the same decoder position (it hasn't advanced past it yet).
        const subType = decoding.readVarUint(decoder);
        const replyDecoder = decoding.createDecoder(data);
        decoding.readVarUint(replyDecoder); // skip the envelope byte
        const encoder = encoding.createEncoder();
        encoding.writeVarUint(encoder, messageSync);
        syncProtocol.readSyncMessage(replyDecoder, encoder, this.doc, REMOTE_ORIGIN);
        // length 1 = only the messageType byte was written → no reply payload.
        if (encoding.length(encoder) > 1 && this.ws) {
          this.ws.send(encoding.toUint8Array(encoder));
        }
        if (subType === syncProtocol.messageYjsSyncStep1) {
          const localState = this.awareness.getLocalState();
          if (localState !== null) this.broadcastAwareness([this.doc.clientID]);
        }
        break;
      }
      case messageAwareness: {
        applyAwarenessUpdate(this.awareness, decoding.readVarUint8Array(decoder), this);
        break;
      }
      default:
        // Enum drift: an envelope type this client doesn't know is ignored, not
        // fatal (mirrors the server's forward-compat stance).
        this.log("debug", "draft-yjs: unknown envelope", messageType);
    }
  }

  private handleDocUpdate = (update: Uint8Array, origin: unknown): void => {
    // Don't echo a network-applied update back to the network.
    if (origin === REMOTE_ORIGIN) return;
    if (!this.ws || this.ws.readyState !== this.WS.OPEN) return;
    const encoder = encoding.createEncoder();
    encoding.writeVarUint(encoder, messageSync);
    syncProtocol.writeUpdate(encoder, update);
    this.ws.send(encoding.toUint8Array(encoder));
  };

  private handleAwarenessUpdate = (
    changes: { added: number[]; updated: number[]; removed: number[] },
    origin: unknown,
  ): void => {
    // Only broadcast changes we originated locally. A change we applied from the
    // network has `origin === this`; re-broadcasting it loops.
    if (origin === this) return;
    const changed = [...changes.added, ...changes.updated, ...changes.removed];
    this.broadcastAwareness(changed);
  };

  private broadcastAwareness(clients: number[]): void {
    if (!this.ws || this.ws.readyState !== this.WS.OPEN || clients.length === 0) return;
    const encoder = encoding.createEncoder();
    encoding.writeVarUint(encoder, messageAwareness);
    encoding.writeVarUint8Array(encoder, encodeAwarenessUpdate(this.awareness, clients));
    this.ws.send(encoding.toUint8Array(encoder));
  }

  private emitStatus(connected: boolean): void {
    for (const cb of this.onStatus) cb(connected);
  }

  private scheduleReconnect(): void {
    if (this.destroyed || this.reconnectTimer) return;
    const delay = Math.min(this.reconnectBaseMs * 2 ** this.retries, MAX_BACKOFF_MS);
    this.retries += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.openSocket();
    }, delay);
  }

  /** Tear down: drop our awareness state, detach listeners, close the socket. */
  destroy(): void {
    this.destroyed = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.doc.off("update", this.handleDocUpdate);
    this.awareness.off("update", this.handleAwarenessUpdate);
    // Politely remove our own awareness state so peers drop our cursor.
    removeAwarenessStates(this.awareness, [this.doc.clientID], this);
    if (this.ws) {
      const ws = this.ws;
      this.ws = null;
      ws.onclose = null;
      ws.onerror = null;
      try {
        ws.close();
      } catch {
        // already closing/closed
      }
    }
    this.onStatus.clear();
  }
}
