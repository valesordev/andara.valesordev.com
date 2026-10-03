// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valesordev/andara/content/core"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-042: has this World had Zones, from the records alone.

// swapRec is a ContentSwap record on the World Partition.
func swapRec(pack string, zones uint32, base, world byte, zoneID string) *logv1.LoggedCommand {
	cs := &logv1.ContentSwap{PackId: pack, Version: 1, ZoneCount: zones, WorldDigest: []byte{world}}
	if base != 0 {
		cs.BaseDigest = []byte{base}
	}
	return &logv1.LoggedCommand{ZoneId: zoneID, Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: cs}}
}

func worldLog(cmds ...*logv1.LoggedCommand) *memLog {
	l := &memLog{recs: simtest.MemorySource{}}
	for _, c := range cmds {
		l.recs[sim.WorldPartition] = append(l.recs[sim.WorldPartition], sim.Record{
			Partition: sim.WorldPartition, Offset: int64(len(l.recs[sim.WorldPartition])), Command: c,
		})
	}
	return l
}

// AC-3 and AC-6: the boot's decision table. The digest in effect starts
// empty; a swap that passes routing and whose base is the digest in effect
// applied, and its world_digest is then in effect. The World has had Zones
// if an applied swap carried any.
func TestWorldHadZones_FromTheRecordsAlone(t *testing.T) {
	look := &logv1.LoggedCommand{ZoneId: "", Command: &logv1.LoggedCommand_Look{Look: &logv1.Look{}}}
	for _, tc := range []struct {
		name string
		log  *memLog
		want bool
	}{
		{"an empty log", worldLog(), false},
		{"core alone, applied", worldLog(swapRec("andara.core", 0, 0, 1, "")), false},
		{"core, then a pack with Zones on it", worldLog(swapRec("andara.core", 0, 0, 1, ""), swapRec("town", 4, 1, 2, "")), true},
		{"a genesis with Zones", worldLog(swapRec("town", 4, 0, 2, "")), true},
		// stale_base: built on a World the log has moved past, or on one
		// when nothing was in effect.
		{"the only Zone-bearing swap is stale", worldLog(swapRec("andara.core", 0, 0, 1, ""), swapRec("town", 4, 9, 2, "")), false},
		{"a first swap claiming a base", worldLog(swapRec("town", 4, 9, 2, "")), false},
		// misrouted: a swap carrying a zone_id.
		{"the only Zone-bearing swap is misrouted", worldLog(swapRec("andara.core", 0, 0, 1, ""), swapRec("town", 4, 1, 2, "town")), false},
		// A refused swap leaves the digest in effect alone, so the next
		// swap is judged against the one before.
		{"a refused swap, then a good one", worldLog(swapRec("andara.core", 0, 0, 1, ""), swapRec("town", 4, 9, 7, ""), swapRec("town", 4, 1, 2, "")), true},
		{"other records are ignored", worldLog(look, swapRec("andara.core", 0, 0, 1, ""), look), false},
		// A record written before zone_count reads 0: the safe direction.
		{"a swap from before the field", worldLog(swapRec("town", 0, 0, 2, "")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := worldHadZones(context.Background(), tc.log)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("had Zones = %t, want %t", got, tc.want)
			}
		})
	}
}

// The walk agrees with the Engine. A real Engine applies a log of swaps (a
// Templates-only core, a stale swap with Zones, a misrouted one, then a good
// one), each carrying the zone_count the Loader would set. After every record,
// worldHadZones over the log so far says what the Engine's World says.
func TestWorldHadZones_AgreesWithTheEngine(t *testing.T) {
	c, err := simtest.TownVersions()
	if err != nil {
		t.Fatal(err)
	}
	c.Zones["andara.core"] = map[uint64][]*contentv1.ZoneDefinition{1: nil}
	e := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 1, Partitions: allPartitionsForTest(), Handlers: sim.Handlers(), Content: c})
	log := &memLog{recs: simtest.MemorySource{}}

	swap := func(pack string, v uint64) *logv1.LoggedCommand {
		t.Helper()
		cmd, err := c.Swap(e, pack, v)
		if err != nil {
			t.Fatal(err)
		}
		cs := cmd.GetContentSwap()
		inEffect, _ := e.Content()
		after := map[string]uint64{}
		for p, x := range inEffect {
			after[p] = x
		}
		topo, err := c.Prepare(after, cs)
		if err != nil {
			t.Fatal(err)
		}
		cs.ZoneCount = uint32(len(topo.World.Zones))
		return cmd
	}
	step := func(cmd *logv1.LoggedCommand, applies bool) {
		t.Helper()
		rec := sim.Record{Partition: sim.WorldPartition, Offset: int64(len(log.recs[sim.WorldPartition])), Command: cmd}
		log.recs[sim.WorldPartition] = append(log.recs[sim.WorldPartition], rec)
		res, err := e.Step(sim.TickInput{Records: []sim.Record{rec}})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(res.Swaps) == 1; got != applies {
			t.Fatalf("the Engine applied %v (refused %v), want applied=%t", res.Swaps, res.SwapsRefused, applies)
		}
		had, err := worldHadZones(context.Background(), log)
		if err != nil {
			t.Fatal(err)
		}
		if engine := len(e.World().Zones) > 0; had != engine {
			t.Fatalf("after %v: the walk says had Zones=%t, the Engine's World has Zones=%t", cmd.GetContentSwap(), had, engine)
		}
	}

	step(swap("andara.core", 1), true)
	stale := swap("town", 1)
	stale.GetContentSwap().BaseDigest = []byte("not the digest in effect")
	step(stale, false)
	misrouted := swap("town", 1)
	misrouted.ZoneId = "town"
	step(misrouted, false)
	step(swap("town", 1), true)
}

// syncBuffer is a log sink a test may read while the tick loop writes.
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

// liveLogs points rt's logger at a buffer the test can read while the loop
// runs; call it before the loop starts.
func liveLogs(rt *Runtime) *syncBuffer {
	sb := &syncBuffer{}
	rt.Tel.Log = slog.New(slog.NewJSONHandler(sb, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sb
}

// logLines is every JSON log line with msg.
func logLines(t *testing.T, logs interface{ String() string }, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func status(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr.Code
}

// AC-1, AC-2 and AC-7, the wait as a state machine: entered once with its
// warn line; started but unready while waiting; left on the first Zones that
// hold the spawn Room, with its info line, and ready; never ready while
// draining, and /startedz 200 to the end.
func TestWait_StartedUnreadyThenReady(t *testing.T) {
	rt, logs := runtime(t, fixture(t, "valid"), false)
	rt.Cfg.ContentSource = "kafka"
	rt.Cfg.ContentPacks = []string{"*"}
	rt.Cfg.CharacterSpawnRoom = "town/plaza"
	world, err := simtest.CrossingWorld()
	if err != nil {
		t.Fatal(err)
	}
	templates, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Handler()
	ctx := context.Background()

	if status(t, h, "/startedz") != http.StatusServiceUnavailable || status(t, h, "/readyz") != http.StatusServiceUnavailable {
		t.Fatal("started or ready before recovery finished")
	}
	rt.enterWait(ctx)
	rt.enterWait(ctx)
	if got := logLines(t, logs, "waiting for content: no Zones in effect; publish and activate a pack"); len(got) != 1 ||
		got[0]["level"] != "WARN" || got[0]["content_source"] != "kafka" || got[0]["packs"] != "*" {
		t.Fatalf("the warn line, once: %v", got)
	}
	rt.MarkStarted()
	if status(t, h, "/startedz") != http.StatusOK || status(t, h, "/readyz") != http.StatusServiceUnavailable {
		t.Fatal("waiting: want /startedz 200 and /readyz 503")
	}

	// A swap that brings no Zones, and one whose Zones lack the spawn Room,
	// don't end the wait.
	rt.leaveWait(sim.EmptyWorld(), templates, []sim.SwapApplied{{Pack: "andara.core", Version: 1}})
	rt.Cfg.CharacterSpawnRoom = "purgatory/start"
	rt.leaveWait(world, templates, []sim.SwapApplied{{Pack: "town", Version: 2}})
	if !rt.Waiting() || status(t, h, "/readyz") != http.StatusServiceUnavailable {
		t.Fatal("left the wait without the spawn Room")
	}
	if len(logLines(t, logs, "content in effect lacks the spawn Room; still waiting")) != 1 {
		t.Error("no error line for a first content without the spawn Room")
	}

	// The first Zones with the spawn Room end it.
	rt.Cfg.CharacterSpawnRoom = "town/plaza"
	tp := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	rt.leaveWait(world, templates, []sim.SwapApplied{{Pack: "town", Version: 3, TraceParent: tp}})
	if rt.Waiting() || status(t, h, "/readyz") != http.StatusOK || status(t, h, "/startedz") != http.StatusOK {
		t.Fatal("not ready after the first content")
	}
	got := logLines(t, logs, "content in effect: leaving the wait")
	if len(got) != 1 || got[0]["level"] != "INFO" || got[0]["zones"] != float64(3) || got[0]["pack"] != "town@3" ||
		got[0]["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("the leaving line: %v", got)
	}

	// AC-7: draining, /readyz reports it and /startedz stays 200.
	rt.Drain()
	if status(t, h, "/readyz") != http.StatusServiceUnavailable || status(t, h, "/startedz") != http.StatusOK {
		t.Error("drain: want /readyz 503 and /startedz 200")
	}
}

// The order of leaving and starting doesn't matter: content arriving before
// the Gateway serves makes it ready when it does, and a waiting server that
// drains never becomes ready when content arrives mid-drain (AC-7).
func TestWait_LeavingBeforeStartAndDuringDrain(t *testing.T) {
	world, _ := simtest.CrossingWorld()
	templates, _ := simtest.Templates()

	rt, _ := runtime(t, fixture(t, "valid"), false)
	rt.Cfg.CharacterSpawnRoom = "town/plaza"
	rt.enterWait(context.Background())
	rt.leaveWait(world, templates, nil)
	if rt.Ready() {
		t.Fatal("ready before the Gateway serves")
	}
	rt.MarkStarted()
	if !rt.Ready() {
		t.Fatal("not ready once it serves, with content in effect")
	}

	rt, _ = runtime(t, fixture(t, "valid"), false)
	rt.Cfg.CharacterSpawnRoom = "town/plaza"
	rt.enterWait(context.Background())
	rt.MarkStarted()
	rt.Drain()
	rt.leaveWait(world, templates, nil)
	if rt.Ready() {
		t.Fatal("a draining server became ready")
	}
	if status(t, rt.Handler(), "/startedz") != http.StatusOK {
		t.Fatal("/startedz left 200 during the drain")
	}
}

// emptyStore is a content store with nothing published and no pointers.
type emptyStore struct{}

func (emptyStore) Active(context.Context) (map[string]uint64, error) { return map[string]uint64{}, nil }
func (emptyStore) Manifest(_ context.Context, pack string, v uint64) (*contentv1.ContentVersion, error) {
	return nil, &content.ErrManifestMissing{Pack: pack, Version: v}
}
func (emptyStore) Blobs(context.Context, []*contentv1.BlobRef) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}

// memStore is a content store held in memory: pack versions as blobs by
// path, and the Active Pointers.
type memStore struct {
	mu       sync.Mutex
	active   map[string]uint64
	versions map[string]map[uint64]map[string][]byte
}

func (s *memStore) put(pack string, v uint64, blobs map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.versions[pack] == nil {
		s.versions[pack] = map[uint64]map[string][]byte{}
	}
	s.versions[pack][v] = blobs
	s.active[pack] = v
}

func (s *memStore) Active(context.Context) (map[string]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]uint64{}
	for p, v := range s.active {
		out[p] = v
	}
	return out, nil
}

func (s *memStore) Manifest(_ context.Context, pack string, v uint64) (*contentv1.ContentVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blobs, ok := s.versions[pack][v]
	if !ok {
		return nil, &content.ErrManifestMissing{Pack: pack, Version: v}
	}
	cv := &contentv1.ContentVersion{PackId: pack, Version: v}
	for p, b := range blobs {
		sum := sha256.Sum256(b)
		cv.Blobs = append(cv.Blobs, &contentv1.BlobRef{Path: p, Hash: sum[:], SizeBytes: uint64(len(b))})
	}
	return cv, nil
}

func (s *memStore) Blobs(_ context.Context, refs []*contentv1.BlobRef) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for _, vs := range s.versions {
		for _, blobs := range vs {
			for _, r := range refs {
				if b, ok := blobs[r.GetPath()]; ok {
					sum := sha256.Sum256(b)
					if string(sum[:]) == string(r.GetHash()) {
						out[r.GetPath()] = b
					}
				}
			}
		}
	}
	return out, nil
}

// AC-2 through the loop: a waiting server whose store then gains andara.core
// and a town holding the spawn Room brings them into effect through the tick
// loop, whose contentApplied ends the wait, and goes ready with no restart.
func TestReconcileContent_TheFirstZonesThroughTheLoopEndTheWait(t *testing.T) {
	store := &memStore{active: map[string]uint64{}, versions: map[string]map[uint64]map[string][]byte{}}
	rt, _ := runtime(t, t.TempDir(), false)
	logs := liveLogs(rt)
	rt.Cfg.ContentSource = content.SourceKafka
	rt.Cfg.ContentPacks = []string{content.AllPacks}
	rt.Cfg.CharacterSpawnRoom = "town/plaza"
	rt.ContentMetrics = content.NewMetrics(nil)
	rt.Content = content.OverLoader(rt.contentOptions(), content.NewLoader(content.LoaderOptions{
		Store: store, Packs: rt.Cfg.ContentPacks, SpawnRoom: sim.RoomRef{Zone: "town", Room: "plaza"},
	}))
	rt.replay = worldLog()
	// What a request could see when the content source is told: the swap
	// that brings the first Zones must reach the content source (and
	// GetServerInfo) before the wait ends and /readyz turns 200 (review of
	// #359). Set before the loop starts, which reads it.
	type atApplied struct{ zones, waiting, ready bool }
	var (
		seenMu sync.Mutex
		seen   []atApplied
	)
	rt.applied = func(swaps []sim.SwapApplied) {
		seenMu.Lock()
		seen = append(seen, atApplied{len(rt.Engine.World().Zones) > 0, rt.Waiting(), rt.Ready()})
		seenMu.Unlock()
		rt.Content.Applied(swaps)
	}
	startMemoryLoop(t, rt)
	if code := rt.ReconcileContent(context.Background()); code != ExitOK || !rt.Waiting() {
		t.Fatalf("an empty store: exit %d, waiting %t\n%s", code, rt.Waiting(), logs.String())
	}
	rt.MarkStarted()
	if rt.Ready() {
		t.Fatal("ready while waiting")
	}

	store.put(content.CorePack, 1, core.Blobs())
	store.put("town", 1, map[string][]byte{"town.json": []byte(`{"formatVersion":1,"id":"town","name":"Town","fallbackRoom":"plaza",
		"rooms":[{"id":"plaza","title":"Plaza","description":"A square."}]}`)})
	if _, err := rt.Content.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually.True(t, 5*time.Second, "the wait to end", func() bool { return !rt.Waiting() && rt.Ready() })
	if got := logLines(t, logs, "content in effect: leaving the wait"); len(got) != 1 || got[0]["zones"] != float64(1) {
		t.Fatalf("the leaving line: %v", got)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	first := -1
	for i, a := range seen {
		if a.zones {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatalf("no swap with Zones reached the content source: %+v", seen)
	}
	if a := seen[first]; !a.waiting || a.ready {
		t.Fatalf("when the first Zones' swap reached the content source the wait had already ended (waiting %t, ready %t)", a.waiting, a.ready)
	}
}

// ReconcileContent's decision (ACs 1, 3 and 6): with no Zones in effect, a
// store-backed World that has never had them waits, unready, exit 0, and does
// again on a restart over the same log; one whose log shows an applied swap
// with Zones exits 1, as it always has. A directory with no Zones exits 1
// (AW-SRV-001 AC-9, AC-4), checked by LoadContent before reconcile.
func TestReconcileContent_WaitsOnlyForAWorldThatNeverHadZones(t *testing.T) {
	reconcile := func(t *testing.T, log *memLog) (*Runtime, int, *syncBuffer) {
		t.Helper()
		rt, _ := runtime(t, t.TempDir(), false)
		logs := liveLogs(rt)
		rt.Cfg.ContentSource = content.SourceKafka
		rt.Cfg.ContentPacks = []string{content.AllPacks}
		rt.ContentMetrics = content.NewMetrics(nil)
		rt.Content = content.OverLoader(rt.contentOptions(), content.NewLoader(content.LoaderOptions{Store: emptyStore{}, Packs: rt.Cfg.ContentPacks}))
		rt.replay = log
		startMemoryLoop(t, rt)
		return rt, rt.ReconcileContent(context.Background()), logs
	}

	t.Run("never had Zones: waits, and again after a restart", func(t *testing.T) {
		for range 2 {
			rt, code, logs := reconcile(t, worldLog())
			if code != ExitOK || !rt.Waiting() || rt.Ready() {
				t.Fatalf("exit %d, waiting %t, ready %t\n%s", code, rt.Waiting(), rt.Ready(), logs.String())
			}
			if len(logLines(t, logs, "waiting for content: no Zones in effect; publish and activate a pack")) != 1 {
				t.Errorf("no warn line:\n%s", logs.String())
			}
		}
	})
	t.Run("only a refused Zone-bearing swap: waits", func(t *testing.T) {
		rt, code, logs := reconcile(t, worldLog(swapRec("town", 4, 9, 2, "")))
		if code != ExitOK || !rt.Waiting() {
			t.Fatalf("exit %d, waiting %t\n%s", code, rt.Waiting(), logs.String())
		}
	})
	t.Run("an applied swap had Zones: exits", func(t *testing.T) {
		rt, code, logs := reconcile(t, worldLog(swapRec("town", 4, 0, 2, "")))
		if code != ExitFail || rt.Waiting() {
			t.Fatalf("exit %d, waiting %t\n%s", code, rt.Waiting(), logs.String())
		}
		if !strings.Contains(logs.String(), "no content in effect") {
			t.Errorf("no exit line:\n%s", logs.String())
		}
	})
	t.Run("a directory with no Zones: exits", func(t *testing.T) {
		rt, logs := runtime(t, t.TempDir(), false)
		if code := rt.LoadContent(context.Background()); code != ExitFail {
			t.Fatalf("dir with no Zones: exit %d\n%s", code, logs.String())
		}
	})
}

// #326: a source with no Active Pointers has nothing to wait on, so a
// read-only consumer without a World still fails, as it did before.
func TestWaitForContent_ADirectoryHasNothingToWaitOn(t *testing.T) {
	rt, _ := runtime(t, t.TempDir(), false)
	if err := rt.WaitForContent(context.Background()); err == nil || !strings.Contains(err.Error(), "no Active Pointers to wait on") {
		t.Fatalf("no content: %v", err)
	}
	rt.Content, _ = content.Open(context.Background(), rt.contentOptions())
	if err := rt.WaitForContent(context.Background()); err == nil {
		t.Fatal("a directory waited")
	}
	if rt.Waiting() {
		t.Error("waiting on a directory")
	}
}

// Review of #334: a reload that finds no World retries on a backoff, not
// only on the next pointer move. A store-backed load that can't read the
// store returns no World and no error, and the move that triggered it is
// consumed, so without the retry a transient failure on the move that
// brought the Zones would leave the projector waiting for a move that may
// never come.
func TestWaitLoop_RetriesWithoutAMove(t *testing.T) {
	rt, _ := runtime(t, t.TempDir(), false)
	logs := liveLogs(rt)
	rt.waitRetry = 5 * time.Millisecond
	rt.waiting.Store(true)
	moves := make(chan content.PointerMove, 1)
	moves <- content.PointerMove{Pack: "town", Version: 1}
	var loads int
	// The move's own load fails transiently; the retries after it find the
	// World. No second move comes.
	reload := func() (bool, error) { loads++; return loads >= 3, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.waitLoop(ctx, moves, reload, func() int { return 1 }); err != nil {
		t.Fatalf("the wait didn't end with no second move: %v (%d loads)", err, loads)
	}
	if rt.Waiting() || loads != 3 {
		t.Errorf("waiting %t after %d loads", rt.Waiting(), loads)
	}
	if got := logLines(t, logs, "content in effect: leaving the wait"); len(got) != 1 || got[0]["pack"] != "town@1" {
		t.Errorf("the leaving line: %v", got)
	}
	// A reload error ends the wait with it.
	failing := func() (bool, error) { return false, errors.New("boom") }
	if err := rt.waitLoop(ctx, make(chan content.PointerMove), failing, func() int { return 0 }); err == nil || err.Error() != "boom" {
		t.Errorf("a failed reload: %v", err)
	}
}
