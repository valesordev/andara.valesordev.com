// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

// AW-SRV-007 against a running broker and the filesystem store (`make up`,
// then `make test-integration`): a real process is SIGKILLed mid-play and a
// second recovers from what it left.
package recovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

var canonical = proto.MarshalOptions{Deterministic: true}

func brokers(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	return strings.Split(v, ",")
}

// topics creates a throwaway commands topic and events topic, deleted when the
// test ends.
func topics(t *testing.T, bk []string, tag string) (commands, events string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(bk...))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	nonce := time.Now().UnixNano()
	commands = fmt.Sprintf("andara.test.rec.commands.%s.%d", tag, nonce)
	events = fmt.Sprintf("andara.test.rec.events.%s.%d", tag, nonce)
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, commands, events)
		cl.Close()
	})
	for _, tp := range []string{commands, events} {
		if _, err := adm.CreateTopic(ctx, sim.PartitionCount, 1, nil, tp); err != nil {
			t.Fatal(err)
		}
	}
	return commands, events
}

// The two Worlds the tests run: the Script fixture, and the crossing World
// (town, docks, wilds) with the real verb handlers, where a Move east is a
// cross-Zone handoff.
const (
	fixtureScript = "script"
	fixtureCross  = "cross"
	// fixtureSizing is the AC-7 World: simtest's sizing fixture (25,000
	// Entities, 2,000 Rooms, 16 Zones, 500 Characters), populated directly, so
	// a process starts on it and recovery restores it from a round.
	fixtureSizing = "sizing"
)

// options is recovery over the broker and the store at dir, the way boot builds
// it; the returned func closes the readers.
func options(t *testing.T, fix string, bk []string, commands, events, dir string) (recovery.Options, func()) {
	t.Helper()
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	cfg := sim.Config{Seed: 11, Partitions: simtest.AllPartitions(), Handlers: simtest.Handlers(reg)}
	world := simtest.World
	switch fix {
	case fixtureCross:
		world = simtest.CrossingWorld
		cfg = sim.Config{Seed: 9, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()}
	case fixtureSizing:
		world = simtest.SizingWorld
		cfg = sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()}
	}
	w, err := world()
	if err != nil {
		t.Fatal(err)
	}
	br, err := tickloop.NewBoundaryReader(context.Background(), bk, events, "recovery-it")
	if err != nil {
		t.Fatal(err)
	}
	return recovery.Options{
		Store:      store.NewFS(dir),
		Owned:      zoneIDs(t, fix),
		Boundaries: br,
		OpenRecords: func(ctx context.Context, start map[int32]int64) (sim.RecordSource, error) {
			return tickloop.NewCommandSource(ctx, bk, commands, "recovery-it", start)
		},
		Config:  cfg,
		Prepare: func(map[string]uint64) (sim.Topology, error) { return sim.Topology{World: w, Templates: reg}, nil },
		Poll:    200 * time.Millisecond,
		Metrics: recovery.NewMetrics(nil),
	}, br.Close
}

func zoneIDs(t *testing.T, fix string) []sim.ZoneID {
	t.Helper()
	e, err := simtest.NewEngine(11)
	switch fix {
	case fixtureCross:
		e, err = simtest.NewVerbEngine(9)
	case fixtureSizing:
		e, err = simtest.SizingEngine(7)
	}
	if err != nil {
		t.Fatal(err)
	}
	return e.State().SortedZoneIDs()
}

// TestRecoveryChild is the process the test spawns and SIGKILLs: it recovers,
// then runs a loop that takes a snapshot round every 400 ms.
func TestRecoveryChild(t *testing.T) {
	spec := os.Getenv("ANDARA_RECOVERY_CHILD")
	if spec == "" {
		t.Skip("child mode only")
	}
	parts := strings.Split(spec, "|")
	bk, commands, events, dir, fix := strings.Split(parts[0], ","), parts[1], parts[2], parts[3], parts[4]
	ctx := context.Background()
	o, closeReader := options(t, fix, bk, commands, events, dir)
	var e *sim.Engine
	var err error
	if fix == fixtureSizing {
		// A World the log can't build from empty: the process starts on it,
		// and recovery restores it from the rounds this process writes.
		e, err = simtest.SizingEngine(7)
	} else {
		e, _, err = recovery.Recover(ctx, o)
	}
	closeReader()
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	interval, rate := 400*time.Millisecond, 50
	if fix == fixtureSizing {
		interval, rate = 60*time.Second, 1000
	}
	src, err := tickloop.NewKafkaSource(ctx, tickloop.KafkaSourceOptions{Brokers: bk, Group: "andara-rec-" + commands[len(commands)-8:], Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	pub, err := tickloop.NewKafkaPublisher(ctx, bk, "recovery-it-child")
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	pub.Commands, pub.Events = commands, events
	var out tickloop.Publisher = pub
	if parts[5] == "lossy" {
		out = &arriveDropping{KafkaPublisher: pub}
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	snap, err := tickloop.NewSnapshotter(tickloop.SnapshotOptions{Store: store.NewFS(dir), Interval: interval, MaxStall: time.Second, UploadTimeout: 10 * time.Second, AwaitBoundaryAck: true, Log: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	loop, err := tickloop.New(tickloop.Options{
		Engine: e, Source: src, Publisher: out, Snapshotter: snap, AwaitBoundaryAck: true,
		TickRate: rate, TickBudget: 20 * time.Millisecond, MaxPerTick: 3, DrainTimeout: 5 * time.Second, CheckpointEvery: 5, Log: log,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	e.SetObserver(loop)
	tickloop.WireBoundaries(pub, loop, snap, nil)
	fmt.Println("child: running")
	if err := loop.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(4)
	}
}

// arriveDropping is the Kafka publisher with its cross-Zone producer dropping
// every Arrive: what a broker outage past the delivery timeout does.
type arriveDropping struct{ *tickloop.KafkaPublisher }

func (p *arriveDropping) Produce(ctx context.Context, cmds []*logv1.LoggedCommand) error {
	var keep []*logv1.LoggedCommand
	for _, c := range cmds {
		if c.GetArrive() == nil {
			keep = append(keep, c)
		}
	}
	if len(keep) == 0 {
		return nil
	}
	return p.KafkaPublisher.Produce(ctx, keep)
}

func spawn(t *testing.T, fix, mode string, bk []string, commands, events, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestRecoveryChild$", "-test.v")
	cmd.Env = append(os.Environ(), "ANDARA_RECOVERY_CHILD="+strings.Join(bk, ",")+"|"+commands+"|"+events+"|"+dir+"|"+fix+"|"+mode)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// ack is a Command a client was told the log holds: where, and what.
type ack struct {
	partition int32
	offset    int64
	value     []byte
}

// submit produces the scripted log through its own client, as a Gateway's
// producer does, and records every acknowledgement's partition and offset.
func submit(t *testing.T, bk []string, commands string, n int) []ack {
	t.Helper()
	var cmds []*logv1.LoggedCommand
	for _, recs := range simtest.Script(n) {
		for _, r := range recs {
			cmds = append(cmds, r.Command)
		}
	}
	return produceCommands(t, bk, commands, cmds)
}

// produceCommands produces cmds in order, each to its own Partition.
func produceCommands(t *testing.T, bk []string, commands string, cmds []*logv1.LoggedCommand) []ack {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(bk...), kgo.RecordPartitioner(kgo.ManualPartitioner()), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var acks []ack
	for _, cmd := range cmds {
		body, err := canonical.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		res := cl.ProduceSync(context.Background(), &kgo.Record{Topic: commands, Partition: sim.CommandPartition(cmd), Key: []byte(cmd.GetZoneId()), Value: body})
		if err := res.FirstErr(); err != nil {
			t.Fatal(err)
		}
		acks = append(acks, ack{partition: res[0].Record.Partition, offset: res[0].Record.Offset, value: body})
	}
	return acks
}

// history is what a SIGKILLed process left: its log, its store, and the
// acknowledgements its clients hold.
type history struct {
	bk                 []string
	commands, events   string
	dir                string
	acks               []ack
	lastTick           sim.Tick
	lastHash           [32]byte
	newestRound        sim.Tick
	boundaryCount      int
	killedAfterBoundry int
}

func killedProcess(t *testing.T) *history {
	t.Helper()
	bk := brokers(t)
	commands, events := topics(t, bk, "live")
	dir := t.TempDir()
	h := &history{bk: bk, commands: commands, events: events, dir: dir}
	child := spawn(t, fixtureScript, "lossless", bk, commands, events, dir)
	t.Cleanup(func() { _ = child.Process.Signal(syscall.SIGKILL); _ = child.Wait() })
	h.acks = submit(t, bk, commands, 40)

	// Every acknowledged Command applied, two rounds taken, and some ticks
	// past the newest round: the tail recovery must replay.
	fs := store.NewFS(dir)
	var bs []sim.TickCompleted
	eventually.Observed(t, 60*time.Second, "all acks applied, two rounds, a tail past the newest", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		if bs, err = tickloop.ReadBoundaries(ctx, bk, events); err != nil || len(bs) == 0 {
			return false, fmt.Sprint("no boundaries ", err)
		}
		last := bs[len(bs)-1]
		for _, a := range h.acks {
			if last.Offsets[a.partition] <= a.offset {
				return false, fmt.Sprintf("partition %d applied to %d, ack at %d", a.partition, last.Offsets[a.partition], a.offset)
			}
		}
		rounds, err := store.ListRounds(ctx, fs, zoneIDs(t, fixtureScript))
		if err != nil {
			return false, err.Error()
		}
		complete := 0
		for _, r := range rounds {
			if r.Complete {
				complete++
				if h.newestRound == 0 {
					h.newestRound = r.Tick
				}
			}
		}
		h.newestRound = 0
		for _, r := range rounds {
			if r.Complete {
				h.newestRound = r.Tick
				break
			}
		}
		return complete >= 2 && last.Tick >= h.newestRound+5, fmt.Sprintf("%d complete rounds, newest %d, head %d", complete, h.newestRound, last.Tick)
	})
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	// What survived: every boundary the broker acknowledged before the kill.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bs, err := tickloop.ReadBoundaries(ctx, bk, events)
	if err != nil {
		t.Fatal(err)
	}
	h.boundaryCount = len(bs)
	h.lastTick, h.lastHash = bs[len(bs)-1].Tick, bs[len(bs)-1].StateHash
	return h
}

func copyStore(t *testing.T, from string) string {
	t.Helper()
	to := t.TempDir()
	if out, err := exec.Command("cp", "-a", from+"/.", to).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	return to
}

func recoverOnce(t *testing.T, h *history, dir string, mutate func(*recovery.Options)) (*sim.Engine, recovery.Report, error) {
	t.Helper()
	o, closeReader := options(t, fixtureScript, h.bk, h.commands, h.events, dir)
	defer closeReader()
	if mutate != nil {
		mutate(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return recovery.Recover(ctx, o)
}

func TestRecoveryAgainstTheBroker(t *testing.T) {
	h := killedProcess(t)

	// AC-1: the recovered World's State Hash is the one the last surviving
	// boundary recorded, from a snapshot round and a replayed tail.
	t.Run("AC-1 a SIGKILLed World recovers to its last recorded hash", func(t *testing.T) {
		e, rep, err := recoverOnce(t, h, copyStore(t, h.dir), nil)
		if err != nil {
			t.Fatal(err)
		}
		if e.Tick() != h.lastTick || e.StateHash() != h.lastHash || !rep.Match {
			t.Fatalf("recovered tick %d hash %x, log's last is tick %d hash %x", e.Tick(), e.StateHash(), h.lastTick, h.lastHash)
		}
		if rep.Round.Tick == 0 || rep.Replayed == 0 {
			t.Fatalf("round %d replayed %d: recovery should restore a round and replay a tail", rep.Round.Tick, rep.Replayed)
		}

		// AC-8: every acknowledged Command is below the replayed head of its
		// Partition, and the record at that offset is the one acknowledged.
		ks := tickloop.KafkaRecords{Brokers: h.bk, Topic: h.commands}
		for _, a := range h.acks {
			if head := e.State().Offsets[a.partition]; a.offset >= head {
				t.Fatalf("partition %d: acked offset %d is not below the replayed head %d", a.partition, a.offset, head)
			}
			recs, err := ks.Fetch(a.partition, a.offset, a.offset+1)
			if err != nil || len(recs) != 1 {
				t.Fatalf("fetch %d/%d: %v", a.partition, a.offset, err)
			}
			got, _ := canonical.Marshal(recs[0].Command)
			if !bytes.Equal(got, a.value) {
				t.Fatalf("partition %d offset %d holds another Command than the one acknowledged", a.partition, a.offset)
			}
		}
	})

	// AC-3: the batching of the replay changes nothing.
	t.Run("AC-3 replay batching does not change the hashes", func(t *testing.T) {
		var want []tickloop.Boundary
		_ = want
		var ref [][32]byte
		for i, batch := range []int{1, 7, 4096} {
			var hashes [][32]byte
			e, _, err := recoverOnce(t, h, copyStore(t, h.dir), func(o *recovery.Options) {
				o.ReplayBatch = batch
				o.After = func(r sim.StepResult) error { hashes = append(hashes, r.Completed.StateHash); return nil }
			})
			if err != nil || e.StateHash() != h.lastHash {
				t.Fatalf("batch %d: %v", batch, err)
			}
			if i == 0 {
				ref = hashes
			} else if len(hashes) != len(ref) {
				t.Fatalf("batch %d replayed %d ticks, batch 1 replayed %d", batch, len(hashes), len(ref))
			} else {
				for j := range hashes {
					if hashes[j] != ref[j] {
						t.Fatalf("batch %d: hash %d differs", batch, j)
					}
				}
			}
		}
	})

	// AC-4: a corrupt Zone object makes the newest round incomplete, and
	// recovery selects the next complete one; a named one is exit 7 (AC-15).
	t.Run("AC-4 a hash-invalid Zone object falls back to the next round", func(t *testing.T) {
		dir := copyStore(t, h.dir)
		fs := store.NewFS(dir)
		newest, _, ok, err := store.NewestComplete(context.Background(), fs, zoneIDs(t, fixtureScript))
		if err != nil || !ok {
			t.Fatal(err)
		}
		key := newest.Zones[0].Key
		raw, _ := fs.Get(context.Background(), key)
		raw[len(raw)/2] ^= 0xff
		if err := fs.Put(context.Background(), key, raw); err != nil {
			t.Fatal(err)
		}
		e, rep, err := recoverOnce(t, h, dir, nil)
		if err != nil || e.StateHash() != h.lastHash {
			t.Fatalf("recovery after the corruption: %v", err)
		}
		if rep.Round.Tick >= newest.Tick {
			t.Fatalf("recovered from round %d, the corrupt one is %d", rep.Round.Tick, newest.Tick)
		}
		named := newest.Tick
		_, _, err = recoverOnce(t, h, dir, func(o *recovery.Options) { o.Round = &named })
		var inc *sim.ErrRoundIncomplete
		if !errors.As(err, &inc) || recovery.ExitCode(err) != recovery.ExitRound || inc.Cause != sim.RoundHash {
			t.Fatalf("named incomplete round: exit %d err %v", recovery.ExitCode(err), err)
		}
	})

	// AC-9: no round at all replays from offset zero to the same World.
	t.Run("AC-9 no snapshot replays from offset zero", func(t *testing.T) {
		e, rep, err := recoverOnce(t, h, t.TempDir(), nil)
		if err != nil || e.StateHash() != h.lastHash {
			t.Fatalf("cold start: %v", err)
		}
		if rep.Round.Tick != 0 || rep.Replayed != uint64(h.lastTick) {
			t.Fatalf("round %d replayed %d, want 0 and %d", rep.Round.Tick, rep.Replayed, h.lastTick)
		}
		_, _, err = recoverOnce(t, h, t.TempDir(), func(o *recovery.Options) { o.RequireSnapshot = true })
		if recovery.ExitCode(err) != recovery.ExitRound {
			t.Fatalf("require_snapshot with no round: exit %d", recovery.ExitCode(err))
		}
	})

	// AC-5: a tail boundary whose recorded hash is not the one replay
	// produces is exit 8, naming that tick and the round used.
	t.Run("AC-5 a rewritten tail boundary is exit 8 at that tick", func(t *testing.T) {
		ctx := context.Background()
		_, rep, err := recoverOnce(t, h, copyStore(t, h.dir), nil)
		if err != nil {
			t.Fatal(err)
		}
		flip := rep.Round.Tick + 3
		events2 := republish(t, h, flip)
		h2 := *h
		h2.events = events2
		_, rep2, err := recoverOnce(t, &h2, copyStore(t, h.dir), nil)
		var hm *recovery.HashMismatchError
		if !errors.As(err, &hm) || hm.Tick != flip || hm.Round != rep.Round.Tick || recovery.ExitCode(err) != recovery.ExitHashMismatch {
			t.Fatalf("err %v (exit %d), want a mismatch at tick %d from round %d", err, recovery.ExitCode(err), flip, rep.Round.Tick)
		}
		if rep2.Match || rep2.MismatchTick != flip {
			t.Fatalf("report %+v", rep2)
		}
		_ = ctx
	})

	// AC-13: every object of the newest round carries a flipped byte of
	// prng_state, re-signed, so the round agrees with itself and verifies. It
	// doesn't reproduce its own tick: exit 6, nothing replayed, no other round
	// tried, the gauge at 0 and failures{restore} at 1.
	t.Run("AC-13 a round that does not reproduce its tick is exit 6", func(t *testing.T) {
		dir := copyStore(t, h.dir)
		fs := store.NewFS(dir)
		newest, _, ok, err := store.NewestComplete(context.Background(), fs, zoneIDs(t, fixtureScript))
		if err != nil || !ok {
			t.Fatal(err)
		}
		for _, z := range newest.Zones {
			raw, err := fs.Get(context.Background(), z.Key)
			if err != nil {
				t.Fatal(err)
			}
			var env statev1.SnapshotEnvelope
			var body statev1.ZoneState
			if err := proto.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			if err := proto.Unmarshal(env.GetBody(), &body); err != nil {
				t.Fatal(err)
			}
			body.PrngState[0] ^= 0x01
			if env.Body, err = canonical.Marshal(&body); err != nil {
				t.Fatal(err)
			}
			sum, err := sim.BodyStateHash(&body)
			if err != nil {
				t.Fatal(err)
			}
			env.StateHash = sum[:]
			if raw, err = canonical.Marshal(&env); err != nil {
				t.Fatal(err)
			}
			if err := fs.Put(context.Background(), z.Key, raw); err != nil {
				t.Fatal(err)
			}
		}
		var m *recovery.Metrics
		_, rep, err := recoverOnce(t, h, dir, func(o *recovery.Options) { m = o.Metrics })
		var rmm *sim.RestoreMismatch
		if !errors.As(err, &rmm) || recovery.ExitCode(err) != recovery.ExitRestore || rmm.RoundTick != uint64(newest.Tick) {
			t.Fatalf("err %v (exit %d), want a restore mismatch at round %d", err, recovery.ExitCode(err), newest.Tick)
		}
		if rep.Replayed != 0 || rep.Round.Tick != newest.Tick {
			t.Fatalf("replayed %d from round %d: nothing may be replayed and no other round tried", rep.Replayed, rep.Round.Tick)
		}
		if testutil.ToFloat64(m.HashMatch()) != 0 || testutil.ToFloat64(m.Failures.WithLabelValues(recovery.ReasonRestore)) != 1 ||
			testutil.ToFloat64(m.Restores.WithLabelValues(recovery.CallerRecovery, "hash_mismatch")) != 1 {
			t.Fatal("gauge not 0, failures{restore} not 1, or restore_total{hash_mismatch} not 1")
		}
	})

	// AC-2 last: it deletes history. Retention past the round's offset is
	// exit 3, naming the Partition, the round's offset and the log's earliest.
	// The oldest round is used: the newest has consumed the whole log, and a
	// log can't begin past its own end.
	t.Run("AC-2 a log shorter than the round is exit 3", func(t *testing.T) {
		dir := copyStore(t, h.dir)
		rounds, err := store.ListRounds(context.Background(), store.NewFS(dir), zoneIDs(t, fixtureScript))
		if err != nil {
			t.Fatal(err)
		}
		var oldest store.Round
		for _, r := range rounds {
			if r.Complete {
				oldest = r
			}
		}
		_, state, err := store.RoundAt(context.Background(), store.NewFS(dir), zoneIDs(t, fixtureScript), oldest.Tick)
		if err != nil {
			t.Fatal(err)
		}
		var target sim.PartitionOffset
		for _, po := range state.Offsets {
			if end, err := tickloop.EndOffset(context.Background(), h.bk, h.commands, po.Partition); err == nil && po.Offset > 0 && po.Offset < end {
				target = po
				break
			}
		}
		if target.Offset == 0 {
			t.Skip("no Partition has a Command past the oldest round's offset to truncate below")
		}
		cl, err := kgo.NewClient(kgo.SeedBrokers(h.bk...))
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		del := kadm.Offsets{}
		del.Add(kadm.Offset{Topic: h.commands, Partition: target.Partition, At: target.Offset + 1})
		resp, err := kadm.NewClient(cl).DeleteRecords(context.Background(), del)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Error(); err != nil {
			t.Fatal(err)
		}
		tick := oldest.Tick
		_, _, err = recoverOnce(t, h, dir, func(o *recovery.Options) { o.Round = &tick })
		var lg *tickloop.LogGapError
		if !errors.As(err, &lg) || recovery.ExitCode(err) != recovery.ExitLogGap || lg.Partition != target.Partition || lg.Need != target.Offset || lg.Have != target.Offset+1 {
			t.Fatalf("round %d: err %v (exit %d), want a log gap on partition %d at %d", oldest.Tick, err, recovery.ExitCode(err), target.Partition, target.Offset)
		}
	})
}

// republish copies the events topic to a fresh one with the boundary at tick
// carrying a different State Hash.
func republish(t *testing.T, h *history, tick sim.Tick) string {
	t.Helper()
	_, events2 := topics(t, h.bk, "doctored")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(h.bk...), kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{h.events: {tickloop.BoundaryPartition: kgo.NewOffset().AtStart()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	end, err := tickloop.EndOffset(ctx, h.bk, h.events, tickloop.BoundaryPartition)
	if err != nil {
		t.Fatal(err)
	}
	for next := int64(0); next < end; {
		fetches := cl.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			t.Fatal(err)
		}
		fetches.EachRecord(func(r *kgo.Record) {
			next = r.Offset + 1
			value := r.Value
			if string(r.Key) == tickloop.BoundaryKey {
				var tc logv1.TickCompleted
				if err := proto.Unmarshal(r.Value, &tc); err != nil {
					t.Fatal(err)
				}
				if tc.GetTick() == uint64(tick) {
					tc.StateHash[0] ^= 0xff
					if value, err = canonical.Marshal(&tc); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := cl.ProduceSync(ctx, &kgo.Record{Topic: events2, Partition: tickloop.BoundaryPartition, Key: r.Key, Value: value}).FirstErr(); err != nil {
				t.Fatal(err)
			}
		})
	}
	return events2
}

// DoD (moved from AW-SRV-028): the recovery test includes a handoff in flight.
// alice's Move east is logged and applied, and every Arrive the process
// produces is lost, so a Transit record stands in the rounds it takes. It is
// SIGKILLed with alice in Transit, a second process recovers from a round, and
// the first live ticks retry: she arrives in wilds exactly once.
func TestRecoveryWithAHandoffInFlight(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk, "handoff")
	dir := t.TempDir()
	produceCommands(t, bk, commands, []*logv1.LoggedCommand{simtest.Bind("town", "alice", "Alice", "plaza")})
	child := spawn(t, fixtureCross, "lossy", bk, commands, events, dir)
	t.Cleanup(func() { _ = child.Process.Signal(syscall.SIGKILL); _ = child.Wait() })
	produceCommands(t, bk, commands, []*logv1.LoggedCommand{simtest.Move("town", "alice", "east")})

	fs := store.NewFS(dir)
	eventually.Observed(t, 60*time.Second, "a complete round with alice in Transit, and boundaries past it", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, state, ok, err := store.NewestComplete(ctx, fs, zoneIDs(t, fixtureCross))
		if err != nil || !ok {
			return false, fmt.Sprint("no complete round ", err)
		}
		for _, z := range state.Zones {
			if z.ID == "town" && len(z.Transit) == 1 {
				bs, err := tickloop.ReadBoundaries(ctx, bk, events)
				return err == nil && len(bs) >= int(r.Tick)+3, fmt.Sprintf("round %d, %d boundaries", r.Tick, len(bs))
			}
		}
		return false, fmt.Sprintf("round %d has no Transit", r.Tick)
	})
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	before, err := tickloop.ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}

	recoverCross := func() (*sim.Engine, recovery.Report, error) {
		o, closeReader := options(t, fixtureCross, bk, commands, events, dir)
		defer closeReader()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return recovery.Recover(ctx, o)
	}
	r, rep, err := recoverCross()
	if err != nil {
		t.Fatalf("recovery after the SIGKILL: %v", err)
	}
	if rep.Round.Tick == 0 || r.StateHash() != before[len(before)-1].StateHash {
		t.Fatalf("recovered from round %d to hash %x, the log's last is %x", rep.Round.Tick, r.StateHash(), before[len(before)-1].StateHash)
	}
	st := r.State()
	if len(st.Zones["town"].Transit) != 1 || st.Zones["town"].Entities["alice"] != nil || st.Zones["wilds"].Entities["alice"] != nil {
		t.Fatalf("alice should be in town's Transit and nowhere else at the recovered hash: transit %v", st.Zones["town"].Transit)
	}

	// The second life: it recovers and runs lossless. Its first live ticks
	// retry from the recovered Transit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, err := tickloop.NewKafkaSource(ctx, tickloop.KafkaSourceOptions{Brokers: bk, Group: "andara-rec-second-" + commands[len(commands)-8:], Start: r.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := tickloop.NewKafkaPublisher(ctx, bk, "recovery-it-second")
	if err != nil {
		t.Fatal(err)
	}
	pub.Commands, pub.Events = commands, events
	off, err := tickloop.NewSnapshotter(tickloop.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	loop, err := tickloop.New(tickloop.Options{
		Engine: r, Source: src, Publisher: pub, Snapshotter: off, AwaitBoundaryAck: true,
		TickRate: 50, TickBudget: 20 * time.Millisecond, MaxPerTick: 3, DrainTimeout: 5 * time.Second, CheckpointEvery: 5,
		Log: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.SetObserver(loop)
	tickloop.WireBoundaries(pub, loop, off, nil)
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	defer func() { cancel(); <-done }()

	var after *sim.Engine
	eventually.Observed(t, 60*time.Second, "alice arrived in wilds after the second life's retries", func() (bool, string) {
		var err error
		var arep recovery.Report
		after, arep, err = recoverCross()
		if err != nil {
			return false, err.Error()
		}
		s := after.State()
		return s.Zones["wilds"].Entities["alice"] != nil && len(s.Zones["town"].Transit) == 0, fmt.Sprintf("tick %d from round %d", after.Tick(), arep.Round.Tick)
	})
	s := after.State()
	if s.Zones["town"].Entities["alice"] != nil || s.Zones["wilds"].Placed["alice"].Seq != 1 || s.Zones["wilds"].Entities["alice"].HandoffSeq != 1 {
		t.Fatalf("alice should have been placed exactly once, at sequence 1: marks %v", s.Zones["wilds"].Placed)
	}
}

// AC-7: recovery of the sizing fixture with a 600-tick tail, timed per phase,
// with its peak RSS, written to recovery-timing.json for the CI job summary.
// It runs the fixture for minutes, so it's off unless ANDARA_RECOVERY_TIMING is
// set: the CI job sets it, and ANDARA_RECOVERY_TIMING_OUT names the artifact.
func TestRecoveryTimingAtSizingScale(t *testing.T) {
	if os.Getenv("ANDARA_RECOVERY_TIMING") == "" {
		t.Skip("set ANDARA_RECOVERY_TIMING=1 to run the sizing-scale recovery (minutes)")
	}
	bk := brokers(t)
	commands, events := topics(t, bk, "sizing")
	dir := t.TempDir()
	child := spawn(t, fixtureSizing, "lossless", bk, commands, events, dir)
	t.Cleanup(func() { _ = child.Process.Signal(syscall.SIGKILL); _ = child.Wait() })

	// The tail is the ticks past the newest round, and it is 600 or more when
	// the process is killed: the round interval is longer than 600 ticks cost.
	const tail = 600
	fs := store.NewFS(dir)
	var round sim.Tick
	eventually.Observed(t, 8*time.Minute, "a round, and a tail of 600 ticks past it", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, _, ok, err := store.NewestComplete(ctx, fs, zoneIDs(t, fixtureSizing))
		if err != nil || !ok {
			return false, fmt.Sprint("no complete round ", err)
		}
		bs, err := tickloop.ReadBoundaries(ctx, bk, events)
		if err != nil {
			return false, err.Error()
		}
		round = r.Tick
		return len(bs) >= int(r.Tick)+tail, fmt.Sprintf("round %d, head %d", r.Tick, len(bs))
	})
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	want, err := tickloop.ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}

	o, closeReader := options(t, fixtureSizing, bk, commands, events, dir)
	defer closeReader()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	e, rep, err := recovery.Recover(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if e.StateHash() != want[len(want)-1].StateHash {
		t.Fatalf("recovered hash %x, the log's last is %x", e.StateHash(), want[len(want)-1].StateHash)
	}
	phases := map[string]float64{}
	for ph, d := range rep.Phases {
		phases[ph] = d.Seconds()
	}
	out := map[string]any{
		"round_tick": uint64(rep.Round.Tick), "tail_ticks": rep.Replayed, "entities": simtest.SizingEntities, "rooms": simtest.SizingRooms,
		"zones": simtest.SizingZones, "characters": simtest.SizingCharacters, "peak_rss_bytes": peakRSS(),
		"phases_seconds": phases, "head_tick": uint64(e.Tick()),
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("ANDARA_RECOVERY_TIMING_OUT")
	if path == "" {
		path = filepath.Join(t.TempDir(), "recovery-timing.json")
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recovery-timing.json (%s):\n%s", path, body)
	if rep.Replayed < tail || rep.Round.Tick != round {
		t.Fatalf("tail %d from round %d, want at least %d from round %d", rep.Replayed, rep.Round.Tick, tail, round)
	}
	if total := rep.Phases[recovery.PhaseTotal]; total >= 90*time.Second {
		t.Fatalf("total %s, want under 90s", total)
	}
}

// peakRSS is this process's high-water resident set in bytes (VmHWM), 0 where
// /proc isn't there.
func peakRSS() int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			var kb int64
			if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%d", &kb); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}

// DoD (inherited from AW-SRV-015): 50 Characters play, the process is killed,
// and the World recovers from a round with every body where it stood. The one
// linkdead at the kill still is, with its four fields; the other 49 are the
// bodies no Session holds, which recovery's caller marks linkdead, and every
// one of the 50 then rebinds with none despawned.
func TestFiftyCharactersSurviveAKill(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk, "fifty")
	dir := t.TempDir()
	spawnRooms := []struct{ zone, room string }{{"town", "plaza"}, {"docks", "pier"}, {"wilds", "trail"}}
	var cmds []*logv1.LoggedCommand
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = fmt.Sprintf("ch-%02d", i)
		sr := spawnRooms[i%len(spawnRooms)]
		cmds = append(cmds, simtest.Bind(sr.zone, ids[i], "Char"+ids[i], sr.room))
	}
	cmds = append(cmds, simtest.MarkLinkdead("town", ids[0], 100000, 6000, 300000))
	produceCommands(t, bk, commands, cmds)

	child := spawn(t, fixtureCross, "lossless", bk, commands, events, dir)
	t.Cleanup(func() { _ = child.Process.Signal(syscall.SIGKILL); _ = child.Wait() })
	fs := store.NewFS(dir)
	eventually.Observed(t, 60*time.Second, "a round holding all 50 bodies, with a tail", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, state, ok, err := store.NewestComplete(ctx, fs, zoneIDs(t, fixtureCross))
		if err != nil || !ok {
			return false, fmt.Sprint("no complete round ", err)
		}
		bodies := 0
		for _, z := range state.Zones {
			bodies += len(z.Entities)
		}
		bs, err := tickloop.ReadBoundaries(ctx, bk, events)
		return err == nil && bodies == 50 && len(bs) >= int(r.Tick)+3, fmt.Sprintf("round %d holds %d bodies", r.Tick, bodies)
	})
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	want, err := tickloop.ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}

	o, closeReader := options(t, fixtureCross, bk, commands, events, dir)
	defer closeReader()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e, rep, err := recovery.Recover(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Round.Tick == 0 || e.StateHash() != want[len(want)-1].StateHash {
		t.Fatalf("recovered from round %d to %x, the log's last is %x", rep.Round.Tick, e.StateHash(), want[len(want)-1].StateHash)
	}
	orphans := e.PresentCharacters()
	if len(orphans) != 49 {
		t.Fatalf("%d bodies with no Session, want 49 (one was linkdead at the kill)", len(orphans))
	}
	if ent := e.State().Zones["town"].Entities[sim.EntityID(ids[0])]; ent == nil || !ent.Linkdead() || ent.LinkdeadDeadline-ent.LinkdeadSince != 100000 {
		t.Fatalf("the body linkdead at the kill lost its fields in the round: %+v", ent)
	}

	// What the roster produces for those 49 (Roster.MarkOrphans), then the 50
	// reconnects: every body present again, none despawned.
	apply := func(cmds ...*logv1.LoggedCommand) {
		t.Helper()
		var in sim.TickInput
		for _, c := range cmds {
			p := sim.CommandPartition(c)
			next := e.State().Offsets[p] + int64(countOn(in, p))
			in.Records = append(in.Records, sim.Record{Partition: p, Offset: next, Command: c})
		}
		if _, err := e.Step(in); err != nil {
			t.Fatal(err)
		}
	}
	var marks, binds []*logv1.LoggedCommand
	for _, b := range orphans {
		marks = append(marks, simtest.MarkLinkdead(string(b.Zone), string(b.ID), 100000, 6000, 300000))
	}
	for i, id := range ids {
		sr := spawnRooms[i%len(spawnRooms)]
		binds = append(binds, simtest.Bind(sr.zone, id, "Char"+id, sr.room))
	}
	apply(marks...)
	if n := len(e.LinkdeadBodies()); n != 50 {
		t.Fatalf("%d bodies linkdead after the marks, want 50", n)
	}
	apply(binds...)
	if n := len(e.PresentCharacters()); n != 50 || len(e.LinkdeadBodies()) != 0 {
		t.Fatalf("after the reconnects %d bodies present and %d linkdead, want 50 and 0", n, len(e.LinkdeadBodies()))
	}
	for _, z := range e.State().Zones {
		for id, ent := range z.Entities {
			if ent.Dormant {
				t.Errorf("%s despawned", id)
			}
		}
	}
}

// countOn is how many records in already target partition p.
func countOn(in sim.TickInput, p int32) int {
	n := 0
	for _, r := range in.Records {
		if r.Partition == p {
			n++
		}
	}
	return n
}

// TestRecoveryAC12Child is the process AC-12 measures: recovery and nothing
// else, so its peak RSS is recovery's. It prints one line for the parent.
func TestRecoveryAC12Child(t *testing.T) {
	spec := os.Getenv("ANDARA_RECOVERY_AC12_CHILD")
	if spec == "" {
		t.Skip("child mode only")
	}
	parts := strings.Split(spec, "|")
	o, closeReader := options(t, parts[4], strings.Split(parts[0], ","), parts[1], parts[2], parts[3])
	defer closeReader()
	_, rep, err := recovery.Recover(context.Background(), o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	ms := func(ph string) int64 { return rep.Phases[ph].Milliseconds() }
	fmt.Printf("AC12 load=%d seek=%d replay=%d total=%d replayed=%d round=%d rss=%d\n",
		ms(recovery.PhaseLoad), ms(recovery.PhaseSeek), ms(recovery.PhaseReplay), ms(recovery.PhaseTotal), rep.Replayed, rep.Round.Tick, peakRSS())
}

type ac12Run struct{ load, seek, replay, total, replayed, round, rss int64 }

func runAC12Child(t *testing.T, fix string, bk []string, commands, events, dir string) ac12Run {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestRecoveryAC12Child$", "-test.v")
	cmd.Env = append(os.Environ(), "ANDARA_RECOVERY_AC12_CHILD="+strings.Join(bk, ",")+"|"+commands+"|"+events+"|"+dir+"|"+fix)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("recovery child: %v\n%s", err, out)
	}
	var r ac12Run
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "AC12 "); ok {
			if _, err := fmt.Sscanf(rest, "load=%d seek=%d replay=%d total=%d replayed=%d round=%d rss=%d", &r.load, &r.seek, &r.replay, &r.total, &r.replayed, &r.round, &r.rss); err != nil {
				t.Fatalf("child line %q: %v", line, err)
			}
			return r
		}
	}
	t.Fatalf("the child printed no AC12 line:\n%s", out)
	return r
}

// AC-12: with ANDARA_AC12_HISTORY_TICKS ticks of history ahead of the round
// (864000 is 24 h at 10 Hz) and a 10-tick tail, recovery finds the round's own
// boundary by binary search rather than reading the history: its load + seek +
// replay is within 1 s of the same run with no history, and its peak RSS within
// 10%. The history is filler (a boundary and an Event per tick), written to a
// second set of topics; the World is fabricated at the round's tick, as
// AW-SRV-019's AC-6 run does. Off unless the variable is set: 24 h of
// boundaries is hundreds of MB of broker, so the record says which broker it
// ran on.
func TestSeekIsBoundedByTheRoundNotTheHistory(t *testing.T) {
	raw := os.Getenv("ANDARA_AC12_HISTORY_TICKS")
	if raw == "" {
		t.Skip("ANDARA_AC12_HISTORY_TICKS not set; AC-12 is a measurement, run on a throwaway broker")
	}
	var history int
	if _, err := fmt.Sscanf(raw, "%d", &history); err != nil || history < 10 {
		t.Fatalf("ANDARA_AC12_HISTORY_TICKS=%q: want a tick count of at least 10", raw)
	}
	bk := brokers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// A World that has been running for `history` ticks: the sizing fixture,
	// restored at tick H with the round's own hash learned the way AC-6 does.
	// The fixture is what makes the RSS bound mean something: 10% of a 30 MB
	// process is the noise of one allocator arena.
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	w, err := simtest.SizingWorld()
	if err != nil {
		t.Fatal(err)
	}
	cfg := sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()}
	e0, err := simtest.SizingEngine(7)
	if err != nil {
		t.Fatal(err)
	}
	putRound := func(fs *store.FS, e *sim.Engine) {
		t.Helper()
		for _, s := range e.SnapshotAll(1) {
			body, err := s.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := fs.Put(ctx, s.Key(), body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := e0.Step(sim.TickInput{}); err != nil {
		t.Fatal(err)
	}
	scratch := store.NewFS(t.TempDir())
	putRound(scratch, e0)
	_, state, ok, err := store.NewestComplete(ctx, scratch, zoneIDs(t, fixtureSizing))
	if err != nil || !ok {
		t.Fatalf("the fabricated round: ok %v %v", ok, err)
	}
	state.Tick, state.RecordedHash = sim.Tick(history), make([]byte, 32)
	var rm *sim.RestoreMismatch
	if _, err := sim.RestoreEngine(w, reg, cfg, state); !errors.As(err, &rm) {
		t.Fatalf("learning the fabricated round's hash: %v", err)
	}
	state.RecordedHash = rm.Restored
	live, err := sim.RestoreEngine(w, reg, cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	putRound(store.NewFS(dir), live)
	boundaries := []sim.TickCompleted{{Tick: live.Tick(), Offsets: live.State().Offsets, StateHash: live.StateHash(), StateVersion: sim.StateVersion}}
	for range 10 {
		res, err := live.Step(sim.TickInput{})
		if err != nil {
			t.Fatal(err)
		}
		boundaries = append(boundaries, res.Completed)
	}

	commands, withHistory := topics(t, bk, "ac12-history")
	_, quiet := topics(t, bk, "ac12-quiet")

	cl, err := kgo.NewClient(kgo.SeedBrokers(bk...), kgo.RecordPartitioner(kgo.ManualPartitioner()), kgo.ProducerBatchMaxBytes(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	write := func(topic string, tcs ...sim.TickCompleted) {
		t.Helper()
		recs := make([]*kgo.Record, 0, 2*len(tcs))
		for _, tc := range tcs {
			body, err := canonical.Marshal(tc.Proto())
			if err != nil {
				t.Fatal(err)
			}
			recs = append(recs,
				&kgo.Record{Topic: topic, Partition: tickloop.BoundaryPartition, Key: []byte("z00"), Value: body},
				&kgo.Record{Topic: topic, Partition: tickloop.BoundaryPartition, Key: []byte(tickloop.BoundaryKey), Value: body})
		}
		if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	for from := 1; from < history; from += 5000 {
		var batch []sim.TickCompleted
		for tick := from; tick < from+5000 && tick < history; tick++ {
			like := boundaries[0]
			like.Tick = sim.Tick(tick)
			batch = append(batch, like)
		}
		write(withHistory, batch...)
	}
	write(withHistory, boundaries...)
	write(quiet, boundaries...)

	none := runAC12Child(t, fixtureSizing, bk, commands, quiet, dir)
	full := runAC12Child(t, fixtureSizing, bk, commands, withHistory, dir)
	t.Logf("AC-12: %d ticks of history, round at tick %d, %d-tick tail", history, history, len(boundaries)-1)
	t.Logf("  no history: load %d ms, seek %d ms, replay %d ms, total %d ms, peak RSS %d bytes", none.load, none.seek, none.replay, none.total, none.rss)
	t.Logf("  history:    load %d ms, seek %d ms, replay %d ms, total %d ms, peak RSS %d bytes", full.load, full.seek, full.replay, full.total, full.rss)
	if none.round != int64(history) || full.round != int64(history) || none.replayed != 10 || full.replayed != 10 {
		t.Fatalf("round %d/%d replayed %d/%d, want round %d and 10 ticks both times", none.round, full.round, none.replayed, full.replayed, history)
	}
	if extra := (full.load + full.seek + full.replay) - (none.load + none.seek + none.replay); extra > 1000 {
		t.Errorf("the history cost recovery %d ms more, past 1 s: it reads history it never replays", extra)
	}
	if none.rss > 0 && float64(full.rss) > 1.10*float64(none.rss) {
		t.Errorf("peak RSS %d bytes with history, %d without: more than 10%% over", full.rss, none.rss)
	}
}
