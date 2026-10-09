// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valesordev/andara/server/sim"
)

// The causes of a degraded Partition, and the names a probe gives its
// findings (docs/specs/kafka/client-contract.md, "Degraded state is per
// Partition"). A produce error's name is the broker's protocol name.
const (
	CauseProduceError = "produce_error"
	CauseProbe        = "probe"

	ErrNameLeaderAbsent        = "leader_absent"
	ErrNameISRBelowMin         = "isr_below_min"
	ErrNameMetadataUnavailable = "metadata_unavailable"
)

// MinDegradedHold is the floor of ingress.degraded_hold: WorldReadOnly's
// for: (30 s) twice over, plus 30 s. Config load fails below it.
const MinDegradedHold = 90 * time.Second

// DefaultDegradedHold is ingress.degraded_hold's default.
const DefaultDegradedHold = 120 * time.Second

// partitionView is what the probe's metadata says of one Partition.
type partitionView struct {
	Leader int32 // -1: none
	ISR    int   // the in-sync replica count
}

// topicView is a metadata answer for the Commands topic.
type topicView struct {
	// Missing: the broker answered but does not know the topic.
	Missing    bool
	Partitions map[int32]partitionView
}

// partitionHealth is the per-Partition read-only state (AW-SRV-052): which
// of the 64 Partitions cannot take writes, and why. A Partition is degraded
// while either mark stands:
//
//   - a produce mark, set when a produce to it failed with a broker error
//     after the client's own retries; it holds for the hold, measured from
//     the most recent such mark, and is cleared by the first healthy probe
//     after that, because metadata can show a full ISR while the broker is
//     still refusing writes;
//   - a probe mark, set from topic metadata (a leader absent on two
//     consecutive probes, an ISR below min.insync.replicas, or no metadata
//     answer on two consecutive probes) and cleared by the first probe that
//     finds the Partition healthy.
//
// Degraded is a lock-free read: the Submit path asks it for every Command.
type partitionHealth struct {
	now    func() time.Time
	hold   time.Duration
	notify func(ctx context.Context, partition int32, degraded bool, cause, name string)

	flags [sim.PartitionCount]atomic.Bool

	mu           sync.Mutex
	state        [sim.PartitionCount]partitionState
	metadataMiss int
}

type partitionState struct {
	produceAt   time.Time // the most recent produce-error mark; zero if none
	produceName string
	probeName   string // the probe mark's finding; empty if none
	leaderMiss  int    // consecutive probes that found no leader
	degraded    bool
	cause, name string // what entered the state, for the line that leaves it
}

// newPartitionHealth builds the state. notify is called once per
// transition, outside the lock: with degraded true and the cause and name
// that entered it, or false and those that held it.
func newPartitionHealth(now func() time.Time, hold time.Duration, notify func(context.Context, int32, bool, string, string)) *partitionHealth {
	return &partitionHealth{now: now, hold: hold, notify: notify}
}

// Degraded reports whether the Partition cannot take writes.
func (h *partitionHealth) Degraded(p int32) bool {
	if p < 0 || int(p) >= len(h.flags) {
		return false
	}
	return h.flags[p].Load()
}

type transition struct {
	partition int32
	degraded  bool
	cause     string
	name      string
}

// MarkProduceError marks a Partition whose produce failed with the broker's
// error name, and restarts its hold.
func (h *partitionHealth) MarkProduceError(ctx context.Context, p int32, name string) {
	if p < 0 || int(p) >= len(h.flags) {
		return
	}
	h.mu.Lock()
	s := &h.state[p]
	s.produceAt, s.produceName = h.now(), name
	t := h.settleLocked(p)
	h.mu.Unlock()
	h.fire(ctx, t...)
}

// ObserveProbe applies one probe: a topic view, or nil when the metadata
// request failed. minISR is the topic's min.insync.replicas.
func (h *partitionHealth) ObserveProbe(ctx context.Context, v *topicView, minISR int) {
	h.mu.Lock()
	var changes []transition
	if v == nil {
		h.metadataMiss++
		if h.metadataMiss >= 2 {
			for p := range int32(sim.PartitionCount) {
				h.state[p].probeName = ErrNameMetadataUnavailable
				changes = append(changes, h.settleLocked(p)...)
			}
		}
	} else {
		h.metadataMiss = 0
		for p := range int32(sim.PartitionCount) {
			s := &h.state[p]
			pv, listed := v.Partitions[p]
			if v.Missing {
				pv, listed = partitionView{Leader: -1}, true
			}
			if !listed {
				// The topic has no such Partition: nothing to judge.
				s.leaderMiss, s.probeName = 0, ""
				if !s.produceAt.IsZero() && h.now().Sub(s.produceAt) >= h.hold {
					s.produceAt, s.produceName = time.Time{}, ""
				}
				changes = append(changes, h.settleLocked(p)...)
				continue
			}
			if pv.Leader < 0 {
				s.leaderMiss++
			} else {
				s.leaderMiss = 0
			}
			switch {
			case s.leaderMiss >= 2:
				s.probeName = ErrNameLeaderAbsent
			case pv.Leader >= 0 && pv.ISR < minISR:
				s.probeName = ErrNameISRBelowMin
			case s.leaderMiss == 1:
				// One probe without a leader marks nothing, and does not
				// clear a mark already standing.
			default:
				s.probeName = ""
				// A healthy probe after the hold ends a produce mark.
				if !s.produceAt.IsZero() && h.now().Sub(s.produceAt) >= h.hold {
					s.produceAt, s.produceName = time.Time{}, ""
				}
			}
			changes = append(changes, h.settleLocked(p)...)
		}
	}
	h.mu.Unlock()
	h.fire(ctx, changes...)
}

// settleLocked recomputes a Partition's degraded flag and returns the
// transition, if any.
func (h *partitionHealth) settleLocked(p int32) []transition {
	s := &h.state[p]
	now := !s.produceAt.IsZero() || s.probeName != ""
	if now == s.degraded {
		return nil
	}
	s.degraded = now
	h.flags[p].Store(now)
	if !now {
		return []transition{{p, false, s.cause, s.name}}
	}
	s.cause, s.name = CauseProbe, s.probeName
	if !s.produceAt.IsZero() {
		s.cause, s.name = CauseProduceError, s.produceName
	}
	return []transition{{p, true, s.cause, s.name}}
}

func (h *partitionHealth) fire(ctx context.Context, ts ...transition) {
	for _, t := range ts {
		if h.notify != nil {
			h.notify(ctx, t.partition, t.degraded, t.cause, t.name)
		}
	}
}

// produceErrorNames are the broker errors that mark a Partition when a
// produce to it fails after the client's retries, by protocol name.
var produceErrorNames = []string{
	"NOT_ENOUGH_REPLICAS",
	"NOT_ENOUGH_REPLICAS_AFTER_APPEND",
	"LEADER_NOT_AVAILABLE",
	"NOT_LEADER_OR_FOLLOWER",
	"REQUEST_TIMED_OUT",
}
