// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"sort"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// The handoff retry schedule's defaults (AW-SRV-028): the first interval, the
// longest, and the most Arrives one DueHandoffs call returns.
const (
	DefaultHandoffRetryTicks    Tick = 10
	DefaultHandoffRetryMaxTicks Tick = 100
	DefaultHandoffRetryBatch         = 50
)

// maxBackoffShift saturates the backoff exponent, so a handoff stuck for a
// very long time never wraps a shift to a zero interval.
const maxBackoffShift = 16

// handoffKey names one handoff of one Entity.
type handoffKey struct {
	id  EntityID
	seq uint64
}

// handoffSched is when an Arrive was last produced for a handoff and how many
// have been: the departure's is the first.
type handoffSched struct {
	attempts int
	last     Tick
}

// HandoffRetry is one Arrive DueHandoffs produced again: the Command, and what
// the loop's counter and log line need. Attempt is the number of Arrives
// produced for the handoff, this one included.
type HandoffRetry struct {
	Command *logv1.LoggedCommand
	Entity  EntityID
	From    ZoneID
	To      ZoneID
	Seq     uint64
	Attempt int
}

func (e *Engine) retryTicks() Tick {
	if e.cfg.HandoffRetryTicks > 0 {
		return e.cfg.HandoffRetryTicks
	}
	return DefaultHandoffRetryTicks
}

func (e *Engine) retryMaxTicks() Tick {
	if e.cfg.HandoffRetryMaxTicks > 0 {
		return e.cfg.HandoffRetryMaxTicks
	}
	return DefaultHandoffRetryMaxTicks
}

func (e *Engine) retryBatch() int {
	if e.cfg.HandoffRetryBatch > 0 {
		return e.cfg.HandoffRetryBatch
	}
	return DefaultHandoffRetryBatch
}

// retryInterval is the ticks to wait after the n-th Arrive: the first
// interval doubled for each attempt after the first, up to the maximum. The
// exponent saturates, so no attempt count wraps the interval to zero.
func (e *Engine) retryInterval(attempts int) Tick {
	first, longest := e.retryTicks(), e.retryMaxTicks()
	if longest < first {
		longest = first
	}
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > maxBackoffShift {
		shift = maxBackoffShift
	}
	interval := first << shift
	if interval > longest || interval < first {
		return longest
	}
	return interval
}

// noteHandoff records the departure as the handoff's first attempt, so the
// Arrive the departure produced isn't re-sent on the same tick.
func (e *Engine) noteHandoff(id EntityID, seq uint64, tick Tick) {
	if e == nil || e.replaying {
		// Nothing while replaying: a record whose departure is replayed has no
		// entry, like one restored from a snapshot, so every record found after
		// a recovery is due on the first live call.
		return
	}
	e.handoffs[handoffKey{id, seq}] = handoffSched{attempts: 1, last: tick}
}

// dropTransit removes an Entity's transit record from z and the schedule entry
// with it, so the map holds one entry per record in transit.
func (e *Engine) dropTransit(z *ZoneState, id EntityID) {
	rec, ok := z.Transit[id]
	if !ok {
		return
	}
	delete(z.Transit, id)
	if e != nil {
		delete(e.handoffs, handoffKey{id, rec.Entity.HandoffSeq})
	}
}

// DueHandoffs is the retry pass (AW-SRV-028): the Arrives due for the records
// in transit at tick, at most the batch size, earliest due first and then in
// Entity-ID order, so a restart with many in-flight handoffs doesn't fill a
// tick's input budget and defer players' Commands. The live tick loop calls it
// after Step and produces what it returns. It is never called by replay:
// recovery and the state projector run the same Step as the live loop through
// ReplayEach, so the pass lives outside Step, and replay produces no retries,
// counts none and logs none.
//
// A record with no schedule entry is due, which is what a recovery leaves; the
// retry writes (1, tick) for it, so a restart resets the backoff. A record
// whose Arrive was last produced at t is due at t + the interval after its
// attempts so far: the first interval doubling for each attempt after the
// first, up to the maximum. Nothing is produced for a Zone that is faulted or
// whose Partition is frozen (AW-SRV-002): the acknowledgement couldn't be
// applied.
func (e *Engine) DueHandoffs(tick Tick) []HandoffRetry {
	type due struct {
		at  Tick
		rec TransitRecord
		zid ZoneID
	}
	frozen := map[int32]bool{}
	for _, p := range e.FaultedPartitions() {
		frozen[p] = true
	}
	var dues []due
	for _, zid := range e.state.SortedZoneIDs() {
		z := e.state.Zones[zid]
		if z.Faulted || frozen[PartitionFor(zid)] {
			continue
		}
		for id, rec := range z.Transit {
			at := Tick(0)
			if s, ok := e.handoffs[handoffKey{id, rec.Entity.HandoffSeq}]; ok {
				at = s.last + e.retryInterval(s.attempts)
				if tick < at {
					continue
				}
			}
			dues = append(dues, due{at: at, rec: rec, zid: zid})
		}
	}
	sort.Slice(dues, func(i, j int) bool {
		if dues[i].at != dues[j].at {
			return dues[i].at < dues[j].at
		}
		if dues[i].rec.Entity.ID != dues[j].rec.Entity.ID {
			return dues[i].rec.Entity.ID < dues[j].rec.Entity.ID
		}
		return dues[i].zid < dues[j].zid
	})
	if n := e.retryBatch(); len(dues) > n {
		dues = dues[:n]
	}
	out := make([]HandoffRetry, 0, len(dues))
	for _, d := range dues {
		key := handoffKey{d.rec.Entity.ID, d.rec.Entity.HandoffSeq}
		attempt := 1
		if s, ok := e.handoffs[key]; ok {
			attempt = s.attempts + 1
		}
		e.handoffs[key] = handoffSched{attempts: attempt, last: tick}
		out = append(out, HandoffRetry{
			// No session, client ref or trace: a retry starts a trace of its
			// own, and the loop adds entity_id to it.
			Command: &logv1.LoggedCommand{
				ZoneId:  string(d.rec.To),
				ActorId: string(d.rec.Entity.ID),
				Command: &logv1.LoggedCommand_Arrive{Arrive: arriveFor(d.zid, d.rec)},
			},
			Entity: d.rec.Entity.ID, From: d.zid, To: d.rec.To, Seq: d.rec.Entity.HandoffSeq, Attempt: attempt,
		})
	}
	return out
}

// HandoffsInTransit is how many Entities are in transit across every Zone, for
// andara_handoffs_in_transit. Read on the loop goroutine only.
func (e *Engine) HandoffsInTransit() int {
	n := 0
	for _, z := range e.state.Zones {
		n += len(z.Transit)
	}
	return n
}

// HandoffPlacedEntries is how many handoff marks every Zone holds, for
// andara_handoff_placed_entries: they are kept for good, so the gauge is what
// says pruning is wanted. Read on the loop goroutine only.
func (e *Engine) HandoffPlacedEntries() int {
	n := 0
	for _, z := range e.state.Zones {
		n += len(z.Placed)
	}
	return n
}

// HandoffScheduleSize is how many handoffs the retry schedule tracks: one per
// Entity in transit whose departure or retry this process has seen. It is
// what a test reads to check an entry is deleted when its record is dropped.
func (e *Engine) HandoffScheduleSize() int { return len(e.handoffs) }
