// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-055's integration case, against the object store s3Store names: a
// sweep deletes the oldest untagged rounds, every object and tag of them,
// and none of the tagged ones. Skipped without ANDARA_S3_TEST_ENDPOINT.
func TestS3SweepDeletesOldestUntaggedAndNoneTagged(t *testing.T) {
	s, _ := s3Store(t)
	ctx := context.Background()
	e, err := simtest.NewEngine(11)
	if err != nil {
		t.Fatal(err)
	}
	owned := e.State().SortedZoneIDs()
	remain := simtest.Script(12)
	var ticks []sim.Tick
	for range 5 {
		if _, err := e.Step(simtest.Batch(remain, 2)); err != nil {
			t.Fatal(err)
		}
		snaps := e.SnapshotAll(time.Now().UnixNano())
		for i := range snaps {
			b, err := snaps[i].Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Put(ctx, snaps[i].Key(), b); err != nil {
				t.Fatal(err)
			}
		}
		ticks = append(ticks, e.Tick())
	}
	if _, err := TagRoundAt(ctx, s, owned, nil, "deploy:old", ticks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := TagRoundAt(ctx, s, owned, nil, "deploy:mid", ticks[2]); err != nil {
		t.Fatal(err)
	}
	// One untagged round, plus the round TagRound's tag keys sit beside: List
	// never returns a tag key.
	keys, err := s.List(ctx, owned[0])
	if err != nil || len(keys) != 5 {
		t.Fatalf("List: %v %v", keys, err)
	}
	res, err := NewSweeper().Sweep(ctx, s, owned, nil, Retention{KeepRounds: 1, KeepTagged: 30, MaxAge: time.Hour}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := ListRounds(ctx, s, owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	var left []sim.Tick
	for _, r := range rounds {
		left = append([]sim.Tick{r.Tick}, left...)
	}
	if want := []sim.Tick{ticks[0], ticks[2], ticks[4]}; !slices.Equal(left, want) || res.Deleted != 2 || res.KeptTagged != 2 {
		t.Fatalf("left %v (%+v), want %v", left, res, want)
	}
	// A tagged round past keep_deploy_rounds goes with its tags.
	if _, err := NewSweeper().Sweep(ctx, s, owned, nil, Retention{KeepRounds: 1, KeepTagged: 1, MaxAge: time.Hour}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, z := range owned {
		tags, err := s.ZoneTags(ctx, z)
		if err != nil {
			t.Fatal(err)
		}
		for k := range tags {
			if _, _, tick, _, _ := sim.ParseSnapshotKey(k); tick == ticks[0] {
				t.Errorf("a deleted round's tag survived: %s %v", k, tags[k])
			}
		}
	}
}
