// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// AW-SRV-007 AC-16: a content swap that adds a Zone lands after the last round.

func zoneDef(id string) *contentv1.ZoneDefinition {
	return &contentv1.ZoneDefinition{FormatVersion: 1, Id: id, Name: id, FallbackRoom: "r1", Rooms: []*contentv1.RoomDefinition{{Id: "r1", Title: "r1", Description: "A room."}}}
}

type swapWorld struct {
	src      *simtest.VersionedContent
	fs       *store.FS
	bounds   []tickloop.Boundary
	records  simtest.MemorySource
	roundAt  sim.Tick
	headHash [32]byte
	headTick sim.Tick
}

// newSwapWorld runs a World on p@1 (Zones a, b), takes a round, then swaps to
// p@2, which adds Zone c, and runs on.
func newSwapWorld(t *testing.T) *swapWorld {
	t.Helper()
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	src := &simtest.VersionedContent{Templates: reg, Zones: map[string]map[uint64][]*contentv1.ZoneDefinition{
		"p": {1: {zoneDef("a"), zoneDef("b")}, 2: {zoneDef("a"), zoneDef("b"), zoneDef("c")}},
	}}
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 3, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: src})
	w := &swapWorld{src: src, fs: store.NewFS(t.TempDir()), records: simtest.MemorySource{}}
	step := func(swap bool, version uint64) {
		t.Helper()
		var in sim.TickInput
		if swap {
			cmd, err := src.Swap(e, "p", version)
			if err != nil {
				t.Fatal(err)
			}
			rec := sim.Record{Partition: sim.WorldPartition, Offset: int64(len(w.records[sim.WorldPartition])), Command: cmd}
			w.records[sim.WorldPartition] = append(w.records[sim.WorldPartition], rec)
			in.Records = []sim.Record{rec}
		}
		res, err := e.Step(in)
		if err != nil {
			t.Fatal(err)
		}
		w.bounds = append(w.bounds, tickloop.Boundary{TickCompleted: res.Completed})
	}
	step(true, 1)
	step(false, 0)
	step(false, 0)
	w.roundAt = e.Tick()
	for _, s := range e.SnapshotAll(1) {
		body, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := w.fs.Put(context.Background(), s.Key(), body); err != nil {
			t.Fatal(err)
		}
	}
	step(false, 0)
	step(true, 2) // adds Zone c after the last round
	step(false, 0)
	step(false, 0)
	w.headHash, w.headTick = e.StateHash(), e.Tick()
	return w
}

func (w *swapWorld) opts(src sim.ContentSource, log *slog.Logger) recovery.Options {
	return recovery.Options{
		Store:      w.fs,
		Listed:     []sim.ZoneID{"a", "b", "c"}, // the content in effect at boot: p@2
		Boundaries: &memBoundaries{all: w.bounds},
		OpenRecords: func(context.Context, map[int32]int64) (sim.RecordSource, error) {
			return w.records, nil
		},
		Config:  sim.Config{Seed: 3, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers(), Content: src},
		Content: src,
		Poll:    time.Millisecond,
		Metrics: recovery.NewMetrics(nil),
		Log:     log,
	}
}

func TestARoundIsSelectedAfterASwapAddedAZone(t *testing.T) {
	t.Parallel()
	w := newSwapWorld(t)
	e, rep, err := recovery.Recover(context.Background(), w.opts(w.src, nil))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Round.Tick != w.roundAt || e.StateHash() != w.headHash || e.Tick() != w.headTick {
		t.Fatalf("recovered from round %d to tick %d, want round %d to tick %d: the round was not selected, or the swap did not replay", rep.Round.Tick, e.Tick(), w.roundAt, w.headTick)
	}
	if versions, _ := e.Content(); versions["p"] != 2 || len(e.State().Zones) != 3 {
		t.Fatalf("content %v with %d Zones after the swap replayed", versions, len(e.State().Zones))
	}

	// The case AC-16 removes: judged against the content in effect at boot, the
	// round lacks Zone c, is incomplete, and recovery replays the whole log.
	o := w.opts(w.src, nil)
	o.ZonesAt = func(map[string]uint64) ([]sim.ZoneID, error) { return o.Listed, nil }
	if _, rep, err := recovery.Recover(context.Background(), o); err != nil || rep.Round.Tick != 0 {
		t.Fatalf("judged on the current content: round %d err %v, want the old behavior, a cold replay", rep.Round.Tick, err)
	}
}

// The source no longer has the version the round records: exit 6, reason
// content, the gauge at 0, counted as a restore failure, one restore-mismatch
// line, and no other round tried.
func TestAVersionTheSourceLacksIsExit6(t *testing.T) {
	t.Parallel()
	w := newSwapWorld(t)
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	without := &simtest.VersionedContent{Templates: reg, Zones: map[string]map[uint64][]*contentv1.ZoneDefinition{
		"p": {2: {zoneDef("a"), zoneDef("b"), zoneDef("c")}},
	}}
	var buf bytes.Buffer
	o := w.opts(without, slog.New(slog.NewJSONHandler(&buf, nil)))
	_, rep, err := recovery.Recover(context.Background(), o)
	var rc *sim.ErrRoundContent
	if !errors.As(err, &rc) || rc.Versions["p"] != 1 || rc.Tick != w.roundAt {
		t.Fatalf("err %v, want ErrRoundContent for p@1 at round %d", err, w.roundAt)
	}
	f := recovery.Classify(err)
	if f.Exit != recovery.ExitRestore || f.Restore != recovery.RestoreContent || f.Reason != recovery.ReasonRestore || rep.Replayed != 0 {
		t.Fatalf("failure %+v, replayed %d", f, rep.Replayed)
	}
	if testutil.ToFloat64(o.Metrics.HashMatch()) != 0 || testutil.ToFloat64(o.Metrics.Failures.WithLabelValues(recovery.ReasonRestore)) != 1 {
		t.Fatal("gauge not 0, or failures{restore} not 1")
	}
	out := buf.String()
	if strings.Count(out, "recovery restore mismatch") != 1 || strings.Contains(out, "recovery refused") ||
		!strings.Contains(out, `"reason":"content"`) || !strings.Contains(out, `"pack_versions":"p@1"`) {
		t.Fatalf("log:\n%s", out)
	}
}

// A source that fails to answer is not a state mismatch: exit 1, and the
// gauge RecoveryStateMismatch pages on stays unset.
func TestASourceThatFailsToAnswerIsExit1(t *testing.T) {
	t.Parallel()
	w := newSwapWorld(t)
	o := w.opts(w.src, nil)
	o.ZonesAt = func(map[string]uint64) ([]sim.ZoneID, error) { return nil, errors.New("registry timed out") }
	_, _, err := recovery.Recover(context.Background(), o)
	if got := recovery.ExitCode(err); got != recovery.ExitConfig || o.Metrics.HashMatch() != nil {
		t.Fatalf("exit %d (%v), gauge set %v", got, err, o.Metrics.HashMatch() != nil)
	}
}
