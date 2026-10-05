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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
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
	for _, tp := range []string{commands, events} {
		if _, err := adm.CreateTopic(ctx, sim.PartitionCount, 1, nil, tp); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, commands, events)
		cl.Close()
	})
	return commands, events
}

// options is recovery over the broker and the store at dir, the way boot builds
// it; the returned func closes the readers.
func options(t *testing.T, bk []string, commands, events, dir string) (recovery.Options, func()) {
	t.Helper()
	w, err := simtest.World()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	br, err := tickloop.NewBoundaryReader(context.Background(), bk, events, "recovery-it")
	if err != nil {
		t.Fatal(err)
	}
	fs := store.NewFS(dir)
	return recovery.Options{
		Store:      fs,
		Owned:      zoneIDs(t),
		Boundaries: br,
		OpenRecords: func(ctx context.Context, start map[int32]int64) (sim.RecordSource, error) {
			return tickloop.NewCommandSource(ctx, bk, commands, "recovery-it", start)
		},
		Config:  sim.Config{Seed: 11, Partitions: simtest.AllPartitions(), Handlers: simtest.Handlers(reg)},
		Prepare: func(map[string]uint64) (sim.Topology, error) { return sim.Topology{World: w, Templates: reg}, nil },
		Poll:    200 * time.Millisecond,
		Metrics: recovery.NewMetrics(nil),
	}, br.Close
}

func zoneIDs(t *testing.T) []sim.ZoneID {
	t.Helper()
	e, err := simtest.NewEngine(11)
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
	bk, commands, events, dir := strings.Split(parts[0], ","), parts[1], parts[2], parts[3]
	ctx := context.Background()
	o, closeReader := options(t, bk, commands, events, dir)
	e, _, err := recovery.Recover(ctx, o)
	closeReader()
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
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
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	snap, err := tickloop.NewSnapshotter(tickloop.SnapshotOptions{Store: store.NewFS(dir), Interval: 400 * time.Millisecond, UploadTimeout: 10 * time.Second, AwaitBoundaryAck: true, Log: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(3)
	}
	loop, err := tickloop.New(tickloop.Options{
		Engine: e, Source: src, Publisher: pub, Snapshotter: snap, AwaitBoundaryAck: true,
		TickRate: 50, TickBudget: 20 * time.Millisecond, MaxPerTick: 3, DrainTimeout: 5 * time.Second, CheckpointEvery: 5, Log: log,
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

func spawn(t *testing.T, bk []string, commands, events, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestRecoveryChild$", "-test.v")
	cmd.Env = append(os.Environ(), "ANDARA_RECOVERY_CHILD="+strings.Join(bk, ",")+"|"+commands+"|"+events+"|"+dir)
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
	cl, err := kgo.NewClient(kgo.SeedBrokers(bk...), kgo.RecordPartitioner(kgo.ManualPartitioner()), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var acks []ack
	for _, recs := range simtest.Script(n) {
		for _, r := range recs {
			body, err := canonical.Marshal(r.Command)
			if err != nil {
				t.Fatal(err)
			}
			res := cl.ProduceSync(context.Background(), &kgo.Record{Topic: commands, Partition: sim.CommandPartition(r.Command), Key: []byte(r.Command.GetZoneId()), Value: body})
			if err := res.FirstErr(); err != nil {
				t.Fatal(err)
			}
			acks = append(acks, ack{partition: res[0].Record.Partition, offset: res[0].Record.Offset, value: body})
		}
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
	child := spawn(t, bk, commands, events, dir)
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
		rounds, err := store.ListRounds(ctx, fs, zoneIDs(t))
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
	o, closeReader := options(t, h.bk, h.commands, h.events, dir)
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
		src, err := tickloop.NewCommandSource(context.Background(), h.bk, h.commands, "recovery-it-ack", map[int32]int64{})
		if err == nil {
			defer src.Close()
		}
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
		newest, _, ok, err := store.NewestComplete(context.Background(), fs, zoneIDs(t))
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

	// AC-2 last: it deletes history. Retention past the round's offset is
	// exit 3, naming the Partition, the round's offset and the log's earliest.
	t.Run("AC-2 a log shorter than the round is exit 3", func(t *testing.T) {
		dir := copyStore(t, h.dir)
		newest, state, ok, err := store.NewestComplete(context.Background(), store.NewFS(dir), zoneIDs(t))
		if err != nil || !ok {
			t.Fatal(err)
		}
		var target sim.PartitionOffset
		for _, po := range state.Offsets {
			if po.Offset > 0 {
				target = po
				break
			}
		}
		if target.Offset == 0 {
			t.Skip("the round holds no Command offsets to truncate below")
		}
		cl, err := kgo.NewClient(kgo.SeedBrokers(h.bk...))
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		adm := kadm.NewClient(cl)
		del := kadm.Offsets{}
		del.Add(kadm.Offset{Topic: h.commands, Partition: target.Partition, At: target.Offset + 1})
		if _, err := adm.DeleteRecords(context.Background(), del); err != nil {
			t.Fatal(err)
		}
		_, _, err = recoverOnce(t, h, dir, nil)
		var lg *tickloop.LogGapError
		if !errors.As(err, &lg) || recovery.ExitCode(err) != recovery.ExitLogGap || lg.Partition != target.Partition || lg.Need != target.Offset || lg.Have <= lg.Need {
			t.Fatalf("round %d: err %v (exit %d), want a log gap on partition %d at %d", newest.Tick, err, recovery.ExitCode(err), target.Partition, target.Offset)
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
