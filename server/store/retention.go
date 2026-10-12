// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/sim"
)

// Retention is the sweep's policy (AW-SRV-055; lifecycle.md, "Round tags").
type Retention struct {
	// KeepRounds is snapshot.keep_rounds: the newest untagged rounds kept.
	KeepRounds int
	// KeepTagged is snapshot.keep_deploy_rounds: the newest tagged rounds
	// kept. It counts rounds, not minutes.
	KeepTagged int
	// MaxAge is snapshot.max_round_age: a round older than it is deleted,
	// tagged or not. Zero keeps rounds of any age.
	MaxAge time.Duration
	// Pin is recovery.pin_round, 0 for none: every round at that tick is
	// kept whatever its age or tag count.
	Pin sim.Tick
}

// SweepResult is what one sweep did. Every round the store held is in exactly
// one of the counts.
type SweepResult struct {
	// Deleted rounds, each with every object and tag it had.
	Deleted int
	// KeptTagged and KeptUntagged are the rounds left, by whether any of
	// their objects carries a tag: andara_snapshot_rounds_retained.
	KeptTagged   int
	KeptUntagged int
	// NewestComplete is the tick of the round kept as the newest complete
	// one, 0 when there is none.
	NewestComplete sim.Tick
}

// Sweeper runs the retention sweep. It remembers each round's taken_at, read
// once from one of its objects, because a round's objects never change and a
// sweep after every snapshot round would otherwise reread the same envelopes.
//
// Tagging and sweeping must not interleave: a round tagged after a sweep
// listed it untagged would be deleted with its new tag. Lock is the lock both
// take; TagRound callers in the same process hold it too.
type Sweeper struct {
	sync.Mutex

	takenAt    map[roundKey]int64
	incomplete map[roundKey]bool
	listed     string
}

// NewSweeper returns a Sweeper with nothing remembered.
func NewSweeper() *Sweeper {
	return &Sweeper{takenAt: map[roundKey]int64{}, incomplete: map[roundKey]bool{}}
}

// Sweep applies p to the rounds of the listed Zones at now, deleting what it
// doesn't keep. The caller holds the Sweeper's lock.
//
// Newest first, each round is kept or deleted by the first rule that applies:
// the newest complete round and the pinned round are kept; a round older than
// MaxAge is deleted; a tagged round (a tag on any of its objects) is kept while
// fewer than KeepTagged tagged rounds have been, an untagged one while fewer
// than KeepRounds untagged ones have been; anything else is deleted. A round's
// tags go before its objects, so a sweep interrupted part-way leaves a round
// it decides about the same way next time.
//
// A store error stops the sweep and is returned with what it had done.
func (s *Sweeper) Sweep(ctx context.Context, ws TagStore, listed []sim.ZoneID, zonesAt ZonesAt, p Retention, now time.Time) (SweepResult, error) {
	var res SweepResult
	rounds, err := discover(ctx, ws, listed)
	if err != nil {
		return res, err
	}
	tags, err := attachTags(ctx, ws, listed, rounds)
	if err != nil {
		return res, err
	}
	newest, err := s.newestComplete(ctx, ws, listed, zonesAt, rounds)
	if err != nil {
		return res, err
	}
	if newest >= 0 {
		res.NewestComplete = rounds[newest].Tick
	}
	live := make(map[roundKey]bool, len(rounds))
	for i, r := range rounds {
		rk := roundKey{r.Tick, r.StateVersion}
		live[rk] = true
		keep := i == newest || (p.Pin != 0 && r.Tick == p.Pin)
		if !keep {
			if r.Tagged {
				keep = res.KeptTagged < p.KeepTagged
			} else {
				keep = res.KeptUntagged < p.KeepRounds
			}
			// Dated only when the count would keep it: the rounds past
			// the count go whatever their age, unread.
			if keep && p.MaxAge > 0 && s.older(ctx, ws, r, now, p.MaxAge) {
				keep = false
			}
		}
		if keep {
			if r.Tagged {
				res.KeptTagged++
			} else {
				res.KeptUntagged++
			}
			continue
		}
		if err := deleteRound(ctx, ws, r, tags); err != nil {
			return res, err
		}
		res.Deleted++
		delete(live, rk)
	}
	for rk := range s.takenAt {
		if !live[rk] {
			delete(s.takenAt, rk)
		}
	}
	for rk := range s.incomplete {
		if !live[rk] {
			delete(s.incomplete, rk)
		}
	}
	return res, nil
}

// newestComplete is the index in rounds of the newest complete round, -1 for
// none. It reads rounds newest first until one is complete, and remembers the
// ones that weren't, except the newest, which may be a round still being
// written: a written round never changes, so a store with no complete round
// is read once, not on every sweep. What it remembers is for one set of
// listed Zones.
func (s *Sweeper) newestComplete(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID, zonesAt ZonesAt, rounds []Round) (int, error) {
	if l := fmt.Sprint(listed); l != s.listed {
		s.listed, s.incomplete = l, map[roundKey]bool{}
	}
	for i := range rounds {
		rk := roundKey{rounds[i].Tick, rounds[i].StateVersion}
		if s.incomplete[rk] {
			continue
		}
		// verify on a copy: it fills in the Zones it reads.
		r := rounds[i]
		r.Zones = append([]ZoneSnapshotRef(nil), rounds[i].Zones...)
		if _, err := verify(ctx, ws, listed, zonesAt, &r); err != nil {
			return -1, err
		}
		if r.Complete {
			return i, nil
		}
		// Cause content can change its mind: the source may learn the
		// version later.
		if i > 0 && r.Cause != causeContent {
			s.incomplete[rk] = true
		}
	}
	return -1, nil
}

// older reports whether r was taken more than maxAge before now. A round whose
// taken_at can't be read is not older: age alone deletes only what it can
// date.
func (s *Sweeper) older(ctx context.Context, ws sim.WorldStore, r Round, now time.Time, maxAge time.Duration) bool {
	rk := roundKey{r.Tick, r.StateVersion}
	at, ok := s.takenAt[rk]
	if !ok {
		at = takenAt(ctx, ws, r)
		if at != 0 {
			s.takenAt[rk] = at
		}
	}
	return at != 0 && now.Sub(time.Unix(0, at)) > maxAge
}

// takenAt reads the newest taken_at_unix_nano of r's objects from their
// envelopes alone, so it dates a round this binary can't otherwise read. It
// stops at the first object that has one: a round's objects are one cut.
func takenAt(ctx context.Context, ws sim.WorldStore, r Round) int64 {
	for _, ref := range r.Zones {
		raw, err := ws.Get(ctx, ref.Key)
		if err != nil {
			continue
		}
		var env statev1.SnapshotEnvelope
		if proto.Unmarshal(raw, &env) != nil {
			continue
		}
		if t := env.GetTakenAtUnixNano(); t != 0 {
			return t
		}
	}
	return 0
}

// deleteRound deletes r's tags, then its objects.
func deleteRound(ctx context.Context, ws TagStore, r Round, tags map[string][]string) error {
	for _, ref := range r.Zones {
		for _, n := range tags[ref.Key] {
			if err := ws.Delete(ctx, TagKey(ref.Key, n)); err != nil {
				return fmt.Errorf("store: delete round %d: %w", r.Tick, err)
			}
		}
	}
	for _, ref := range r.Zones {
		if err := ws.Delete(ctx, ref.Key); err != nil && !errors.Is(err, sim.ErrSnapshotNotFound) {
			return fmt.Errorf("store: delete round %d: %w", r.Tick, err)
		}
	}
	return nil
}
