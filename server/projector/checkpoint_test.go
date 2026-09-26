// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"strings"
	"testing"
)

// The checkpoint's offset metadata carries an unresolved divergence across a
// restart (feedback §2), and reads back as it was written.
func TestCheckpointMetaRoundTripsADivergence(t *testing.T) {
	d := &Divergence{Tick: 5}
	d.Recorded[0], d.Replayed[31] = 0xab, 0xcd
	for _, cp := range []Checkpoint{{Tick: 41}, {Tick: 4, Diverged: d}} {
		meta := checkpointMeta(cp)
		if len(meta) > 4096 {
			t.Fatalf("metadata is %d bytes, over the broker's default offset.metadata.max.bytes", len(meta))
		}
		tick, got, err := parseCheckpointMeta(meta)
		if err != nil || tick != cp.Tick {
			t.Fatalf("%q: tick %d, err %v; want %d", meta, tick, err, cp.Tick)
		}
		if (got == nil) != (cp.Diverged == nil) {
			t.Fatalf("%q: divergence %v, want %v", meta, got, cp.Diverged)
		}
		if got != nil && (got.Tick != d.Tick || got.Recorded != d.Recorded || got.Replayed != d.Replayed) {
			t.Fatalf("%q: read back %+v, wrote %+v", meta, got, d)
		}
	}
	for _, bad := range []string{"", "offset=4", "tick=x", "tick=4;diverged=5", "tick=4;diverged=5:zz:00", "tick=4;diverged=5:ab:cd"} {
		if _, _, err := parseCheckpointMeta(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if !strings.HasPrefix(checkpointMeta(Checkpoint{Tick: 7}), "tick=7") {
		t.Error("a plain checkpoint's metadata changed shape")
	}
}
