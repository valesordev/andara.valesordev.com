// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import (
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// frame is one thing a stream sends: an Event as delivered to this
// Session, or a lifecycle notice (event_id 0).
type frame struct {
	id  uint64
	typ string
	env *gamev1.EventEnvelope
}

// history is a Session's retained deliveries: the last window frames
// appended, each at a sequence number that only grows, so a stream is a
// cursor and a resume is a search. Not safe for concurrent use; the
// session's lock covers it.
type history struct {
	buf   []frame
	start uint64 // seq of buf's oldest frame
	n     int
	// evicted is the ID of the newest Event that has fallen out of the
	// window, and newest the ID of the newest appended: the two bounds a
	// resume is checked against. Lifecycle frames (ID 0) count for neither.
	evicted uint64
	newest  uint64
}

func newHistory(window int) history {
	return history{buf: make([]frame, window)}
}

// end is the seq the next frame will take: where a fresh stream starts.
func (h *history) end() uint64 { return h.start + uint64(h.n) }

// at is the frame at seq, which must be in [start, end). A seq's slot
// never moves, so eviction is a bound moving, not a copy.
func (h *history) at(seq uint64) frame {
	return h.buf[seq%uint64(len(h.buf))]
}

// append retains f, evicting the oldest once the window is full.
func (h *history) append(f frame) {
	if h.n == len(h.buf) {
		old := h.at(h.start)
		if old.id != 0 {
			h.evicted = old.id
		}
		h.start++
		h.n--
	}
	h.buf[h.end()%uint64(len(h.buf))] = f
	h.n++
	if f.id != 0 {
		h.newest = f.id
	}
}

// reset forgets everything: a Session whose perception changed has no
// history a client could resume against.
func (h *history) reset() {
	h.start = h.end()
	h.n = 0
	h.evicted, h.newest = 0, 0
}

// resume finds where a stream that last saw Event last continues: the seq
// of the first retained Event after it, and an empty reason. When the
// window no longer reaches back to last, or nothing this Session was
// sent accounts for it, the seq is end and the reason says which — the
// stream opens with a Resync and runs live.
func (h *history) resume(last uint64) (uint64, string) {
	switch {
	case last == 0:
		return h.end(), ""
	case last > h.newest:
		return h.end(), ResyncNoHistory
	case last < h.evicted:
		return h.end(), ResyncWindowExceeded
	}
	for seq := h.start; seq < h.end(); seq++ {
		if f := h.at(seq); f.id > last {
			return seq, ""
		}
	}
	return h.end(), ""
}
