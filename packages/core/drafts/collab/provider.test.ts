// Unit tests for the real Yjs provider (slice 3a). They run the provider against
// a FAKE in-memory relay that mimics the Go dumb-relay server: it replays a
// per-draft update log on connect, fans out frames to other sockets in the
// room, persists sync frames, and never persists awareness. This exercises the
// production framing (envelope + y-protocols sync/awareness) end to end without
// a real WebSocket or backend — the same role connectDocs plays for the editor
// spike, but for the provider's wire protocol.

import { describe, it, expect, afterEach, vi } from "vitest";
import * as Y from "yjs";
import { Awareness } from "y-protocols/awareness";
import { DraftYjsProvider } from "./provider";
import { draftYjsWsUrl } from "./url";

// --- Fake WebSocket + relay -------------------------------------------------

const OPEN = 1;
const CLOSED = 3;

/** The relay's view of one draft room: connected sockets + the persisted log. */
class FakeRoom {
  sockets = new Set<FakeWebSocket>();
  /** Persisted SYNC frames (envelope byte 0). Awareness is never stored. */
  log: Uint8Array[] = [];
}

/** A process-wide fake server keyed by draft URL. */
class FakeRelay {
  rooms = new Map<string, FakeRoom>();

  room(url: string): FakeRoom {
    let r = this.rooms.get(url);
    if (!r) {
      r = new FakeRoom();
      this.rooms.set(url, r);
    }
    return r;
  }

  join(ws: FakeWebSocket): void {
    const room = this.room(ws.url);
    room.sockets.add(ws);
    // Replay the persisted log to the joining socket (server behavior).
    for (const frame of room.log) ws.deliver(frame);
  }

  leave(ws: FakeWebSocket): void {
    const room = this.rooms.get(ws.url);
    room?.sockets.delete(ws);
  }

  /** A socket sent a binary frame: fan out to others + persist sync frames. */
  send(ws: FakeWebSocket, frame: Uint8Array): void {
    const room = this.room(ws.url);
    const envelope = frame[0];
    for (const peer of room.sockets) {
      if (peer !== ws) peer.deliver(frame);
    }
    if (envelope === 0) {
      // SYNC frame → persist (copy: callers may reuse buffers).
      room.log.push(frame.slice());
    }
    // envelope === 1 (awareness) → relayed above, NOT persisted.
  }
}

class FakeWebSocket {
  static relay = new FakeRelay();
  static readonly OPEN = OPEN;
  static readonly CLOSED = CLOSED;
  readyState = OPEN;
  binaryType = "arraybuffer";
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: ArrayBuffer | string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(public url: string) {
    // Open + join asynchronously, like a real socket.
    queueMicrotask(() => {
      if (this.readyState !== OPEN) return;
      FakeWebSocket.relay.join(this);
      this.onopen?.();
    });
  }

  send(data: string | ArrayBufferLike | ArrayBufferView): void {
    if (this.readyState !== OPEN) return;
    if (typeof data === "string") return; // auth frame — ignored by the fake relay
    const bytes =
      data instanceof Uint8Array
        ? data
        : new Uint8Array(data as ArrayBuffer);
    FakeWebSocket.relay.send(this, bytes);
  }

  /** Relay → this socket. Wrapped so the provider's onmessage sees ArrayBuffer. */
  deliver(frame: Uint8Array): void {
    if (this.readyState !== OPEN) return;
    const copy = frame.slice();
    this.onmessage?.({ data: copy.buffer.slice(copy.byteOffset, copy.byteOffset + copy.byteLength) });
  }

  close(): void {
    if (this.readyState === CLOSED) return;
    this.readyState = CLOSED;
    FakeWebSocket.relay.leave(this);
    this.onclose?.();
  }
}

// A constructor matching the lib.dom WebSocket shape closely enough for the
// provider (it only uses new(), send, close, the handlers, binaryType, OPEN).
const FakeWS = FakeWebSocket as unknown as typeof WebSocket;

function resetRelay() {
  FakeWebSocket.relay = new FakeRelay();
}

interface Peer {
  doc: Y.Doc;
  awareness: Awareness;
  provider: DraftYjsProvider;
}

const peers: Peer[] = [];
function makePeer(url: string, name: string): Peer {
  const doc = new Y.Doc();
  const awareness = new Awareness(doc);
  awareness.setLocalStateField("user", { name, color: "#abcabc" });
  const provider = new DraftYjsProvider({
    url,
    doc,
    awareness,
    WebSocketImpl: FakeWS,
    reconnectBaseMs: 5,
  });
  const peer = { doc, awareness, provider };
  peers.push(peer);
  return peer;
}

afterEach(() => {
  while (peers.length) {
    const p = peers.pop()!;
    p.provider.destroy();
    p.awareness.destroy();
    p.doc.destroy();
  }
  resetRelay();
});

const flush = () => new Promise<void>((r) => setTimeout(r, 0));

describe("DraftYjsProvider / sync", () => {
  it("propagates an edit from one peer to another over the relay", async () => {
    resetRelay();
    const url = draftYjsWsUrl("ws://host:8080/ws", "draft-1");

    const a = makePeer(url, "Ann");
    const b = makePeer(url, "Bob");
    a.provider.connect();
    b.provider.connect();
    await flush();

    // Ann types into the shared text.
    a.doc.getText("body").insert(0, "hello from Ann");
    await flush();

    expect(b.doc.getText("body").toString()).toBe("hello from Ann");
  });

  it("converges a peer that edited BEFORE connecting (full-state push on open)", async () => {
    resetRelay();
    const url = draftYjsWsUrl("ws://host:8080/ws", "draft-2");

    const a = makePeer(url, "Ann");
    // Ann edits offline, before her provider connects.
    a.doc.getText("body").insert(0, "offline edit");

    const b = makePeer(url, "Bob");
    a.provider.connect();
    b.provider.connect();
    await flush();
    await flush();

    expect(b.doc.getText("body").toString()).toBe("offline edit");
  });

  it("relays awareness so a peer learns another's presence state", async () => {
    resetRelay();
    const url = draftYjsWsUrl("ws://host:8080/ws", "draft-aware");

    const a = makePeer(url, "Ann");
    const b = makePeer(url, "Bob");
    a.provider.connect();
    b.provider.connect();
    await flush();
    await flush();

    // Bob sees Ann's awareness entry (a remote clientID with a user field).
    const remoteStates = Array.from(b.awareness.getStates().entries()).filter(
      ([id]) => id !== b.doc.clientID,
    );
    expect(remoteStates.length).toBeGreaterThanOrEqual(1);
    const annState = remoteStates.find(
      ([, s]) => (s as { user?: { name?: string } }).user?.name === "Ann",
    );
    expect(annState).toBeDefined();
  });
});

describe("DraftYjsProvider / reconnect-replay", () => {
  it("a freshly-connecting peer converges from the replayed log alone", async () => {
    resetRelay();
    const url = draftYjsWsUrl("ws://host:8080/ws", "draft-replay");

    // Ann connects and makes an edit; the relay persists it to the log.
    const a = makePeer(url, "Ann");
    a.provider.connect();
    await flush();
    a.doc.getText("body").insert(0, "persisted content");
    await flush();

    // Ann leaves entirely.
    a.provider.destroy();

    // A brand-new peer connects later: it converges purely from the replayed
    // log (no live peer is editing).
    const c = makePeer(url, "Cat");
    c.provider.connect();
    await flush();
    await flush();

    expect(c.doc.getText("body").toString()).toBe("persisted content");
  });

  it("reconnects after an unexpected close and re-syncs", async () => {
    resetRelay();
    vi.useFakeTimers();
    const url = draftYjsWsUrl("ws://host:8080/ws", "draft-reconnect");

    const a = makePeer(url, "Ann");
    const b = makePeer(url, "Bob");
    a.provider.connect();
    b.provider.connect();
    await vi.advanceTimersByTimeAsync(1);

    // Simulate an unexpected drop of Bob's socket.
    // Access the live socket via the relay and force-close it.
    const room = FakeWebSocket.relay.room(url);
    const bobSocket = Array.from(room.sockets).find(
      (s) => s !== Array.from(room.sockets)[0],
    );
    // Close whichever socket is Bob's; the provider schedules a reconnect.
    bobSocket?.close();

    // Ann edits while Bob is briefly disconnected — it persists to the log.
    a.doc.getText("body").insert(0, "during outage");

    // Bob's provider reconnects (backoff timer) and replays the log.
    await vi.advanceTimersByTimeAsync(50);
    await vi.advanceTimersByTimeAsync(50);

    vi.useRealTimers();
    await flush();

    expect(b.doc.getText("body").toString()).toContain("during outage");
  });
});
