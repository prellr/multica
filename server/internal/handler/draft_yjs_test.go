package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/draftyjs"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Drafts slice 3a — the Yjs binary co-editing relay. These tests exercise the
// novel server piece end to end over a real WebSocket:
//   - replay-on-connect streams the persisted log to a joining client
//   - a sync frame is relayed to other room members AND appended to the log
//   - an awareness frame is relayed but NEVER persisted
//   - the auth/owner gate rejects a non-owner before the upgrade
//
// The relay is a DUMB relay: the frames here are arbitrary bytes with the
// correct leading envelope varUint (0 = sync, 1 = awareness). The server never
// decodes them, so synthetic payloads are sufficient to prove the routing.

// draftYjsMembership is a membership checker that admits only testUserID in the
// test workspace — the gate's membership leg.
type draftYjsMembership struct{}

func (draftYjsMembership) IsMember(_ context.Context, userID, workspaceID string) bool {
	return userID == testUserID && workspaceID == testWorkspaceID
}

// newDraftYjsServer mounts the relay on an httptest server, mirroring the router
// wiring (mc + pr injected, first-message/cookie auth inside the handler).
func newDraftYjsServer(t *testing.T) *httptest.Server {
	t.Helper()
	mc := draftYjsMembership{}
	mux := http.NewServeMux()
	// chi isn't needed: the handler reads the draft id from the chi URL param,
	// so we attach it via a tiny wrapper that sets the param like the test
	// helper withURLParam does.
	mux.HandleFunc("/api/drafts/", func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/drafts/{id}/yjs
		rest := strings.TrimPrefix(r.URL.Path, "/api/drafts/")
		id := strings.TrimSuffix(rest, "/yjs")
		r = withURLParam(r, "id", id)
		testHandler.DraftYjsWebSocket(mc, realtime.PATResolver(nil), w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func draftYjsToken(t *testing.T, userID string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": userID})
	signed, err := token.SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

// dialDraftYjs opens a relay connection for draftID and authenticates via the
// first-message path with a JWT for userID. It returns the connection AFTER the
// first-message auth handshake (no auth_ack frame on this endpoint — the relay
// proceeds straight to replay).
func dialDraftYjs(t *testing.T, srv *httptest.Server, draftID, userID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/drafts/" + draftID + "/yjs"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// First-message auth frame (text), same envelope realtime.HandleWebSocket
	// expects.
	authMsg := `{"type":"auth","payload":{"token":"` + draftYjsToken(t, userID) + `"}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(authMsg)); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	return conn
}

// readBinaryWithin reads one binary frame, failing if none arrives within d.
func readBinaryWithin(t *testing.T, conn *websocket.Conn, d time.Duration) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(d))
	defer conn.SetReadDeadline(time.Time{})
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read binary: %v", err)
		}
		if mt == websocket.BinaryMessage {
			return data
		}
		// Skip any text frame (e.g. an error) — caller asserts binary.
	}
}

func countDraftYjsUpdates(t *testing.T, draftID string) int64 {
	t.Helper()
	n, err := testHandler.Queries.CountDraftYjsUpdates(context.Background(), util.MustParseUUID(draftID))
	if err != nil {
		t.Fatalf("count updates: %v", err)
	}
	return n
}

// TestDraftYjsRelayPersistsSyncFansOutAndNotAwareness covers the core relay
// contract in one flow: two clients in a room; one sends a sync frame (relayed
// to the other AND persisted) and an awareness frame (relayed, NOT persisted).
func TestDraftYjsRelayPersistsSyncFansOutAndNotAwareness(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture unavailable")
	}
	srv := newDraftYjsServer(t)
	draftID := createDraftForTest(t, "yjs relay", "# hello")

	a := dialDraftYjs(t, srv, draftID, testUserID)
	defer a.Close()
	b := dialDraftYjs(t, srv, draftID, testUserID)
	defer b.Close()

	// Give both joins time to register in the room before A broadcasts.
	time.Sleep(150 * time.Millisecond)

	syncFrame := []byte{draftyjs.MessageSync, 0x02, 0xde, 0xad, 0xbe, 0xef}
	if err := a.WriteMessage(websocket.BinaryMessage, syncFrame); err != nil {
		t.Fatalf("A write sync: %v", err)
	}

	// B receives the relayed sync frame.
	got := readBinaryWithin(t, b, 2*time.Second)
	if string(got) != string(syncFrame) {
		t.Fatalf("B relayed sync = %v, want %v", got, syncFrame)
	}

	// The sync frame is appended to the log.
	deadline := time.Now().Add(2 * time.Second)
	for countDraftYjsUpdates(t, draftID) < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := countDraftYjsUpdates(t, draftID); n != 1 {
		t.Fatalf("after sync frame, log has %d rows, want 1", n)
	}

	// Now an awareness frame: relayed to B but NOT persisted.
	awarenessFrame := []byte{draftyjs.MessageAwareness, 0x01, 0x99}
	if err := a.WriteMessage(websocket.BinaryMessage, awarenessFrame); err != nil {
		t.Fatalf("A write awareness: %v", err)
	}
	got = readBinaryWithin(t, b, 2*time.Second)
	if string(got) != string(awarenessFrame) {
		t.Fatalf("B relayed awareness = %v, want %v", got, awarenessFrame)
	}

	// Give any (erroneous) persist a chance to land, then assert the log is
	// still 1 — awareness was NOT persisted.
	time.Sleep(200 * time.Millisecond)
	if n := countDraftYjsUpdates(t, draftID); n != 1 {
		t.Fatalf("awareness frame must not be persisted: log has %d rows, want 1", n)
	}
}

// TestDraftYjsReplayOnConnect: a frame persisted by an earlier session is
// replayed to a freshly-joining client before any live traffic.
func TestDraftYjsReplayOnConnect(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture unavailable")
	}
	srv := newDraftYjsServer(t)
	draftID := createDraftForTest(t, "yjs replay", "# hello")

	// Pre-seed the log directly (simulating a prior session's persisted update).
	seeded := []byte{draftyjs.MessageSync, 0x02, 0x01, 0x02, 0x03}
	if _, err := testHandler.Queries.AppendDraftYjsUpdate(context.Background(), db.AppendDraftYjsUpdateParams{
		DraftID: util.MustParseUUID(draftID),
		Update:  seeded,
	}); err != nil {
		t.Fatalf("seed update: %v", err)
	}

	conn := dialDraftYjs(t, srv, draftID, testUserID)
	defer conn.Close()

	// On connect, the relay replays the log: the first binary frame is the
	// seeded update, byte-identical.
	got := readBinaryWithin(t, conn, 2*time.Second)
	if string(got) != string(seeded) {
		t.Fatalf("replay frame = %v, want %v", got, seeded)
	}
}

// TestDraftYjsGateRejectsNonOwner: a user who does not own the draft is rejected
// at the gate (the first-message auth path closes the connection without
// joining the room or replaying anything).
func TestDraftYjsGateRejectsNonOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture unavailable")
	}
	srv := newDraftYjsServer(t)
	draftID := createDraftForTest(t, "yjs gate", "# hello")

	// A different user (valid JWT, but not the owner and not a member per the
	// mock checker) must be rejected.
	otherUserID := util.UUIDToString(util.MustParseUUID("00000000-0000-0000-0000-0000000000aa"))
	conn := dialDraftYjs(t, srv, draftID, otherUserID)
	defer conn.Close()

	// The relay rejects after first-message auth: we expect either an error
	// text frame or a close, NOT a replayed binary frame.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, _, err := conn.ReadMessage()
	if err != nil {
		// A close is an acceptable rejection signal.
		return
	}
	if mt == websocket.BinaryMessage {
		t.Fatalf("non-owner received a binary frame — gate did not reject")
	}
	// A text frame here is the error payload — acceptable. Confirm no binary
	// frame follows (no room join).
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if mt2, _, err := conn.ReadMessage(); err == nil && mt2 == websocket.BinaryMessage {
		t.Fatalf("non-owner received a binary frame after the error — gate did not reject")
	}
}
