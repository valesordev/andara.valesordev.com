// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/valesordev/andara/server/sim"
)

// Sink receives one verified tick: its boundary and the records it rendered,
// possibly none. The Kafka sink produces them and commits the boundary's
// offsets only once they are acknowledged (the offset-commit rule).
type Sink func(b sim.TickCompleted, records []Out) error

// Divergence is AC-3's halt: the replica's State Hash after Tick is not the
// one the live server recorded. Nothing was rendered for Tick, and LastGood
// is the next-to-read offset per Partition after the last tick that did
// verify — the furthest any offset may be committed.
type Divergence struct {
	Tick     sim.Tick
	Recorded [32]byte
	Replayed [32]byte
	LastGood map[int32]int64
}

func (d *Divergence) Error() string {
	return fmt.Sprintf("state projector diverged at tick %d: recorded %x, replayed %x; last good offsets %s", d.Tick, d.Recorded, d.Replayed, d.lastGood())
}

// lastGood renders LastGood as partition:offset pairs, sorted.
func (d *Divergence) lastGood() string {
	parts := make([]int, 0, len(d.LastGood))
	for p := range d.LastGood {
		parts = append(parts, int(p))
	}
	sort.Ints(parts)
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d:%d", p, d.LastGood[int32(p)])
	}
	return b.String()
}

// Unwrap makes errors.Is(err, sim.ErrHashMismatch) hold.
func (d *Divergence) Unwrap() error { return sim.ErrHashMismatch }

// Replay applies boundaries in order through the replica's own ReplayEach,
// renders each verified tick, and hands it to sink. A hash mismatch comes
// back as *Divergence; a sink error stops the replay and comes back as is.
func (p *Projector) Replay(boundaries []sim.TickCompleted, src sim.RecordSource, sink Sink) error {
	lastGood := copyOffsets(p.e.State().Offsets)
	i := 0
	err := p.e.ReplayEach(boundaries, src, func(res sim.StepResult) error {
		b := boundaries[i]
		i++
		recs, err := p.Render(res)
		if err != nil {
			return err
		}
		if err := sink(b, recs); err != nil {
			return err
		}
		lastGood = copyOffsets(b.Offsets)
		return nil
	})
	var hm *sim.HashMismatchError
	if errors.As(err, &hm) {
		return &Divergence{Tick: hm.Tick, Recorded: hm.Recorded, Replayed: hm.Replayed, LastGood: lastGood}
	}
	return err
}

func copyOffsets(m map[int32]int64) map[int32]int64 {
	out := make(map[int32]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
