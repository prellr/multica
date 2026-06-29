package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/draftyjs"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Drafts slice 3a — the human CRDT (Yjs) co-editing floor.
//
// GET /api/drafts/{id}/yjs upgrades to a BINARY WebSocket and joins the draft's
// co-editing room. The server is a DUMB RELAY: it never decodes the CRDT. Per
// connection it:
//   - authenticates the user (cookie or first-message token, the same path as
//     realtime.HandleWebSocket) and gates access via the draft's owner scoping
//     + workspace membership BEFORE upgrading — a non-owner/non-member is
//     rejected with an HTTP error, never upgraded;
//   - replays the persisted update log (draft_yjs_update, in seq order) to the
//     joining client so a fresh or reconnecting client converges (Yjs merges
//     updates idempotently);
//   - relays each inbound frame to the OTHER room members, and additionally
//     appends SYNC frames (document content) to the log. AWARENESS frames
//     (ephemeral presence) are relayed but never persisted.
//
// Reconciliation seam (3a, flagged not solved): draft.body markdown is
// serialized CLIENT-SIDE on quiescence (the editor's debounced PUT). On
// last-client-leave, body may lag the latest Yjs updates that arrived after the
// last snapshot — a server-side serializer/compactor is deliberately OUT OF
// SCOPE for 3a (a later slice or a pg_crdt swap). The Yjs log is the durable
// truth in that window; body re-converges the next time any client opens the
// draft (it hydrates from the replayed log; the log only re-seeds from body when
// it is empty).

// draftYjsUpgrader is a dedicated binary-frame upgrader. It shares the realtime
// origin allowlist (CSRF defense) but is its own value so the draft relay never
// inherits future realtime-specific upgrader tuning.
var draftYjsUpgrader = websocket.Upgrader{
	CheckOrigin: realtime.CheckWSOrigin,
}

// DraftYjsWebSocket handles GET /api/drafts/{id}/yjs. mc/pr are the same
// membership checker and PAT resolver realtime.HandleWebSocket uses, injected by
// the router so the auth path is identical across WebSocket endpoints.
func (h *Handler) DraftYjsWebSocket(mc realtime.MembershipChecker, pr realtime.PATResolver, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rawDraftID := chi.URLParam(r, "id")

	// --- Auth (pre-upgrade where possible) -------------------------------
	// Cookie auth resolves before the upgrade so a browser request with a
	// valid session is gated without a round-trip. A request with no cookie
	// (native client, or a browser that can't attach one cross-origin) falls
	// through to first-message auth after the upgrade.
	userID, errMsg := realtime.CookieUserID(ctx, r, pr)
	if errMsg != "" {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	// --- Resolve + gate the draft (pre-upgrade for the cookie path) ------
	// For the cookie path we can fully gate before upgrading: reject a
	// non-owner / non-member with a plain HTTP 404, no WebSocket at all.
	var draft db.Draft
	var ok bool
	if userID != "" {
		draft, ok = h.gateDraftForYjs(ctx, mc, rawDraftID, userID)
		if !ok {
			writeError(w, http.StatusNotFound, "draft not found")
			return
		}
	}

	// --- Upgrade ----------------------------------------------------------
	conn, err := draftYjsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("draft yjs: websocket upgrade failed", "error", err)
		return
	}

	// --- First-message auth (no cookie) -----------------------------------
	// After the upgrade we can no longer write an HTTP status, so a failure
	// here closes the connection. The gate runs identically to the cookie
	// path — a non-owner/non-member is dropped before joining the room.
	if userID == "" {
		uid, errMsg := realtime.FirstMessageAuth(ctx, conn, pr)
		if errMsg != "" {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(errMsg))
			conn.Close()
			return
		}
		draft, ok = h.gateDraftForYjs(ctx, mc, rawDraftID, uid)
		if !ok {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"draft not found"}`))
			conn.Close()
			return
		}
		userID = uid
	}

	h.serveDraftYjs(ctx, conn, draft, userID)
}

// gateDraftForYjs resolves a draft by its UUID and verifies the user owns it and
// belongs to its workspace. Mirrors loadDraftForUser's scoping but works from a
// raw userID (the WS connection has no middleware-set actor): a draft UUID
// belonging to another user — or a user who is no longer a workspace member —
// returns ok=false, which the caller maps to "not found" (no information leak
// about whether the draft exists).
func (h *Handler) gateDraftForYjs(ctx context.Context, mc realtime.MembershipChecker, rawDraftID, userID string) (db.Draft, bool) {
	draftUUID, err := util.ParseUUID(rawDraftID)
	if err != nil {
		return db.Draft{}, false
	}
	draft, err := h.Queries.GetDraftByID(ctx, draftUUID)
	if err != nil {
		return db.Draft{}, false
	}
	// Owner scoping (slice 3a is single-owner; the owner is necessarily a
	// member, so this is the stricter gate).
	if uuidToString(draft.OwnerUserID) != userID {
		return db.Draft{}, false
	}
	// Defense in depth: confirm the owner is still a member of the draft's
	// workspace (membership can be revoked after a draft is created).
	if mc != nil && !mc.IsMember(ctx, userID, uuidToString(draft.WorkspaceID)) {
		return db.Draft{}, false
	}
	return draft, true
}

// serveDraftYjs runs the relay loop for one authenticated, gated connection. It
// joins the draft's room, replays the persisted log, then pumps inbound frames
// (relaying to peers and persisting sync frames) until the connection closes.
// draftYjsReadLimit bounds a single inbound frame (memory-DoS guard). Sized
// generous for a large paste / a big initial-state update on connect, but
// bounded so one authed client can't force an arbitrarily large alloc + BYTEA
// row. A doc update is normally a few KB; 4 MB is a wide ceiling for a worst-case
// full-state push on a large document.
const draftYjsReadLimit = 4 << 20 // 4 MiB

func (h *Handler) serveDraftYjs(ctx context.Context, conn *websocket.Conn, draft db.Draft, userID string) {
	draftID := uuidToString(draft.ID)

	// Read-pump hardening (mirrors realtime.HandleWebSocket's readPump):
	//   - SetReadLimit bounds a single frame (memory-DoS guard).
	//   - SetReadDeadline + SetPongHandler are the keepalive: a client that
	//     drops without a FIN (sleep / killed wifi / crash) stops ponging, the
	//     read deadline fires, ReadMessage errors, and this loop exits — so the
	//     goroutine, socket, and room-member entry are reclaimed instead of
	//     leaking. The write pump (in draftyjs) sends the pings on pingPeriod.
	conn.SetReadLimit(draftYjsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(draftyjs.PongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(draftyjs.PongWait))
	})

	hub := h.DraftYjsHub
	member, room := hub.Join(draftID, conn)
	defer func() {
		remaining := hub.Leave(draftID, member, room)
		if remaining == 0 {
			// LAST-CLIENT-LEAVE SEAM: at this point draft.body may lag the
			// Yjs log (body is serialized client-side on quiescence; updates
			// can arrive after the last snapshot). 3a does NOT serialize
			// server-side — the log is the durable truth and body
			// re-converges on the next open. Logged so the staleness window
			// is observable.
			slog.Info("draft yjs: last client left room; body may lag the yjs log until next open",
				"draft_id", draftID, "user_id", userID)
		}
		// The write pump (started by Join, stopped by Leave→closeSend) owns the
		// conn.Close(); we don't double-close here.
	}()

	// Replay the persisted update log to the joining client, and surface the log
	// size — the log is uncompacted in 3a (compaction deferred), so warn-logging
	// the row count makes the unbounded-log / replay-scale window observable in
	// prod before it becomes a problem.
	replayed := h.replayDraftYjsLog(ctx, draft.ID, draftID, member)
	logHasGrown := replayed >= draftYjsLogGrowthWarnAt
	if logHasGrown {
		slog.Warn("draft yjs: replaying a large uncompacted update log (compaction deferred to a later slice)",
			"draft_id", draftID, "user_id", userID, "log_rows", replayed)
	} else {
		slog.Info("draft yjs: client joined", "draft_id", draftID, "user_id", userID, "log_rows", replayed)
	}

	for {
		msgType, frame, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("draft yjs: read error", "draft_id", draftID, "user_id", userID, "error", err)
			}
			return
		}
		// The relay speaks binary frames only. A stray text frame (e.g. an
		// auth ack the client echoes) is ignored rather than relayed.
		if msgType != websocket.BinaryMessage {
			continue
		}

		// Relay to every OTHER member first (presence + edits propagate with
		// minimal latency), then persist sync frames. Broadcast queues
		// non-blockingly through each peer's write pump, so a slow peer is
		// evicted rather than blocking this read goroutine.
		room.Broadcast(frame, member)

		if draftyjs.IsSyncFrame(frame) {
			h.persistDraftYjsUpdate(ctx, draft.ID, draftID, frame)
		}
		// Awareness frames are intentionally NOT persisted — ephemeral
		// presence dies with the connection.
	}
}

// draftYjsLogGrowthWarnAt is the replay-row count above which a join warn-logs
// the uncompacted-log size. Chosen as a soft "this draft has accumulated a lot
// of history" threshold — purely observability; replay still works at any size.
const draftYjsLogGrowthWarnAt = 1000

// replayDraftYjsLog queues every persisted update for a draft to one member, in
// seq order, through its write pump. Yjs applies them idempotently, so replaying
// the whole (uncompacted) log is a correct initial sync. Returns the number of
// rows replayed (for log-growth observability). A full queue mid-replay means
// the member is already a slow consumer — it'll be evicted by the pump/Broadcast
// path, so we stop queuing to it rather than blocking.
func (h *Handler) replayDraftYjsLog(ctx context.Context, draftID pgtype.UUID, logDraftID string, member *draftyjs.Member) int {
	updates, err := h.Queries.ListDraftYjsUpdates(ctx, draftID)
	if err != nil {
		// A failed replay query is a real problem: the joining client converges
		// only from live peer traffic, not history. Warn (per review).
		slog.Warn("draft yjs: failed to load update log for replay", "error", err, "draft_id", logDraftID)
		return 0
	}
	queued := 0
	for _, u := range updates {
		if !h.DraftYjsHub.Enqueue(member, u.Update) {
			// Member's queue is full — it's a slow consumer and will be evicted.
			// Stop replaying to it; the read loop / Broadcast path handles
			// teardown.
			slog.Warn("draft yjs: replay queue full, member is a slow consumer",
				"draft_id", logDraftID, "queued", queued, "total", len(updates))
			break
		}
		queued++
	}
	return queued
}

// persistDraftYjsUpdate appends one opaque sync frame to the draft's log. A
// failure is logged but never propagated to the client: a dropped persist means
// a future fresh client might miss that update on replay, but live peers already
// received it via the relay — degrading replay completeness is preferable to
// tearing down a live co-editing session on a transient DB blip.
func (h *Handler) persistDraftYjsUpdate(ctx context.Context, draftID pgtype.UUID, logDraftID string, frame []byte) {
	// Copy the frame: ReadMessage returns a buffer gorilla may reuse, and the
	// append happens after we've handed the original to Broadcast. A defensive
	// copy guarantees the bytes we persist are stable.
	cp := make([]byte, len(frame))
	copy(cp, frame)
	if _, err := h.Queries.AppendDraftYjsUpdate(ctx, db.AppendDraftYjsUpdateParams{
		DraftID: draftID,
		Update:  cp,
	}); err != nil {
		slog.Warn("draft yjs: failed to persist update", "error", err, "draft_id", logDraftID)
	}
}
