package draftyjs

import (
	"sync"
	"testing"
)

// fakeConn records every frame written to it. WriteMessage is the only surface
// the Room uses; this lets the room hub be tested with no real WebSocket.
type fakeConn struct {
	mu     sync.Mutex
	frames [][]byte
	failOn int // after this many successful writes, return an error (-1 = never)
	writes int
}

func newFakeConn() *fakeConn { return &fakeConn{failOn: -1} }

func (c *fakeConn) WriteMessage(_ int, data []byte) error {
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

func (c *fakeConn) got() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.frames))
	copy(out, c.frames)
	return out
}

type sentinelErr struct{}

func (sentinelErr) Error() string { return "fake write error" }

var errWrite = sentinelErr{}

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

// --- Room fan-out ----------------------------------------------------------

func TestBroadcastFansOutToOthersNotSender(t *testing.T) {
	hub := NewHub()
	connA, connB, connC := newFakeConn(), newFakeConn(), newFakeConn()
	memA, room := hub.Join("draft-1", connA)
	memB, _ := hub.Join("draft-1", connB)
	memC, _ := hub.Join("draft-1", connC)
	_ = memB
	_ = memC

	frame := []byte{MessageSync, 0x02, 0x42}
	room.Broadcast(frame, memA)

	// Sender does NOT receive its own frame.
	if len(connA.got()) != 0 {
		t.Fatalf("sender received its own broadcast: %v", connA.got())
	}
	// Both other members receive exactly one frame, byte-identical.
	for name, c := range map[string]*fakeConn{"B": connB, "C": connC} {
		got := c.got()
		if len(got) != 1 {
			t.Fatalf("member %s received %d frames, want 1", name, len(got))
		}
		if string(got[0]) != string(frame) {
			t.Fatalf("member %s received %v, want %v", name, got[0], frame)
		}
	}
}

func TestBroadcastEvictsDeadMember(t *testing.T) {
	hub := NewHub()
	connA := newFakeConn()
	connB := newFakeConn()
	connB.failOn = 0 // every write to B fails immediately

	memA, room := hub.Join("draft-1", connA)
	hub.Join("draft-1", connB)

	room.Broadcast([]byte{MessageSync, 0x02, 0x01}, memA)

	// B failed its write and should be evicted; a second broadcast reaches
	// only the (now sole) remaining peer set — which is empty besides A.
	room.mu.RLock()
	count := len(room.members)
	room.mu.RUnlock()
	if count != 1 {
		t.Fatalf("dead member not evicted: room has %d members, want 1 (A only)", count)
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
}

func TestSeparateDraftsGetSeparateRooms(t *testing.T) {
	hub := NewHub()
	connA, connB := newFakeConn(), newFakeConn()
	memA, roomA := hub.Join("draft-1", connA)
	memB, _ := hub.Join("draft-2", connB)

	// A frame broadcast in draft-1 must never reach draft-2's member.
	roomA.Broadcast([]byte{MessageSync, 0x02, 0x07}, memA)
	if len(connB.got()) != 0 {
		t.Fatalf("cross-draft leakage: draft-2 member received %v", connB.got())
	}
	_ = memB
	if hub.RoomCount() != 2 {
		t.Fatalf("expected 2 rooms, got %d", hub.RoomCount())
	}
}
