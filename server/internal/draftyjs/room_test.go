package draftyjs

import (
	"sync"
	"testing"
	"time"
)

// fakeConn implements the room's Conn interface with no real WebSocket. Binary
// frames are recorded; ping/close opcodes are ignored (the pump emits those).
// `block`, when set, makes WriteMessage sleep — standing in for a wedged peer
// whose TCP send buffer is full — so the backpressure path can be exercised.
type fakeConn struct {
	mu      sync.Mutex
	frames  [][]byte
	failOn  int // after this many successful binary writes, return an error (-1 = never)
	writes  int
	block   chan struct{} // if non-nil, WriteMessage blocks until this is closed
	closed  bool
	closeCh chan struct{}
}

func newFakeConn() *fakeConn { return &fakeConn{failOn: -1, closeCh: make(chan struct{})} }

func (c *fakeConn) WriteMessage(opcode int, data []byte) error {
	// Block (slow consumer) BEFORE taking the lock so a wedged write doesn't
	// hold the recorder lock — mirrors a real socket stuck in send().
	if c.block != nil {
		<-c.block
	}
	// Ignore ping/close control frames the pump emits — only record binary.
	if opcode != binaryOpcode {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	if c.failOn >= 0 && c.writes > c.failOn {
		return errWrite
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	c.frames = append(c.frames, cp)
	return nil
}

func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.closeCh)
	}
	return nil
}

func (c *fakeConn) got() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.frames))
	copy(out, c.frames)
	return out
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

type sentinelErr struct{}

func (sentinelErr) Error() string { return "fake write error" }

var errWrite = sentinelErr{}

// waitFor polls cond until it returns true or the deadline passes. The write
// pump delivers frames asynchronously, so assertions poll rather than read
// synchronously.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// --- Protocol classification -----------------------------------------------

func TestIsSyncFrame(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		want  bool
	}{
		{"sync update (envelope 0)", []byte{MessageSync, 0x02, 0xff}, true},
		{"sync step1 (envelope 0)", []byte{MessageSync, 0x00, 0x01}, true},
		{"awareness (envelope 1)", []byte{MessageAwareness, 0x01, 0xaa}, false},
		{"empty frame", []byte{}, false},
		// A multi-byte varUint envelope > 1 is neither sync nor awareness.
		{"unknown envelope 200", []byte{0xc8, 0x01, 0x00}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSyncFrame(tc.frame); got != tc.want {
				t.Fatalf("IsSyncFrame(%v) = %v, want %v", tc.frame, got, tc.want)
			}
		})
	}
}

func TestReadVarUintMultiByte(t *testing.T) {
	// 200 encodes as [0xc8, 0x01] in lib0 LEB128.
	v, n, err := readVarUint([]byte{0xc8, 0x01, 0x99})
	if err != nil {
		t.Fatalf("readVarUint err: %v", err)
	}
	if v != 200 {
		t.Fatalf("readVarUint value = %d, want 200", v)
	}
	if n != 2 {
		t.Fatalf("readVarUint consumed = %d, want 2", n)
	}
}

// --- Room fan-out (pump model) ---------------------------------------------

func TestBroadcastFansOutToOthersNotSender(t *testing.T) {
	hub := NewHub()
	connA, connB, connC := newFakeConn(), newFakeConn(), newFakeConn()
	memA, room := hub.Join("draft-1", connA)
	hub.Join("draft-1", connB)
	hub.Join("draft-1", connC)
	t.Cleanup(func() { hub.Leave("draft-1", memA, room) })

	frame := []byte{MessageSync, 0x02, 0x42}
	room.Broadcast(frame, memA)

	// Both other members receive exactly one frame (delivered async by the pump).
	for name, c := range map[string]*fakeConn{"B": connB, "C": connC} {
		if !waitFor(t, time.Second, func() bool { return len(c.got()) == 1 }) {
			t.Fatalf("member %s received %d frames, want 1", name, len(c.got()))
		}
		if string(c.got()[0]) != string(frame) {
			t.Fatalf("member %s received %v, want %v", name, c.got()[0], frame)
		}
	}
	// Sender does NOT receive its own frame (give the pump a beat to be sure).
	time.Sleep(50 * time.Millisecond)
	if len(connA.got()) != 0 {
		t.Fatalf("sender received its own broadcast: %v", connA.got())
	}
}

// TestBackpressureEvictsWedgedPeerWithoutBlockingRoom is the review's required
// backpressure test: a peer whose WriteMessage BLOCKS forever (full send buffer)
// must NOT stall the room. The wedged peer's send queue fills and it gets
// evicted; a healthy peer still receives every frame, and the broadcasting call
// never blocks.
func TestBackpressureEvictsWedgedPeerWithoutBlockingRoom(t *testing.T) {
	hub := NewHub()
	sender := newFakeConn()
	healthy := newFakeConn()
	wedged := newFakeConn()
	wedged.block = make(chan struct{}) // never closed → its write pump wedges forever

	memSender, room := hub.Join("draft-1", sender)
	hub.Join("draft-1", healthy)
	hub.Join("draft-1", wedged)
	t.Cleanup(func() {
		close(wedged.block) // let the wedged pump drain + exit at teardown
		hub.Leave("draft-1", memSender, room)
	})

	// Broadcast more frames than the wedged peer's buffer can hold. The wedged
	// pump is stuck on its first (blocked) write, so its buffer fills; subsequent
	// enqueues fail and evict it. None of this blocks the broadcasting goroutine.
	done := make(chan struct{})
	go func() {
		for i := 0; i < sendBuffer+50; i++ {
			room.Broadcast([]byte{MessageSync, 0x02, byte(i % 256)}, memSender)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Broadcast blocked on a wedged peer — head-of-line blocking not fixed")
	}

	// The healthy peer received frames (fan-out completed despite the wedged peer).
	if !waitFor(t, 2*time.Second, func() bool { return len(healthy.got()) > 0 }) {
		t.Fatal("healthy peer received no frames — room was blocked by the wedged peer")
	}

	// The wedged peer was evicted from the room.
	if !waitFor(t, 2*time.Second, func() bool { return hub.MemberCount("draft-1") <= 2 }) {
		t.Fatalf("wedged peer not evicted: room still has %d members", hub.MemberCount("draft-1"))
	}
}

func TestWritePumpClosesConnOnEvict(t *testing.T) {
	hub := NewHub()
	connA := newFakeConn()
	connB := newFakeConn()
	// B accepts one write then fails — its pump exits and closes the conn. (A
	// failing write is the simplest deterministic "this peer is gone" trigger;
	// the blocking/wedged path is covered by the backpressure test.)
	connB.failOn = 0

	memA, room := hub.Join("draft-1", connA)
	hub.Join("draft-1", connB)
	t.Cleanup(func() { hub.Leave("draft-1", memA, room) })

	room.Broadcast([]byte{MessageSync, 0x02, 0x01}, memA)

	// B's pump hits the write error and closes its conn.
	if !waitFor(t, 2*time.Second, func() bool { return connB.isClosed() }) {
		t.Fatal("member whose write failed never had its conn closed by the write pump")
	}
}

// --- Room lifecycle --------------------------------------------------------

func TestLastClientLeaveTearsDownRoom(t *testing.T) {
	hub := NewHub()
	connA, connB := newFakeConn(), newFakeConn()
	memA, roomA := hub.Join("draft-1", connA)
	memB, roomB := hub.Join("draft-1", connB)

	if hub.RoomCount() != 1 {
		t.Fatalf("expected 1 room, got %d", hub.RoomCount())
	}

	if remaining := hub.Leave("draft-1", memA, roomA); remaining != 1 {
		t.Fatalf("after A leaves, remaining = %d, want 1", remaining)
	}
	if hub.RoomCount() != 1 {
		t.Fatalf("room torn down too early: %d rooms", hub.RoomCount())
	}

	if remaining := hub.Leave("draft-1", memB, roomB); remaining != 0 {
		t.Fatalf("after B leaves, remaining = %d, want 0 (last-client-leave)", remaining)
	}
	if hub.RoomCount() != 0 {
		t.Fatalf("room not torn down on last leave: %d rooms", hub.RoomCount())
	}
	// Both members' pumps should close their conns once Leave signals them.
	if !waitFor(t, time.Second, func() bool { return connA.isClosed() && connB.isClosed() }) {
		t.Fatal("member conns not closed after Leave")
	}
}

func TestSeparateDraftsGetSeparateRooms(t *testing.T) {
	hub := NewHub()
	connA, connB := newFakeConn(), newFakeConn()
	memA, roomA := hub.Join("draft-1", connA)
	memB, roomB := hub.Join("draft-2", connB)
	t.Cleanup(func() {
		hub.Leave("draft-1", memA, roomA)
		hub.Leave("draft-2", memB, roomB)
	})

	// A frame broadcast in draft-1 must never reach draft-2's member.
	roomA.Broadcast([]byte{MessageSync, 0x02, 0x07}, memA)
	time.Sleep(50 * time.Millisecond)
	if len(connB.got()) != 0 {
		t.Fatalf("cross-draft leakage: draft-2 member received %v", connB.got())
	}
	if hub.RoomCount() != 2 {
		t.Fatalf("expected 2 rooms, got %d", hub.RoomCount())
	}
}
