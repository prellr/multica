// Package draftyjs implements the server side of the Drafts Yjs co-editing
// floor (slice 3a). The server is a DUMB RELAY: it never decodes the CRDT. It
// reads only the single leading varUint of each frame — the y-websocket
// envelope type — to tell a sync update (relay + persist) apart from an
// awareness update (relay, never persist). Everything after that byte is
// treated as opaque bytes.
//
// Wire format (matches the de-facto y-websocket envelope, which the hand-rolled
// client provider in packages/core/drafts/collab speaks):
//
//	[ messageType: varUint ][ payload... ]
//
// where messageType is one of:
//
//	MessageSync      (0) — payload is a y-protocols/sync sub-message
//	                       ([ syncType: varUint ][ ... ]). The relay forwards
//	                       the WHOLE frame to peers and appends it to the log.
//	MessageAwareness (1) — payload is a y-protocols/awareness update. Forwarded
//	                       to peers; NEVER persisted (ephemeral presence).
//
// The relay deliberately does NOT inspect the sync sub-type. Persisting the
// occasional SyncStep1/SyncStep2 frame alongside the Update frames is harmless:
// replaying any of them into a fresh Y.Doc is idempotent (a SyncStep2 is itself
// a full update). Keeping the server out of the sync sub-protocol is what makes
// it a true dumb relay and keeps the door open to a pg_crdt swap later.
package draftyjs

import "errors"

const (
	// MessageSync wraps a y-protocols/sync sub-message. These frames carry
	// document content and are the ones persisted to draft_yjs_update.
	MessageSync = 0
	// MessageAwareness wraps a y-protocols/awareness update — ephemeral peer
	// presence (cursors/selections). Relayed but never persisted.
	MessageAwareness = 1
)

// errEmptyFrame is returned when a frame has no leading varUint to classify.
var errEmptyFrame = errors.New("draftyjs: empty frame")

// readVarUint reads a single lib0/LEB128 variable-length unsigned integer from
// the front of buf, returning the value and the number of bytes consumed. This
// is the ONLY decoding the relay does — just enough to read the envelope type.
func readVarUint(buf []byte) (value uint64, n int, err error) {
	var mult uint64 = 1
	for i := 0; i < len(buf); i++ {
		b := buf[i]
		value += uint64(b&0x7f) * mult
		mult *= 128
		if b < 0x80 {
			return value, i + 1, nil
		}
	}
	return 0, 0, errEmptyFrame
}

// messageType returns the y-websocket envelope type of a frame (the leading
// varUint) without decoding the payload. A frame the relay cannot classify
// (empty or truncated) returns ok=false; the caller treats it as a no-op.
func messageType(frame []byte) (msgType uint64, ok bool) {
	v, _, err := readVarUint(frame)
	if err != nil {
		return 0, false
	}
	return v, true
}

// IsSyncFrame reports whether frame is a sync (document-content) frame, i.e.
// one the relay must persist. Awareness and unclassifiable frames return false.
func IsSyncFrame(frame []byte) bool {
	t, ok := messageType(frame)
	return ok && t == MessageSync
}
