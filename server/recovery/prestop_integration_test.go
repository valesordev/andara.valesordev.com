// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

// AW-SRV-054's integration test against the broker and the filesystem store
// (`make up`, then `make test-integration`): pre-stop on a running loop takes a
// complete round tagged deploy:<tag>, commits its tick, and the hash it
// reports is the one the log's boundary records and recovery reproduces.
package recovery_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/lifecycle"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// noStreams and noIngress stand in for the Gateway and the Ingress: this test
// is about the round, the tag and the commit.
type noStreams struct{}

func (noStreams) AnnounceStopping(string, time.Duration) int { return 0 }

type noIngress struct{}

func (noIngress) RefuseRestarting()                 {}
func (noIngress) WaitAdmitted(context.Context) bool { return true }

func TestPrestopRoundIsTaggedCommittedAndRecovered(t *testing.T) {
	bk := brokers(t)
	commands, events := topics(t, bk, "prestop")
	dir := t.TempDir()
	submit(t, bk, commands, 20)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o, closeReader := options(t, fixtureScript, bk, commands, events, dir)
	e, _, err := recovery.Recover(ctx, o)
	closeReader()
	if err != nil {
		t.Fatal(err)
	}
	src, err := tickloop.NewKafkaSource(ctx, tickloop.KafkaSourceOptions{Brokers: bk, Group: "andara-prestop-" + commands[len(commands)-8:], Start: e.State().Offsets, Topic: commands, LagEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := tickloop.NewKafkaPublisher(ctx, bk, "recovery-it-prestop")
	if err != nil {
		t.Fatal(err)
	}
	pub.Commands, pub.Events = commands, events
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// No periodic round inside the test: the only round is pre-stop's.
	snap, err := tickloop.NewSnapshotter(tickloop.SnapshotOptions{Store: store.NewFS(dir), Interval: time.Hour, MaxStall: time.Second, UploadTimeout: 10 * time.Second, AwaitBoundaryAck: true, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	loop, err := tickloop.New(tickloop.Options{
		Engine: e, Source: src, Publisher: pub, Snapshotter: snap, AwaitBoundaryAck: true,
		TickRate: 50, TickBudget: 20 * time.Millisecond, MaxPerTick: 3, DrainTimeout: 5 * time.Second,
		// Far past the run, so the commit the test sees is the round's own.
		CheckpointEvery: 100000, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.SetObserver(loop)
	tickloop.WireBoundaries(pub, loop, snap, nil)
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	logs := &bytes.Buffer{}
	svc := lifecycle.New(lifecycle.Options{
		Announcer: noStreams{}, Refuser: noIngress{}, Rounds: loop.Snapshotter(), Store: loop.Snapshotter().Store(),
		Committed: loop.CommittedThrough, NoticeLead: 0, AdmitWait: time.Second, ExpectedBack: time.Minute,
		ImageTag: "sha-it", UploadTimeout: 10 * time.Second, TickInterval: loop.Interval(),
		Log: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
	defer ccancel()
	res, err := svc.PrepareStop(cctx, connect.NewRequest(&adminv1.PrepareStopRequest{}))
	if err != nil {
		t.Fatalf("PrepareStop: %v", err)
	}
	resp := res.Msg
	if resp.GetOutcome() != adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE || resp.GetTag() != "deploy:sha-it" {
		t.Fatalf("response %v; log:\n%s", resp, logs)
	}
	tick := sim.Tick(resp.GetTick())
	if got := loop.CommittedThrough(); got < tick {
		t.Fatalf("committed through %d, want at least the round's tick %d", got, tick)
	}
	cancel()
	<-done

	bs, err := tickloop.ReadBoundaries(context.Background(), bk, events)
	if err != nil {
		t.Fatal(err)
	}
	var boundary *sim.TickCompleted
	for i := range bs {
		if bs[i].Tick == tick {
			boundary = &bs[i]
		}
	}
	if boundary == nil || !bytes.Equal(boundary.StateHash[:], resp.GetStateHash()) {
		t.Fatalf("the log's boundary at tick %d is %v; pre-stop reported hash %x", tick, boundary, resp.GetStateHash())
	}

	// Recovery from the newest round finds pre-stop's, tagged on every object,
	// verifies it against that boundary, and keeps the head's timestamp (AC-8).
	o, closeReader = options(t, fixtureScript, bk, commands, events, dir)
	defer closeReader()
	rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rcancel()
	_, rep, err := recovery.Recover(rctx, o)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if rep.Round.Tick != tick || rep.HeadProducedAt.IsZero() {
		t.Fatalf("recovered from round %d (head produced at %v), want pre-stop's round %d", rep.Round.Tick, rep.HeadProducedAt, tick)
	}
	keys := make([]string, len(rep.Round.Zones))
	for i, z := range rep.Round.Zones {
		keys[i] = z.Key
	}
	if tags, err := store.RoundTags(rctx, store.NewFS(dir), keys); err != nil || !reflect.DeepEqual(tags, []string{"deploy:sha-it"}) {
		t.Fatalf("the restored round's tags = %v, %v", tags, err)
	}
}
