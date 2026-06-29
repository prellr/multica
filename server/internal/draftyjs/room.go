package draftyjs

import (
	"sync"
)

// Conn is the minimal write surface a room needs over a client connection. It
// matches *websocket.Conn's WriteMessage, so the real handler passes the gorilla
// connection directly and tests pass a fake recorder.
type Conn interface {
	// WriteMessage writes one frame. messageType is the WebSocket opcode
	// (always websocket.BinaryMessage here). A non-nil error means the peer is
	// gone; the room evicts the member.
	WriteMessage(messageType int, data []byte) error
}

// binaryOpcode is gorilla/websocket.BinaryMessage. Duplicated as a const so the
// room package has no dependency on gorilla — keeps it unit-testable with a
// trivial fake Conn.
const binaryOpcode = 2

// Member is one connected client in a draft's room. Writes are serialized
// through writeMu because a single gorilla *websocket.Conn does not permit
// concurrent writers (a replayed log and a peer's relayed frame can race).
type Member struct {
	conn    Conn
	writeMu sync.Mutex
}

// Send writes one binary frame to this member, serialized against other writers.
// Returns the write error so the room can evict a dead member.
func (m *Member) Send(frame []byte) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return m.conn.WriteMessage(binaryOpcode, frame)
}

// Room is the set of clients co-editing one draft. It is a pure fan-out hub: it
// holds no Y.Doc and never decodes a frame. Persistence and replay-on-connect
// are driven by the handler (which owns the DB); the Room only relays a frame to
// every OTHER member.
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

// remove drops a member. Returns the member count after the remove.
func (r *Room) remove(m *Member) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.members, m)
	return len(r.members)
}

// Broadcast sends frame to every member except sender. A member whose write
// fails is collected and evicted after the snapshot (we don't mutate the map
// while iterating a read-lock). The frame bytes are NOT copied — callers must
// not mutate frame after handing it off, which the read-loop guarantees (each
// ReadMessage returns a fresh slice).
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

	var dead []*Member
	for _, m := range targets {
		if err := m.Send(frame); err != nil {
			dead = append(dead, m)
		}
	}
	if len(dead) > 0 {
		r.mu.Lock()
		for _, m := range dead {
			delete(r.members, m)
		}
		r.mu.Unlock()
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

// Join adds a connection to draftID's room, creating the room if needed, and
// returns the member handle plus the room. The caller relays the member's
// inbound frames via Room.broadcast and calls Leave when the connection closes.
func (h *Hub) Join(draftID string, conn Conn) (*Member, *Room) {
	h.mu.Lock()
	room, ok := h.rooms[draftID]
	if !ok {
		room = newRoom()
		h.rooms[draftID] = room
	}
	h.mu.Unlock()

	m := &Member{conn: conn}
	room.add(m)
	return m, room
}

// Leave removes a member from draftID's room and tears the room down if it is
// now empty, so a long-lived process doesn't accumulate empty rooms. Returns the
// remaining member count (0 means the room was the last-client-leave — the seam
// where draft.body may lag the latest Yjs updates; see the handler).
func (h *Hub) Leave(draftID string, m *Member, room *Room) int {
	remaining := room.remove(m)
	if remaining == 0 {
		h.mu.Lock()
		// Re-check under the hub lock: a concurrent Join may have repopulated
		// the room between remove() and here. Only delete if still empty.
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
