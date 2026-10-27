// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package boot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kadm"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/kafkaclient/kafkatest"
	"github.com/valesordev/andara/server/roster"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/telemetry"
	"github.com/valesordev/andara/server/tickloop"
)

// AW-SRV-063: AW-SRV-032's name-reservation and crashed-body purge
// guarantees, end to end. Every server here boots the way main does — the
// account store and the tick loop over the broker, recovery on every boot, the
// roster's sweep on its timer — over its own it<nonce> topics on the local
// stack's Redpanda, and a crash is a server that stops without releasing its
// Sessions.

// purgeWorld is one World's topics, snapshot store and token keys, shared by
// the servers that boot over it in turn.
type purgeWorld struct {
	bk      kafkaclient.Config
	nonce   string
	topics  kafkaclient.Topics
	snaps   string
	keyFile string
}

// The two log lines a re-routed purge produces, in this order (Observability
// requirements), and the sweep's line for the PurgeCharacter it produced.
const (
	lineRerouted = "character purge re-routed"
	lineApplied  = "character purge applied"
	lineProduced = "character purge produced"
)

func newPurgeWorld(t testing.TB) *purgeWorld {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	bk := kafkatest.Config(strings.Split(v, ","))
	nonce := fmt.Sprint(time.Now().UnixNano() % 1_000_000_000)
	tp, err := kafkaclient.NewTopics("it" + nonce)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := kafkaclient.New(bk.For("andara-it"))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Partitions and cleanup policy as deploy/kafka/topics.yaml declares them.
	compact := map[string]*string{"cleanup.policy": kadm.StringPtr("compact")}
	for _, c := range []struct {
		name  string
		parts int32
		cfg   map[string]*string
	}{
		{tp.Commands(), sim.PartitionCount, nil},
		{tp.Events(), sim.PartitionCount, nil},
		{tp.Accounts(), 6, compact},
		{tp.Audit(), 6, nil},
	} {
		if _, err := adm.CreateTopic(ctx, c.parts, 1, c.cfg, c.name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteGroups(dctx, "andara-sim-it"+nonce)
		_, _ = adm.DeleteTopics(dctx, tp.Commands(), tp.Events(), tp.Accounts(), tp.Audit())
		cl.Close()
	})
	dir := t.TempDir()
	keys := filepath.Join(dir, "token-keys")
	if err := os.WriteFile(keys, []byte("k1: "+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snaps := filepath.Join(dir, "snapshots")
	if err := os.Mkdir(snaps, 0o755); err != nil {
		t.Fatal(err)
	}
	return &purgeWorld{bk: bk, nonce: nonce, topics: tp, snaps: snaps, keyFile: keys}
}

type bootOpts struct {
	// snapshots takes a round every 500ms; off, nothing writes a round.
	snapshots bool
	// pinRound is recovery.pin_round.
	pinRound sim.Tick
	// orphans stands in for the roster's release of the bodies a crash left
	// standing: noOrphans is a release whose produce failed.
	orphans orphanReleaser
	// handlers replaces the verb handlers: AC-4's mutant.
	handlers map[sim.CommandKind]sim.Apply
}

// noOrphans is MarkOrphans whose produce failed: the body a crash left stays
// present, with no Session (AW-SRV-014 AC-11).
type noOrphans struct{}

func (noOrphans) MarkOrphans(context.Context, []sim.CharacterBody) (int, int) { return 0, 1 }

// purgeServer is one boot over the World, running until stop.
type purgeServer struct {
	rt     *Runtime
	logs   *syncBuffer
	loop   *tickloop.Loop
	events *eventLog
	// start is the Tick recovery left the World at.
	start sim.Tick
	stop  func()
}

// boot runs main's sequence (cmd/andara-server) up to the Gateway, which
// nothing here needs, and starts the retention sweep.
func (w *purgeWorld) boot(t testing.TB, o bootOpts) *purgeServer {
	t.Helper()
	ctx := context.Background()
	cfg, err := config.Parse([]string{"--validate-only"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ValidateOnly = false
	cfg.ServiceName, cfg.Environment, cfg.LogLevel, cfg.OTLPEndpoint = "andara-server", "it"+w.nonce, "info", ""
	content, err := filepath.Abs(filepath.Join("..", "..", "testdata", "content", "valid"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ContentSource, cfg.ContentPath = "dir", content
	cfg.KafkaBrokers, cfg.KafkaTopicPrefix = w.bk.Brokers, "it"+w.nonce
	cfg.AuthStore, cfg.AuthTokenKeyFile = "kafka", w.keyFile
	cfg.AuthArgon2MemoryKiB, cfg.AuthArgon2Time, cfg.AuthArgon2Threads = 64, 1, 1
	cfg.SimSource, cfg.SimSeed, cfg.SimPartitions = "kafka", 5, allPartitionsForTest()
	cfg.CharacterSpawnRoom = "town/plaza"
	// character.delete_retention, shortened: deleted_unix is whole seconds,
	// so a deleted Character expires within two.
	cfg.CharacterDeleteRetention, cfg.CharacterPurgeSweepInterval = time.Second, 100*time.Millisecond
	// Long enough that the purge lands inside the grace, short enough that
	// the test sees the deadline pass (AC-3).
	cfg.SessionLinkdeadGrace = 8 * time.Second
	cfg.SnapshotStore, cfg.SnapshotFSPath, cfg.SnapshotInterval = "fs", w.snaps, 0
	if o.snapshots {
		cfg.SnapshotInterval = 500 * time.Millisecond
	}
	cfg.RecoveryPinRound = uint64(o.pinRound)

	tel := telemetry.Setup(cfg, &bytes.Buffer{})
	t.Cleanup(func() { tel.Shutdown(context.Background()) })
	rt, err := New(cfg, tel)
	if err != nil {
		t.Fatal(err)
	}
	rt.Kafka = w.bk
	logs := liveLogs(rt)
	if code := rt.LoadContent(ctx); code != ExitOK {
		t.Fatalf("load: exit %d\n%s", code, logs.String())
	}
	if err := rt.LoadVerbs(ctx); err != nil {
		t.Fatal(err)
	}
	accounts, err := rt.OpenAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rt.StartEvents()
	if err := rt.StartIngress(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartRoster(ctx); err != nil {
		t.Fatal(err)
	}
	rt.orphans, rt.handlers = o.orphans, o.handlers
	loop, err := rt.StartTickLoop(ctx)
	if err != nil {
		t.Fatalf("tick loop: %v\n%s", err, logs.String())
	}
	s := &purgeServer{rt: rt, logs: logs, loop: loop, events: &eventLog{}, start: rt.Engine.Tick()}
	// After recovery, as the fan-out is: only live Events.
	rt.Engine.Subscribe(s.events)

	loopCtx, stopLoop := context.WithCancel(ctx)
	loopDone := make(chan error, 1)
	go func() { loopDone <- loop.Run(loopCtx) }()
	sweepCtx, stopSweep := context.WithCancel(ctx)
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			stopSweep()
			stopLoop()
			<-loopDone
			_ = rt.CloseIngress()
			_ = accounts.Close()
		})
	}
	t.Cleanup(s.stop)

	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if code := rt.ReconcileContent(rctx); code != ExitOK {
		t.Fatalf("reconcile: exit %d\n%s", code, logs.String())
	}
	go rt.Roster.RunSweep(sweepCtx)
	return s
}

// tick is the last Tick the loop completed.
func (s *purgeServer) tick() sim.Tick {
	n, _ := s.loop.Live()
	return s.start + sim.Tick(n)
}

// account creates an Account, as an operator does.
func (s *purgeServer) account(t testing.TB, name string) string {
	t.Helper()
	op := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	id, err := s.rt.Accounts.CreateAccount(op, name, "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func session(id, account string) *gateway.Session {
	return &gateway.Session{ID: id, Principal: auth.Principal{AccountID: account, Roles: []auth.Role{auth.RolePlayer}}}
}

func (s *purgeServer) purges(outcome string) float64 {
	return testutil.ToFloat64(s.rt.Roster.Metrics().Purges.WithLabelValues(outcome))
}

func (s *purgeServer) characters(state string) float64 {
	return testutil.ToFloat64(s.rt.Roster.Metrics().Characters.WithLabelValues(state))
}

// lines is every JSON log line whose msg is one of msgs and whose
// character_id is id ("" for any), in the order they were written.
func (s *purgeServer) lines(id string, msgs ...string) []map[string]any {
	var out []map[string]any
	for l := range strings.SplitSeq(s.logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil || (id != "" && m["character_id"] != id) {
			continue
		}
		for _, msg := range msgs {
			if m["msg"] == msg {
				out = append(out, m)
			}
		}
	}
	return out
}

// eventLog is an EventSink that keeps every Event it is handed. Publish runs
// on the loop goroutine.
type eventLog struct {
	mu  sync.Mutex
	all []sim.Event
}

func (l *eventLog) Publish(e sim.Event) {
	l.mu.Lock()
	l.all = append(l.all, e)
	l.mu.Unlock()
}

func (l *eventLog) of(typ sim.EventType) []sim.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []sim.Event
	for _, e := range l.all {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// purgeCommands is every PurgeCharacter on the commands topic's Partitions
// for zones, read back from the broker.
func (w *purgeWorld) purgeCommands(t testing.TB, zones ...sim.ZoneID) []*logv1.LoggedCommand {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kafkaclient.New(w.bk.For("andara-it"))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ends, err := kadm.NewClient(cl).ListEndOffsets(ctx, w.topics.Commands())
	if err != nil {
		t.Fatal(err)
	}
	start := map[int32]int64{}
	for _, z := range zones {
		start[sim.PartitionFor(z)] = 0
	}
	src, err := tickloop.NewCommandSource(ctx, w.bk, w.topics.Commands(), "andara-it", start)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var out []*logv1.LoggedCommand
	for p := range start {
		end, ok := ends.Lookup(w.topics.Commands(), p)
		if !ok || end.Offset == 0 {
			continue
		}
		recs, err := src.Fetch(p, 0, end.Offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			if r.Command.GetPurgeCharacter() != nil {
				out = append(out, r.Command)
			}
		}
	}
	return out
}

func reasonOf(err error) string {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return ""
	}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if info, ok := v.(*errdetails.ErrorInfo); ok {
				return info.GetReason()
			}
		}
	}
	return ""
}

// AC-1 (AW-SRV-032 AC-7 with AC-8): a Character created after round T keeps
// its name through a rollback to T — recovery.pin_round, the server side of
// `make rollback ROUND=T` — and the sweep that follows does not reclaim it.
func TestPurge_ARollbackKeepsTheNameReserved(t *testing.T) {
	w := newPurgeWorld(t)
	ctx := context.Background()
	s1 := w.boot(t, bootOpts{snapshots: true})
	alice, bob := s1.account(t, "alice"), s1.account(t, "bob")

	ws, err := store.Open(store.Options{Kind: "fs", FSPath: w.snaps})
	if err != nil {
		t.Fatal(err)
	}
	var round sim.Tick
	eventually.Observed(t, 30*time.Second, "a complete snapshot round", func() (bool, string) {
		r, _, ok, err := store.NewestComplete(ctx, ws, s1.rt.listedZones(), s1.rt.zonesAt())
		if err != nil {
			return false, err.Error()
		}
		round = r.Tick
		return ok && r.Tick > 0, fmt.Sprintf("ok=%v tick=%d", ok, r.Tick)
	})
	if _, err := s1.rt.Roster.CreateCharacter(ctx, session("s-create", alice), "Aldric"); err != nil {
		t.Fatal(err)
	}
	// A newer round than T, so a recovery that ignored the pin would start
	// from it and not from T.
	eventually.Observed(t, 30*time.Second, "a complete round newer than the pinned one", func() (bool, string) {
		r, _, ok, err := store.NewestComplete(ctx, ws, s1.rt.listedZones(), s1.rt.zonesAt())
		if err != nil {
			return false, err.Error()
		}
		return ok && r.Tick > round, fmt.Sprintf("ok=%v tick=%d, pinned %d", ok, r.Tick, round)
	})
	s1.stop()

	s2 := w.boot(t, bootOpts{pinRound: round})
	if l := s2.lines("", "recovered from the log"); len(l) != 1 || l[0]["round_tick"] != float64(round) {
		t.Fatalf("recovered %v, want one recovery from round %d", l, round)
	}
	refused := func(when string) {
		t.Helper()
		for _, c := range []struct{ account, name string }{{bob, "Aldric"}, {bob, "ALDRIC"}, {alice, "aldric"}} {
			_, err := s2.rt.Roster.CreateCharacter(ctx, session("s-"+c.name, c.account), c.name)
			if connect.CodeOf(err) != connect.CodeAlreadyExists || reasonOf(err) != roster.ReasonNameTaken {
				t.Fatalf("%s: create %q from %s: %v, want ALREADY_EXISTS name_taken", when, c.name, c.account, err)
			}
		}
	}
	refused("after the rollback")

	before := s2.purges(roster.PurgeReclaimed)
	if res := s2.rt.Roster.Sweep(ctx); res.Reclaimed != 0 || res.Failed != 0 {
		t.Fatalf("sweep after the rollback: %+v", res)
	}
	if got := s2.purges(roster.PurgeReclaimed); got != before {
		t.Fatalf("andara_character_purges_total{outcome=reclaimed} %v, was %v", got, before)
	}
	refused("after the sweep")
}

// Where purgeACrashedBody failed: setup, or one of AC-2's assertions.
const (
	stageSetup  = "setup"
	stageAssert = "assert"
)

// purgeACrashedBody is AC-2, or AC-3 with linkdead: a crash leaves the body
// in Zone B (docks) while the roster names Zone A (town); the Character is
// deleted, its retention runs out, and the roster's sweep and the sim purge it
// between them. stage records how far it got, for AC-4.
func purgeACrashedBody(t testing.TB, linkdead bool, handlers map[sim.CommandKind]sim.Apply, stage *string) {
	t.Helper()
	*stage = stageSetup
	w := newPurgeWorld(t)
	ctx := context.Background()

	s1 := w.boot(t, bootOpts{handlers: handlers})
	alice := s1.account(t, "alice")
	resp, err := s1.rt.Roster.CreateCharacter(ctx, session("s-create", alice), "Aldric")
	if err != nil {
		t.Fatal(err)
	}
	id := resp.GetCharacter().GetCharacterId()
	if ref, err := s1.rt.Accounts.Character(alice, id); err != nil || ref.GetZoneId() != "town" {
		t.Fatalf("roster entry %v, %v: want zone_id town", ref, err)
	}
	// The body a Session left in docks before the crash, with the roster's
	// write of the move lost in the arrival-to-write window (ObserveMove).
	if _, err := s1.rt.commandLog.Produce(ctx, &logv1.LoggedCommand{
		ZoneId: "docks", ActorId: id, SessionId: "s-crashed",
		Command: &logv1.LoggedCommand_BindCharacter{BindCharacter: &logv1.BindCharacter{
			CharacterId: id, AccountId: alice, Name: "Aldric", SpawnRoomId: "pier",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	eventually.True(t, 10*time.Second, "the body present in docks", func() bool { return s1.characters(roster.StatePresent) == 1 })
	s1.stop() // the crash: the Session is never released

	o := bootOpts{handlers: handlers, orphans: noOrphans{}}
	if linkdead {
		o.orphans = nil // the roster marks it linkdead, as after any crash
	}
	s2 := w.boot(t, o)
	var markedAt sim.Tick
	if linkdead {
		eventually.True(t, 10*time.Second, "the body marked linkdead", func() bool {
			ev := s2.events.of(sim.EvCharacterLinkdead)
			if len(ev) == 1 {
				markedAt = ev[0].Tick
			}
			return len(ev) == 1
		})
	}
	// The state the body is counted under, read before the delete.
	var present, dormant float64
	eventually.Observed(t, 10*time.Second, "the recovered body counted", func() (bool, string) {
		present, dormant = s2.characters(roster.StatePresent), s2.characters(roster.StateDormant)
		return present == 1, fmt.Sprintf("present=%v dormant=%v", present, dormant)
	})
	okBefore := s2.purges(roster.PurgeOK)
	if _, err := s2.rt.Roster.DeleteCharacter(ctx, session("s-delete", alice), id); err != nil {
		t.Fatal(err)
	}

	*stage = stageAssert
	eventually.Observed(t, 15*time.Second, "andara_character_purges_total{outcome=ok} up by one", func() (bool, string) {
		got := s2.purges(roster.PurgeOK)
		return got == okBefore+1, fmt.Sprintf("%v, was %v", got, okBefore)
	})
	eventually.Observed(t, 10*time.Second, "andara_characters_total down by one where the body was counted", func() (bool, string) {
		p, d := s2.characters(roster.StatePresent), s2.characters(roster.StateDormant)
		return p == present-1 && d == dormant, fmt.Sprintf("present=%v dormant=%v, were %v %v", p, d, present, dormant)
	})
	// The gauge is set after ObservePurges on the same tick, so the apply
	// has been counted by now: a re-route counted as well shows here.
	if got := s2.purges(roster.PurgeOK); got != okBefore+1 {
		t.Fatalf("andara_character_purges_total{outcome=ok} %v after the apply, was %v: want exactly one more", got, okBefore)
	}
	pier := sim.RoomRef{Zone: "docks", Room: "pier"}
	if ds := s2.events.of(sim.EvCharacterDespawned); len(ds) != 1 || ds[0].Scope.Room != pier ||
		ds[0].Envelope.GetCharacterDespawned().GetReason() != sim.DespawnPurge || ds[0].Envelope.GetCharacterDespawned().GetCharacterName() != "Aldric" {
		t.Fatalf("CharacterDespawned %v: want one, reason purge, to docks/pier", ds)
	}
	if ps := s2.events.of(sim.EvCharacterPurged); len(ps) != 1 || ps[0].Zone != "docks" || len(ps[0].Scope.Entities) != 1 || ps[0].Scope.Entities[0] != sim.EntityID(id) {
		t.Fatalf("CharacterPurged %v: want one, in docks, to the Character", ps)
	}
	// The two info lines, in order, under the sweep's one trace.
	ls := s2.lines(id, lineRerouted, lineApplied)
	if len(ls) != 2 || ls[0]["msg"] != lineRerouted || ls[0]["rerouted"] != true ||
		ls[1]["msg"] != lineApplied || ls[1]["zone"] != "docks" || ls[1]["was_present"] != true || ls[1]["rerouted"] != false {
		t.Fatalf("purge lines %v", ls)
	}
	if tr := ls[0]["trace_id"]; tr == "" || tr == nil || tr != ls[1]["trace_id"] {
		t.Fatalf("trace_id %v and %v: want one, non-empty", ls[0]["trace_id"], ls[1]["trace_id"])
	}
	// The sweep logs it after MarkPurged, which the sim does not wait on.
	eventually.Observed(t, 10*time.Second, "one purge produced, to town", func() (bool, string) {
		p := s2.lines(id, lineProduced)
		return len(p) == 1 && p[0]["zone"] == "town", fmt.Sprint(p)
	})
	purgedAt := sim.Tick(ls[1]["tick"].(float64))

	if linkdead {
		// The deadline the MarkLinkdead set passes: nothing despawns again.
		grace, _, _ := s2.rt.Cfg.LinkdeadTicks()
		deadline := markedAt + sim.Tick(grace)
		if purgedAt >= deadline {
			t.Fatalf("purged at tick %d, not before the linkdead deadline %d", purgedAt, deadline)
		}
		eventually.True(t, 30*time.Second, "the linkdead deadline passed", func() bool { return s2.tick() > deadline+2 })
		if ds := s2.events.of(sim.EvCharacterDespawned); len(ds) != 1 {
			t.Fatalf("CharacterDespawned %v past the deadline: want only the purge's", ds)
		}
		if ls := s2.lines(id, lineApplied); len(ls) != 1 {
			t.Fatalf("%d %q lines past the deadline, want 1", len(ls), lineApplied)
		}
	}
	s2.stop()
	if _, ok := s2.rt.Engine.State().Zones["docks"].Entities[sim.EntityID(id)]; ok {
		t.Fatal("the body is still in docks' state")
	}

	// One PurgeCharacter produced, to town, and the sim's re-route to docks.
	cmds := w.purgeCommands(t, "town", "docks")
	var town, docks int
	for _, c := range cmds {
		if c.GetActorId() != id {
			continue
		}
		switch c.GetZoneId() {
		case "town":
			town++
		case "docks":
			docks++
		}
	}
	if town != 1 || docks != 1 {
		t.Fatalf("PurgeCharacter on the log: %d to town, %d to docks; want 1 and 1", town, docks)
	}

	// The log replayed from the beginning: recovery proves the State Hash at
	// every Tick Boundary Record, through the purge.
	s3 := w.boot(t, bootOpts{orphans: noOrphans{}})
	s3.stop()
	if l := s3.lines("", "recovered from the log"); len(l) != 1 || l[0]["round_tick"] != float64(0) || sim.Tick(l[0]["tick"].(float64)) < purgedAt {
		t.Fatalf("replay %v: want one from the beginning, through tick %d", l, purgedAt)
	}
	if _, ok := s3.rt.Engine.State().Zones["docks"].Entities[sim.EntityID(id)]; ok {
		t.Fatal("the replayed World still holds the body")
	}
}

// AC-2: a present body.
func TestPurge_ACrashedPresentBodyThroughRosterAndSim(t *testing.T) {
	var stage string
	purgeACrashedBody(t, false, nil, &stage)
}

// AC-3: a linkdead body, and its deadline passing after the purge.
func TestPurge_ACrashedLinkdeadBodyThroughRosterAndSim(t *testing.T) {
	var stage string
	purgeACrashedBody(t, true, nil, &stage)
}

// recordingTB is a testing.TB whose failures are recorded rather than
// reported, so a test can assert that another fails. Fatal stops the
// goroutine, as it would the test's.
type recordingTB struct {
	testing.TB
	mu     sync.Mutex
	failed bool
	msg    string
}

func (r *recordingTB) record(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.failed {
		r.failed, r.msg = true, msg
	}
}
func (r *recordingTB) Fail()                     { r.record("Fail") }
func (r *recordingTB) FailNow()                  { r.record("FailNow"); goruntime.Goexit() }
func (r *recordingTB) Error(a ...any)            { r.record(fmt.Sprint(a...)) }
func (r *recordingTB) Errorf(f string, a ...any) { r.record(fmt.Sprintf(f, a...)) }
func (r *recordingTB) Fatal(a ...any)            { r.record(fmt.Sprint(a...)); goruntime.Goexit() }
func (r *recordingTB) Fatalf(f string, a ...any) { r.record(fmt.Sprintf(f, a...)); goruntime.Goexit() }
func (r *recordingTB) Failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed
}

// AC-4: AC-2's test fails against a mutant that routes the purge to the
// roster's zone_id only — the sim never looks for the body in another Zone —
// and fails in its assertions, not in its setup.
func TestPurge_AC2FailsWithoutTheReroute(t *testing.T) {
	if os.Getenv("ANDARA_KAFKA_BROKERS") == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	purge := sim.Handlers()[sim.KindPurgeCharacter]
	mutant := sim.Handlers()
	mutant[sim.KindPurgeCharacter] = func(a *sim.ApplyContext, cmd *logv1.LoggedCommand) error {
		if _, here := a.Zone.Entities[sim.EntityID(cmd.GetActorId())]; !here {
			return nil
		}
		return purge(a, cmd)
	}
	rec := &recordingTB{TB: t}
	var stage string
	done := make(chan struct{})
	go func() {
		defer close(done)
		purgeACrashedBody(rec, false, mutant, &stage)
	}()
	<-done
	if !rec.Failed() {
		t.Fatal("AC-2's test passed with the re-route disabled")
	}
	if stage != stageAssert {
		t.Fatalf("AC-2's test failed in %s, not in its assertions: %s", stage, rec.msg)
	}
	t.Logf("AC-2 against the mutant: %s", rec.msg)
}
