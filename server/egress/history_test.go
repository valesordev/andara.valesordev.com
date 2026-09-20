// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress

import "testing"

// The resume window at, inside, and beyond its boundary, and the fresh
// and unknown resume points.
func TestHistory_Resume(t *testing.T) {
	h := newHistory(4)
	if seq, reason := h.resume(0); seq != 0 || reason != "" {
		t.Fatalf("fresh on empty: %d %q", seq, reason)
	}
	if _, reason := h.resume(7); reason != ResyncNoHistory {
		t.Fatalf("unknown on empty: %q", reason)
	}
	for id := uint64(10); id <= 16; id += 2 { // 10 12 14 16: 10 evicted by 18
		h.append(frame{id: id})
	}
	h.append(frame{id: 18}) // window now 12 14 16 18; evicted 10
	cases := []struct {
		last   uint64
		seq    uint64
		reason string
	}{
		{0, h.end(), ""},  // fresh: from now
		{10, 1, ""},       // at the boundary: everything retained follows
		{12, 2, ""},       // inside
		{13, 2, ""},       // between retained IDs: the next one
		{18, h.end(), ""}, // caught up: nothing to replay
		{9, h.end(), ResyncWindowExceeded},
		{19, h.end(), ResyncNoHistory}, // never sent
	}
	for _, c := range cases {
		seq, reason := h.resume(c.last)
		if seq != c.seq || reason != c.reason {
			t.Errorf("resume(%d) = %d %q, want %d %q", c.last, seq, reason, c.seq, c.reason)
		}
	}
	if h.at(1).id != 12 || h.at(4).id != 18 {
		t.Errorf("slots: at(1)=%d at(4)=%d", h.at(1).id, h.at(4).id)
	}
	// A lifecycle frame moves neither bound and is skipped over by a
	// resume.
	h.append(frame{id: 0})
	if h.newest != 18 || h.evicted != 12 {
		t.Errorf("after id 0: newest %d evicted %d", h.newest, h.evicted)
	}
	if seq, _ := h.resume(18); seq != h.end() {
		t.Errorf("resume past a lifecycle frame = %d, want %d", seq, h.end())
	}
	h.reset()
	if _, reason := h.resume(18); reason != ResyncNoHistory {
		t.Errorf("after reset: %q", reason)
	}
}
