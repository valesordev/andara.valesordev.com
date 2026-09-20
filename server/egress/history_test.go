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
	if len(h.buf) != 0 {
		t.Fatal("an empty history holds slots")
	}
	if _, reason := h.resume(7); reason != ResyncNoHistory {
		t.Fatalf("unknown on empty: %q", reason)
	}
	for id := uint64(10); id <= 16; id += 2 { // 10 12 14 16: 10 evicted by 18
		h.append(frame{id: id})
	}
	if len(h.buf) != 4 || h.n != 4 {
		t.Fatalf("after four: len %d n %d", len(h.buf), h.n)
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
	if len(h.buf) != 0 {
		t.Error("reset kept the ring's memory")
	}
	// Growing again from a non-zero base: slots follow the seq.
	end := h.end()
	for id := uint64(20); id <= 30; id += 2 {
		h.append(frame{id: id})
	}
	// 20 and 22 evicted; 24 26 28 30 retained at end+2 .. end+5.
	if h.start != end+2 || h.at(end+2).id != 24 || h.at(end+5).id != 30 || h.evicted != 22 {
		t.Errorf("after regrowth: start=%d (end %d) at(end+2)=%d at(end+5)=%d evicted=%d", h.start, end, h.at(end+2).id, h.at(end+5).id, h.evicted)
	}
	if seq, _ := h.resume(24); seq != end+3 {
		t.Errorf("resume(24) after regrowth = %d, want %d", seq, end+3)
	}
}
