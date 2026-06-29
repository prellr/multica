package draftyjs

import (
	"sync"
	"time"
)

// Connection timing — mirrors realtime.Hub's Client pump (writeWait / pongWait /
// pingPeriod). Defined locally so the relay stays a self-contained package and
// never couples to the realtime hub's internals.
const (
	// writeWait is the per-write deadline. A peer that can't accept a frame
	// within this window is treated as wedged.
	writeWait = 10 * time.Second
	// PongWait is how long a connection may go without a pong before its read
	// pump times out. Exported because the read pump lives in the handler (it
	// owns the gorilla conn); the write pump here pings on pingPeriod.
	PongWait = 60 * time.Second
	// pingPeriod must be < PongWait so a ping always precedes the read timeout.
	pingPeriod = (PongWait * 9) / 10
	// sendBuffer is the per-member outbound queue depth. A burst of relayed
	// frames buffers here; a member that stays full past it is evicted (slow
	// consumer) rather than blocking the room. Generous because replay-on-connect
	// streams the whole log into one member's queue at once.
	sendBuffer = 512
)

// Conn is the surface a room's write pump needs over a client connection. It
// matches the subset of *websocket.Conn the pump uses, so the handler passes the
// gorilla connection directly and tests pass a fake.
type Conn interface {
	// WriteMessage writes one frame (messageType is the WS opcode).
	WriteMessage(messageType int, data []byte) error
	// SetWriteDeadline bounds a single write so a stuck peer can't block the
	// write-pump goroutine forever.
	SetWriteDeadline(t time.Time) error
	// Close tears down the underlying socket. Called once when the write pump
	// exits (channel closed or write error).
	Close() error
}

// gorilla/websocket opcodes, duplicated so the room package has no gorilla
// dependency and stays unit-testable with a trivial fake Conn.
const (
	binaryOpcode = 2 // websocket.BinaryMessage
	pingOpcode   = 9 // websocket.PingMessage
	closeOpcode  = 8 // websocket.CloseMessage
)

// Member is one connected client in a draft's room. Outbound frames are NEVER
// written directly to the conn from a caller goroutine — they are queued on
// `send` and a single dedicated write-pump goroutine drains the queue. This is
// the head-of-line-blocking fix: a slow/wedged peer fills its own buffer and is
// evicted, never blocking the sender's read goroutine or the rest of the room.
type Member struct {
	conn Conn
	send chan []byte

	mu     sync.Mutex
	closed bool
}

func newMember(conn Conn) *Member {
	return &Member{conn: conn, send: make(chan []byte, sendBuffer)}
}

// enqueue does a NON-BLOCKING push onto the member's send queue. Returns false if
// the queue is full (a slow consumer) — the caller evicts the member. Safe under
// the member's own close (a closed member reports full so it gets evicted/skipped
// rather than panicking on a send to a closed channel).
func (m *Member) enqueue(frame []byte) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	select {
	case m.send <- frame:
		m.mu.Unlock()
		return true
	default:
		m.mu.Unlock()
		return false
	}
}

// closeSend closes the send channel exactly once, signaling the write pump to
// flush a close frame and exit. Idempotent.
func (m *Member) closeSend() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.send)
}

// writePump is the single goroutine permitted to write to this member's conn. It
// drains `send`, applying a per-write deadline so a stuck peer can't wedge it,
// and pings on pingPeriod so a half-open connection surfaces as a write error.
// Exits (and closes the conn) when the send channel is closed or a write fails.
func (m *Member) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		m.conn.Close()
	}()

	for {
		select {
		case frame, ok := <-m.send:
			_ = m.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Channel closed (evicted or shutting down): best-effort close
				// frame, then exit.
				_ = m.conn.WriteMessage(closeOpcode, []byte{})
				return
			}
			if err := m.conn.WriteMessage(binaryOpcode, frame); err != nil {
				return
			}
		case <-ticker.C:
			_ = m.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := m.conn.WriteMessage(pingOpcode, nil); err != nil {
				return
			}
		}
	}
}

// Room is the set of clients co-editing one draft. Pure fan-out: it holds no
// Y.Doc and never decodes a frame. Persistence and replay-on-connect are driven
// by the handler; the Room only queues a frame to every OTHER member.
type Room struct {
	mu      sync.RWMutex
	members map[*Member]struct{}
}

func newRoom() *Room {
	return &Room{members: make(map[*Member]struct{})}
}

// add registers a member. Returns the member count after the add.
func (r *Room) add(m *Member) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[m] = struct{}{}
	return len(r.members)
}

// removeLocked drops a member assuming the caller does NOT hold r.mu.
func (r *Room) remove(m *Member) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.members, m)
	return len(r.members)
}

// Broadcast queues frame to every member except sender via a NON-BLOCKING
// enqueue. A member whose queue is full is a slow consumer: it is collected and
// evicted after the snapshot (closeSend → its write pump flushes a close frame
// and tears the socket down). A wedged peer therefore NEVER blocks the sender's
// read goroutine or the rest of the room. The frame bytes are not copied —
// callers must not mutate frame after handing it off (each ReadMessage returns a
// fresh slice, so this holds).
func (r *Room) Broadcast(frame []byte, sender *Member) {
	r.mu.RLock()
	targets := make([]*Member, 0, len(r.members))
	for m := range r.members {
		if m == sender {
			continue
		}
		targets = append(targets, m)
	}
	r.mu.RUnlock()

	var slow []*Member
	for _, m := range targets {
		if !m.enqueue(frame) {
			slow = append(slow, m)
		}
	}
	if len(slow) > 0 {
		r.mu.Lock()
		for _, m := range slow {
			delete(r.members, m)
		}
		r.mu.Unlock()
		// Signal each evicted member's write pump to flush + tear down. Done
		// outside the room lock so a pump's Close can't deadlock against it.
		for _, m := range slow {
			m.closeSend()
		}
	}
}

// Hub keys rooms by draft id. One Hub is shared process-wide; rooms are created
// on first join and torn down on last leave.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

// NewHub creates an empty draft-room hub.
func NewHub() *Hub {
	return &Hub{rooms: make(map[string]*Room)}
}

// Join adds a connection to draftID's room (creating the room if needed), starts
// the member's write pump, and returns the member + room. All outbound writes
// from here on go through the pump; the caller drives the read loop and calls
// Leave when the connection closes.
func (h *Hub) Join(draftID string, conn Conn) (*Member, *Room) {
	h.mu.Lock()
	room, ok := h.rooms[draftID]
	if !ok {
		room = newRoom()
		h.rooms[draftID] = room
	}
	h.mu.Unlock()

	m := newMember(conn)
	room.add(m)
	go m.writePump()
	return m, room
}

// Enqueue queues one frame to a single member through its pump (used by the
// handler for replay-on-connect). Non-blocking; a full queue means the member is
// already a slow consumer and gets evicted by the next Broadcast. Returns whether
// the frame was queued.
func (h *Hub) Enqueue(m *Member, frame []byte) bool {
	return m.enqueue(frame)
}

// Leave removes a member from draftID's room, signals its write pump to exit, and
// tears the room down if it is now empty. Returns the remaining member count (0 =
// last-client-leave — the body-staleness seam; see the handler).
func (h *Hub) Leave(draftID string, m *Member, room *Room) int {
	remaining := room.remove(m)
	// Stop the write pump (idempotent if Broadcast already evicted this member).
	m.closeSend()
	if remaining == 0 {
		h.mu.Lock()
		// Re-check under the hub lock: a concurrent Join may have repopulated the
		// room between remove() and here. Only delete if still empty.
		if cur, ok := h.rooms[draftID]; ok && cur == room {
			room.mu.RLock()
			empty := len(room.members) == 0
			room.mu.RUnlock()
			if empty {
				delete(h.rooms, draftID)
			}
		}
		h.mu.Unlock()
	}
	return remaining
}

// RoomCount returns the number of live rooms. Test/observability only.
func (h *Hub) RoomCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms)
}

// MemberCount returns the number of members in draftID's room (0 if no room).
// Test/observability only.
func (h *Hub) MemberCount(draftID string) int {
	h.mu.Lock()
	room, ok := h.rooms[draftID]
	h.mu.Unlock()
	if !ok {
		return 0
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	return len(room.members)
}
