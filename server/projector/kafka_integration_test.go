// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package projector_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/projector"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

func brokers(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	return strings.Split(v, ",")
}

// broker is one test's throwaway topics and group, and the live World that
// writes to them.
type broker struct {
	t                       *testing.T
	brokers                 []string
	cl                      *kgo.Client
	adm                     *kadm.Client
	commands, events, state string
	group                   string
	// mirrored is how much of the World's log and boundaries has been
	// written to the broker.
	mirroredLog        map[int32]int
	mirroredBoundaries int
}

func newBroker(t *testing.T) *broker {
	t.Helper()
	bs := brokers(t)
	cl, err := kgo.NewClient(kgo.SeedBrokers(bs...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	nonce := time.Now().UnixNano()
	b := &broker{
		t: t, brokers: bs, cl: cl, adm: adm,
		commands:    fmt.Sprintf("andara.test.commands.%d", nonce),
		events:      fmt.Sprintf("andara.test.events.%d", nonce),
		state:       fmt.Sprintf("andara.test.state.%d", nonce),
		group:       fmt.Sprintf("andara-projector-state-test-%d", nonce),
		mirroredLog: map[int32]int{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	compact := "compact"
	for topic, cfg := range map[string]map[string]*string{
		b.commands: nil, b.events: nil, b.state: {"cleanup.policy": &compact},
	} {
		if _, err := adm.CreateTopic(ctx, sim.PartitionCount, 1, cfg, topic); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, b.commands, b.events, b.state)
		_, _ = adm.DeleteGroups(dctx, b.group)
		cl.Close()
	})
	return b
}

// mirror writes what the World has done since the last mirror: its log, in
// offset order per Partition, and its Tick Boundary Records. A fresh topic's
// offsets start at 0, so the broker's offsets are the in-memory indexes.
// corrupt, if set, flips the recorded hash of that tick's boundary.
func (b *broker) mirror(w *world, corrupt sim.Tick) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var recs []*kgo.Record
	for p, log := range w.log {
		for _, r := range log[b.mirroredLog[p]:] {
			body, err := proto.MarshalOptions{Deterministic: true}.Marshal(r.Command)
			if err != nil {
				b.t.Fatal(err)
			}
			recs = append(recs, &kgo.Record{Topic: b.commands, Partition: p, Key: []byte(r.Command.GetZoneId()), Value: body})
		}
		b.mirroredLog[p] = len(log)
	}
	for _, tc := range w.boundaries[b.mirroredBoundaries:] {
		if tc.Tick == corrupt {
			tc.StateHash[0] ^= 0xff
		}
		body, err := proto.MarshalOptions{Deterministic: true}.Marshal(tc.Proto())
		if err != nil {
			b.t.Fatal(err)
		}
		recs = append(recs, &kgo.Record{Topic: b.events, Partition: tickloop.BoundaryPartition, Key: []byte(tickloop.BoundaryKey), Value: body})
	}
	b.mirroredBoundaries = len(w.boundaries)
	if err := b.cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		b.t.Fatal(err)
	}
}

// options is a RunOptions over this broker for a World built like w's.
func (b *broker) options(w *world) projector.RunOptions {
	return projector.RunOptions{
		Brokers: b.brokers, Group: b.group,
		CommandsTopic: b.commands, EventsTopic: b.events, StateTopic: b.state,
		World: w.live.World(), Content: crossing(b.t), Seed: seed,
		BatchTicks:     3,
		Poll:           100 * time.Millisecond,
		ContentVersion: func(sim.ZoneID) string { return "fixture@1" },
	}
}

// running is Run on a goroutine.
type running struct {
	cancel context.CancelFunc
	done   chan error
	ready  *atomic.Bool
}

func start(o projector.RunOptions) *running {
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1), ready: &atomic.Bool{}}
	o.OnReady = func() { r.ready.Store(true) }
	go func() { r.done <- projector.Run(ctx, o) }()
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("Run returned %v on a clean stop", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// committedTick is the group's committed tick, or -1.
func (b *broker) committedTick() int64 {
	cm, err := projector.NewCommitter(b.brokers, b.group, b.commands)
	if err != nil {
		return -1
	}
	defer cm.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cp, ok, err := cm.Last(ctx)
	if err != nil || !ok {
		return -1
	}
	return int64(cp.Tick)
}

func (b *broker) waitCommitted(t *testing.T, tick sim.Tick) {
	t.Helper()
	eventually.Observed(t, 60*time.Second, fmt.Sprintf("the projector commits tick %d", tick), func() (bool, string) {
		got := b.committedTick()
		return got == int64(tick), fmt.Sprintf("committed tick %d", got)
	})
}

// topic reads the state topic end to end: the last value per key, a
// tombstone deleting it — what compaction converges to.
func (b *broker) topic(t *testing.T) view {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ends, err := b.adm.ListEndOffsets(ctx, b.state)
	if err != nil {
		t.Fatal(err)
	}
	assign := map[int32]kgo.Offset{}
	remaining := map[int32]int64{}
	ends.Each(func(o kadm.ListedOffset) {
		if o.Offset > 0 {
			assign[o.Partition] = kgo.NewOffset().AtStart()
			remaining[o.Partition] = o.Offset
		}
	})
	v := view{}
	if len(assign) == 0 {
		return v
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(b.brokers...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{b.state: assign}))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	for len(remaining) > 0 {
		fetches := cl.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			t.Fatal(err)
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if r.Value == nil {
				delete(v, string(r.Key))
			} else {
				v[string(r.Key)] = r.Value
			}
			if r.Offset+1 >= remaining[r.Partition] {
				delete(remaining, r.Partition)
			}
		})
	}
	return v
}

func (b *broker) endOffsets(t *testing.T) map[int32]int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ends, err := b.adm.ListEndOffsets(ctx, b.state)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int32]int64{}
	ends.Each(func(o kadm.ListedOffset) { out[o.Partition] = o.Offset })
	return out
}

// dumpOf is what the topic must equal: a fresh dump of the live World.
func dumpOf(t *testing.T, w *world) view {
	t.Helper()
	recs, err := projector.New(w.live, projector.Options{ContentVersion: func(sim.ZoneID) string { return "fixture@1" }}).Dump()
	if err != nil {
		t.Fatal(err)
	}
	v := view{}
	v.apply(recs)
	return v
}

// AC-1, AC-2, AC-5 end to end: from an empty topic the projector follows the
// log, verifies every boundary, and the compacted topic describes exactly the
// World — the hero's town key tombstoned after the cross-Zone move — and it
// keeps describing it as the World moves on.
func TestRun_FollowsTheLogAndDescribesTheWorld(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	b.mirror(w, 0)
	o := b.options(w)
	m := projector.NewMetrics(nil)
	o.Metrics = m
	r := start(o)
	defer r.stop(t)

	b.waitCommitted(t, w.live.Tick())
	got := b.topic(t)
	sameContent(t, got, dumpOf(t, w))
	if _, ok := got["character:town/hero"]; ok {
		t.Fatal("the hero's town key survived the move into wilds")
	}
	if _, ok := got["character:wilds/hero"]; !ok {
		t.Fatal("no record for the hero in wilds")
	}
	eventually.True(t, 10*time.Second, "the projector reports ready", r.ready.Load)
	if got := testutil.ToFloat64(m.Tick); got != float64(w.live.Tick()) {
		t.Fatalf("andara_state_projector_tick = %v, want %d", got, w.live.Tick())
	}
	if testutil.ToFloat64(m.Tombstones) < 1 || testutil.ToFloat64(m.DigestMismatches) != 0 {
		t.Fatalf("tombstones %v mismatches %v", testutil.ToFloat64(m.Tombstones), testutil.ToFloat64(m.DigestMismatches))
	}

	// The World moves on while the projector runs.
	w.submit(simtest.Move("wilds", "hero", "west"), simtest.Look("town", "ada"))
	w.tick()
	w.tick()
	b.mirror(w, 0)
	b.waitCommitted(t, w.live.Tick())
	sameContent(t, b.topic(t), dumpOf(t, w))
}

// AC-4 and the restart path: a projector restarted behind its checkpoint
// replays silently to it — the topic gains nothing it already had — and then
// resumes producing.
func TestRun_RestartReplaysSilentlyToTheCheckpoint(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	b.mirror(w, 0)
	r := start(b.options(w))
	b.waitCommitted(t, w.live.Tick())
	r.stop(t)
	before := b.endOffsets(t)

	r = start(b.options(w))
	defer r.stop(t)
	eventually.True(t, 30*time.Second, "the restarted projector catches up", r.ready.Load)
	after := b.endOffsets(t)
	for p, off := range before {
		if after[p] != off {
			t.Fatalf("partition %d grew from %d to %d on a restart with nothing new to project", p, off, after[p])
		}
	}

	w.submit(simtest.Move("wilds", "hero", "west"))
	w.tick()
	w.tick()
	b.mirror(w, 0)
	b.waitCommitted(t, w.live.Tick())
	sameContent(t, b.topic(t), dumpOf(t, w))
}

// AC-6: --rebuild bootstraps from the newest complete round, never from zero
// when one exists — proved by deleting the log before the round, which a
// from-zero replay would need — and reaches the same records as the
// incremental projector.
func TestRun_RebuildFromTheRoundEqualsIncremental(t *testing.T) {
	b := newBroker(t)
	w := newWorld(t)
	w.submit(simtest.Bind("town", "hero", "Hero", "plaza"), simtest.Bind("town", "ada", "Ada", "plaza"))
	w.tick()
	w.submit(simtest.Move("town", "hero", "east"))
	w.tick()
	w.tick()
	ws := store.NewFS(t.TempDir())
	for _, s := range w.live.SnapshotAll(1) {
		body, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.Put(context.Background(), s.Key(), body); err != nil {
			t.Fatal(err)
		}
	}
	roundOffsets := map[int32]int64{}
	for p, off := range w.live.State().Offsets {
		roundOffsets[p] = off
	}
	w.submit(simtest.Unbind("town", "ada"), simtest.Move("wilds", "hero", "west"))
	w.tick()
	w.tick()
	b.mirror(w, 0)

	// Incremental, from zero, first.
	r := start(b.options(w))
	b.waitCommitted(t, w.live.Tick())
	r.stop(t)
	incremental := b.topic(t)

	// Delete the log before the round: only a round bootstrap can succeed.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var del kadm.Offsets
	for p, off := range roundOffsets {
		if off > 0 {
			del.Add(kadm.Offset{Topic: b.commands, Partition: p, At: off})
		}
	}
	if _, err := b.adm.DeleteRecords(ctx, del); err != nil {
		t.Fatal(err)
	}

	// The deletion took: a from-zero rebuild now finds history missing.
	zero := b.options(w)
	zero.Store, zero.Rebuild, zero.FromZero = ws, true, true
	if err := projector.Run(context.Background(), zero); projector.ExitCode(err) != projector.ExitLogGap {
		t.Fatalf("a from-zero rebuild over a truncated log should exit 3, got %v", err)
	}

	o := b.options(w)
	o.Store = ws
	o.Rebuild = true
	r = start(o)
	defer r.stop(t)
	eventually.True(t, 30*time.Second, "the rebuilt projector catches up", r.ready.Load)
	b.waitCommitted(t, w.live.Tick())
	rebuilt := b.topic(t)
	sameContent(t, rebuilt, incremental)
	sameContent(t, rebuilt, dumpOf(t, w))
}

// AC-3 end to end: a boundary whose hash does not match stops the projector
// with exit 2, commits through T-1 and no further, and counts the mismatch.
func TestRun_DivergenceExitsTwoAndCommitsNothingPastIt(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	const bad = 5
	b.mirror(w, bad)
	o := b.options(w)
	m := projector.NewMetrics(nil)
	o.Metrics = m
	err := projector.Run(context.Background(), o)
	var d *projector.Divergence
	if !errors.As(err, &d) || d.Tick != bad || projector.ExitCode(err) != projector.ExitDivergence {
		t.Fatalf("want a Divergence at tick %d, exit 2; got %v (exit %d)", bad, err, projector.ExitCode(err))
	}
	if got := b.committedTick(); got != bad-1 {
		t.Fatalf("committed tick %d, want %d", got, bad-1)
	}
	if testutil.ToFloat64(m.DigestMismatches) != 1 {
		t.Fatalf("andara_state_digest_mismatches_total = %v", testutil.ToFloat64(m.DigestMismatches))
	}
}

// Feedback §2, architecture's ruling: a divergence survives the restart. After
// a halt at T, a newer complete round appears; the restart still exits 2 naming
// T and both hashes, rather than bootstrapping past it. Only --rebuild clears
// it, and says so.
func TestRun_ADivergenceSurvivesARestartPastANewerRound(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	const bad = 5
	b.mirror(w, bad)
	first := projector.Run(context.Background(), b.options(w))
	var d1 *projector.Divergence
	if !errors.As(first, &d1) || d1.Tick != bad {
		t.Fatalf("the first run: %v, want a divergence at %d", first, bad)
	}

	// A complete round newer than the divergent tick.
	for w.live.Tick() <= bad+2 {
		w.tick()
	}
	ws := store.NewFS(t.TempDir())
	for _, s := range w.live.SnapshotAll(1) {
		body, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.Put(context.Background(), s.Key(), body); err != nil {
			t.Fatal(err)
		}
	}
	b.mirror(w, 0)

	o := b.options(w)
	o.Store = ws
	m := projector.NewMetrics(nil)
	o.Metrics = m
	// Bounded: a projector that bootstraps past the divergence runs on.
	rctx, rcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer rcancel()
	again := projector.Run(rctx, o)
	var d2 *projector.Divergence
	if !errors.As(again, &d2) || projector.ExitCode(again) != projector.ExitDivergence {
		t.Fatalf("the restart past a newer round: %v (exit %d), want exit 2", again, projector.ExitCode(again))
	}
	if d2.Tick != d1.Tick || d2.Recorded != d1.Recorded || d2.Replayed != d1.Replayed {
		t.Fatalf("the restart named %d %x %x, the halt %d %x %x", d2.Tick, d2.Recorded, d2.Replayed, d1.Tick, d1.Recorded, d1.Replayed)
	}
	if got := b.committedTick(); got != bad-1 {
		t.Fatalf("committed tick %d after the restart, want %d", got, bad-1)
	}
	if testutil.ToFloat64(m.DigestMismatches) != 1 {
		t.Fatalf("andara_state_digest_mismatches_total = %v on the refused start, want 1", testutil.ToFloat64(m.DigestMismatches))
	}

	// --rebuild clears it, bootstraps from the round past the bad
	// boundary, and runs.
	var logs syncBuffer
	o.Rebuild = true
	o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	r := start(o)
	defer r.stop(t)
	eventually.True(t, 30*time.Second, "the rebuilt projector catches up", r.ready.Load)
	if !strings.Contains(logs.String(), `"msg":"--rebuild discards an unresolved divergence","tick":5`) {
		t.Fatalf("no info line naming the discarded divergence:\n%s", logs.String())
	}
}

// syncBuffer is a bytes.Buffer safe for a logger and a reader at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// AC-10: a round written by a newer binary is refused with exit 4.
func TestRun_NewerStateVersionExitsFour(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	b.mirror(w, 0)
	ws := store.NewFS(t.TempDir())
	env, err := proto.Marshal(&statev1.SnapshotEnvelope{StateVersion: sim.StateVersion + 1, ZoneId: "town", Tick: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Put(context.Background(), sim.SnapshotKey("town", sim.StateVersion+1, 3, 0), env); err != nil {
		t.Fatal(err)
	}
	o := b.options(w)
	o.Store = ws
	err = projector.Run(context.Background(), o)
	var sv *sim.ErrStateVersion
	if !errors.As(err, &sv) || projector.ExitCode(err) != projector.ExitStateVersion {
		t.Fatalf("want exit 4 naming both versions, got %v (exit %d)", err, projector.ExitCode(err))
	}
	if sv.Have != sim.StateVersion+1 || sv.Want != sim.StateVersion {
		t.Fatalf("versions %+v", sv)
	}
}

// Exit 3: the log's first boundary is not tick 1 and there is no round to
// start from, so history the replica needs is gone.
func TestRun_MissingHistoryExitsThree(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	b.mirrorFrom(w, 3)
	err := projector.Run(context.Background(), b.options(w))
	if projector.ExitCode(err) != projector.ExitLogGap {
		t.Fatalf("want exit 3, got %v (exit %d)", err, projector.ExitCode(err))
	}
}

// mirrorFrom mirrors the log whole but only the boundaries from tick on.
func (b *broker) mirrorFrom(w *world, tick sim.Tick) {
	b.mirroredBoundaries = int(tick) - 1
	b.mirror(w, 0)
}

// Readiness against a World that never stops ticking. Found running the
// binary beside a live server: a boundary lands every 100 ms, so "caught up =
// a read that found nothing" never happened and /readyz stayed 503. Caught up
// is a position — every boundary on the log, as of the last fetch, verified.
func TestRun_ReadyWhileTheWorldKeepsTicking(t *testing.T) {
	b := newBroker(t)
	w := script(t)
	b.mirror(w, 0)
	r := start(b.options(w))
	defer r.stop(t)

	stop := make(chan struct{})
	ticking := make(chan struct{})
	go func() {
		defer close(ticking)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				w.tick()
				b.mirror(w, 0)
			}
		}
	}()
	eventually.True(t, 30*time.Second, "the projector reports ready while the World ticks", r.ready.Load)
	close(stop)
	<-ticking
	b.waitCommitted(t, w.live.Tick())
	sameContent(t, b.topic(t), dumpOf(t, w))
}

// andara_state_topic_bytes' query (feedback §1): against a topic holding
// records it reports their size, summed over every Partition, rather than 0.
// A topic the broker does not have is an error, not 0.
func TestTopicBytesReportsWhatTheTopicHolds(t *testing.T) {
	b := newBroker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var recs []*kgo.Record
	for p := int32(0); p < 4; p++ {
		recs = append(recs, &kgo.Record{Topic: b.state, Partition: p, Key: []byte("zone:z"), Value: bytes.Repeat([]byte("x"), 512)})
	}
	if err := b.cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		t.Fatal(err)
	}
	eventually.Observed(t, 30*time.Second, "TopicBytes reports the records on four Partitions", func() (bool, string) {
		n, err := projector.TopicBytes(ctx, b.adm, b.state)
		return err == nil && n >= 4*512, fmt.Sprintf("%d bytes, err %v", n, err)
	})
	if n, err := projector.TopicBytes(ctx, b.adm, b.state+".absent"); err == nil {
		t.Fatalf("a missing topic reported %d bytes and no error", n)
	}
}
