// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package projector_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/canonical"
	"github.com/valesordev/andara/server/projector"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// roundStore writes w's round at its current tick to a fresh filesystem
// store. corrupt, if set, names a Zone whose body is altered so that it still
// decodes, with its envelope re-signed: the per-object check passes, and
// only the restore's own check can catch it (AW-SRV-043 AC-2).
func roundStore(t *testing.T, w *world, corrupt sim.ZoneID) (sim.WorldStore, sim.Tick) {
	t.Helper()
	ws := store.NewFS(t.TempDir())
	altered := false
	for _, s := range w.live.SnapshotAll(1) {
		raw, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if s.Zone == corrupt {
			var env statev1.SnapshotEnvelope
			var body statev1.ZoneState
			if err := proto.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			if err := proto.Unmarshal(env.GetBody(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.GetEntities()) == 0 {
				t.Fatalf("zone %s has no Entity to alter", corrupt)
			}
			body.Entities[0].Name += "!"
			if env.Body, err = canonical.Marshal(&body); err != nil {
				t.Fatal(err)
			}
			hash, err := sim.BodyStateHash(&body)
			if err != nil {
				t.Fatal(err)
			}
			env.StateHash = hash[:]
			if raw, err = canonical.Marshal(&env); err != nil {
				t.Fatal(err)
			}
			altered = true
		}
		if err := ws.Put(context.Background(), s.Key(), raw); err != nil {
			t.Fatal(err)
		}
	}
	if corrupt != "" && !altered {
		t.Fatalf("no zone %s in the round", corrupt)
	}
	return ws, w.live.Tick()
}

// mismatchLine is the one error line a refused restore logs.
func mismatchLine(t *testing.T, logs *syncBuffer) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, l := range strings.Split(logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "state projector restore mismatch" {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d restore mismatch lines, want 1:\n%s", len(found), logs.String())
	}
	if found[0]["level"] != "ERROR" || found[0]["trace_id"] == "" || found[0]["trace_id"] == nil {
		t.Fatalf("the mismatch line: %v", found[0])
	}
	return found[0]
}

// AW-SRV-043 AC-3, AC-4: a projector bootstrapping from a round that does
// not restore to its tick's recorded State Hash exits 5, logs one error line
// naming the round tick and both hashes, counts hash_mismatch, writes no
// checkpoint and produces nothing — no fall-back to another round or to
// replay from zero. --rebuild hits the same check.
func TestRun_ACorruptedRoundExitsFive(t *testing.T) {
	for _, rebuild := range []bool{false, true} {
		t.Run(fmt.Sprintf("rebuild=%t", rebuild), func(t *testing.T) {
			b := newBroker(t)
			w := script(t)
			ws, round := roundStore(t, w, "town")
			b.mirror(w, 0)
			o := b.options(w)
			o.Store, o.Rebuild = ws, rebuild
			o.Metrics = projector.NewMetrics(nil)
			var logs syncBuffer
			o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
			spans := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
			defer func() { _ = tp.Shutdown(context.Background()) }()
			o.Tracer = tp.Tracer("test")

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := projector.Run(ctx, o)
			var rm *sim.RestoreMismatch
			if !errors.As(err, &rm) || projector.ExitCode(err) != projector.ExitRestoreMismatch {
				t.Fatalf("Run: %v (exit %d), want a restore mismatch, exit 5", err, projector.ExitCode(err))
			}
			recorded := w.boundaries[round-1]
			if recorded.Tick != round {
				t.Fatalf("fixture: boundary %d is for tick %d", round-1, recorded.Tick)
			}
			line := mismatchLine(t, &logs)
			if line["round_tick"] != float64(round) || line["reason"] != "hash" ||
				line["recorded_hash"] != fmt.Sprintf("%x", recorded.StateHash) || line["restored_hash"] != fmt.Sprintf("%x", rm.Restored) {
				t.Fatalf("the mismatch line: %v", line)
			}
			if got := testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(projector.RestoreCaller, "hash_mismatch")); got != 1 {
				t.Fatalf("andara_restore_total{caller=projector,outcome=hash_mismatch} = %v, want 1", got)
			}
			if got := testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(projector.RestoreCaller, "ok")); got != 0 {
				t.Fatalf("andara_restore_total{caller=projector,outcome=ok} = %v, want 0", got)
			}
			if got := b.committedTick(); got != -1 {
				t.Fatalf("committed tick %d, want none", got)
			}
			if v := b.topic(t); len(v) != 0 {
				t.Fatalf("the state topic holds %d records: the projector fell back to something", len(v))
			}

			// §7: restore.verify under state.bootstrap, both with the
			// outcome, the verify span at Error status.
			var boot, verify sdktrace.ReadOnlySpan
			for _, s := range spans.Ended() {
				switch s.Name() {
				case "state.bootstrap":
					boot = s
				case "restore.verify":
					verify = s
				}
			}
			if boot == nil || verify == nil || verify.Parent().SpanID() != boot.SpanContext().SpanID() {
				t.Fatalf("spans: state.bootstrap %v, restore.verify %v, not parent and child", boot != nil, verify != nil)
			}
			va, ba := attribute.NewSet(verify.Attributes()...), attribute.NewSet(boot.Attributes()...)
			if v, _ := va.Value("outcome"); v.AsString() != "hash_mismatch" || verify.Status().Code != codes.Error {
				t.Fatalf("restore.verify: outcome %q, status %v", v.AsString(), verify.Status())
			}
			if v, _ := va.Value("round_tick"); v.AsInt64() != int64(round) {
				t.Fatalf("restore.verify round_tick %d", v.AsInt64())
			}
			if v, _ := ba.Value("outcome"); v.AsString() != "hash_mismatch" {
				t.Fatalf("state.bootstrap outcome %q", v.AsString())
			}
			if v, _ := ba.Value("rebuild"); v.AsBool() != rebuild {
				t.Fatalf("state.bootstrap rebuild %t", v.AsBool())
			}
		})
	}
}

// AW-SRV-043 AC-5: a round written on one seed, bootstrapped by a projector
// configured with another, exits 5 as a seed mismatch naming both seeds,
// and counts seed_mismatch.
func TestRun_ARoundFromAnotherSeedExitsFive(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	ws, round := roundStore(t, w, "")
	b.mirror(w, 0)
	o := b.options(w)
	o.Store, o.Seed = ws, 7
	o.Metrics = projector.NewMetrics(nil)
	var logs syncBuffer
	o.Log = slog.New(slog.NewJSONHandler(&logs, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := projector.Run(ctx, o)
	var sm *sim.SeedMismatch
	if !errors.As(err, &sm) || projector.ExitCode(err) != projector.ExitRestoreMismatch {
		t.Fatalf("Run: %v (exit %d), want a seed mismatch, exit 5", err, projector.ExitCode(err))
	}
	line := mismatchLine(t, &logs)
	if line["round_tick"] != float64(round) || line["reason"] != "seed" ||
		line["recorded_seed"] != strconv.FormatUint(w.live.State().Seed, 10) || line["configured_seed"] != "7" {
		t.Fatalf("the mismatch line: %v", line)
	}
	if got := testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(projector.RestoreCaller, "seed_mismatch")); got != 1 {
		t.Fatalf("andara_restore_total{caller=projector,outcome=seed_mismatch} = %v, want 1", got)
	}
	if got := b.committedTick(); got != -1 {
		t.Fatalf("committed tick %d, want none", got)
	}
}

// AW-SRV-043 AC-1: a sound round verifies, counts ok, logs the verified line,
// and the projector replays on from the tick after it.
func TestRun_ASoundRoundVerifies(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	ws, round := roundStore(t, w, "")
	for range 3 {
		w.tick()
	}
	b.mirror(w, 0)
	o := b.options(w)
	o.Store = ws
	o.Metrics = projector.NewMetrics(nil)
	var logs syncBuffer
	o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	r := start(o)
	eventually.True(t, 30*time.Second, "the projector catches up", r.ready.Load)
	b.waitCommitted(t, w.live.Tick())
	r.stop(t)

	if got := testutil.ToFloat64(o.Metrics.Restores.WithLabelValues(projector.RestoreCaller, "ok")); got != 1 {
		t.Fatalf("andara_restore_total{caller=projector,outcome=ok} = %v, want 1", got)
	}
	var verified map[string]any
	for _, l := range strings.Split(logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "state projector restore verified" {
			verified = m
		}
	}
	want := fmt.Sprintf("%x", w.boundaries[round-1].StateHash)
	if verified == nil || verified["round_tick"] != float64(round) || verified["restored_hash"] != want || verified["trace_id"] == "" {
		t.Fatalf("the verified line: %v", verified)
	}
}

// A round whose own boundary the log no longer has, with nothing after it,
// is a log gap (exit 3), not an indefinitely quiet log: the round was
// written after its boundary was acknowledged, so no later read brings it
// (Codex on #365).
func TestRun_ARoundWhoseBoundaryIsGoneIsALogGap(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	b.mirror(w, 0) // the log through the tick before the round
	w.tick()
	ws, round := roundStore(t, w, "")
	if got := w.boundaries[len(w.boundaries)-1].Tick; got != round {
		t.Fatalf("fixture: the round is at tick %d, the World at %d", round, got)
	}
	o := b.options(w)
	o.Store = ws
	o.Metrics = projector.NewMetrics(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := projector.Run(ctx, o)
	if ctx.Err() != nil {
		t.Fatal("Run waited for a boundary the log can't bring")
	}
	if !errors.Is(err, projector.ErrLogGap) || projector.ExitCode(err) != projector.ExitLogGap {
		t.Fatalf("Run: %v (exit %d), want a log gap, exit 3", err, projector.ExitCode(err))
	}
}

// The same with an Event after the last boundary on the boundary Partition,
// as the publisher leaves when it sends a tick's Events before its held
// boundary: still a log gap, not a wait (review of #365).
func TestRun_ARoundWhoseBoundaryIsGoneBehindATrailingEventIsALogGap(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	b.mirror(w, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.cl.ProduceSync(ctx, &kgo.Record{Topic: b.events, Partition: tickloop.BoundaryPartition, Key: []byte(""), Value: []byte("an event")}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	w.tick()
	ws, _ := roundStore(t, w, "")
	o := b.options(w)
	o.Store = ws
	o.Metrics = projector.NewMetrics(nil)
	err := projector.Run(ctx, o)
	if ctx.Err() != nil {
		t.Fatal("Run waited for a boundary the log can't bring")
	}
	if !errors.Is(err, projector.ErrLogGap) {
		t.Fatalf("Run: %v, want a log gap", err)
	}
}
