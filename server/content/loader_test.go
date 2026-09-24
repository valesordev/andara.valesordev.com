// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

func testLoader(t *testing.T, s Store, packs ...string) (*Loader, *Metrics) {
	t.Helper()
	m := NewMetrics(nil)
	return NewLoader(LoaderOptions{Store: s, Packs: packs, Metrics: m}), m
}

func serving(t *testing.T, l *Loader, pack string) uint64 {
	t.Helper()
	return l.Versions()[pack]
}

// The retained-version rule, which is the whole point of the loader: a version
// that does not load changes nothing. Not the World, not the previous
// version's place in it, not the process.
func TestLoader_RefusedVersionLeavesThePreviousOneServing(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("town", 7, 0, map[string]string{"town.json": zoneJSON("town", "square")})

	l, m := testLoader(t, s, "andara.core", "town")
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("boot: %v %v", rejects, err)
	}
	if got := serving(t, l, "town"); got != 7 {
		t.Fatalf("serving town@%d", got)
	}
	zonesBefore, _ := l.Inputs()

	// town@8 is written by a newer compiler than this binary understands.
	s.publish("town", 8, 0, map[string]string{
		"town.json": fmt.Sprintf(`{"formatVersion":%d,"id":"town","name":"Town",
			"rooms":[{"id":"square","title":"Square","description":"d"}]}`, sim.MaxFormatVersion+1),
	})
	rejects := l.Apply(context.Background(), PointerMove{Pack: "town", Version: 8})
	if len(rejects) != 1 || rejects[0].Reason != ReasonFormatVersion {
		t.Fatalf("rejects = %+v", rejects)
	}
	if got := serving(t, l, "town"); got != 7 {
		t.Errorf("town is serving %d; the refused version must not displace 7 (AC-4)", got)
	}
	zonesAfter, _ := l.Inputs()
	if len(zonesAfter) != len(zonesBefore) {
		t.Errorf("World inputs changed on a refused load: %d -> %d", len(zonesBefore), len(zonesAfter))
	}
	if n := testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonFormatVersion)); n != 1 {
		t.Errorf("load_failures{format_version} = %v", n)
	}
	if v := testutil.ToFloat64(m.ActiveVersion.WithLabelValues("town")); v != 7 {
		t.Errorf("andara_content_active_version{town} = %v, want the version actually serving", v)
	}
}

// AC-1: boot from the Active Pointers, and report what each pack is serving.
func TestLoader_BootResolvesEveryFollowedPointer(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("town", 7, 3, map[string]string{"town.json": zoneJSON("town", "square")})

	l, m := testLoader(t, s, AllPacks)
	rejects, err := l.LoadAll(context.Background())
	if err != nil || len(rejects) != 0 {
		t.Fatalf("boot: %v %v", rejects, err)
	}
	if v := l.Versions(); v["andara.core"] != 3 || v["town"] != 7 {
		t.Fatalf("versions = %v", v)
	}
	if got := testutil.ToFloat64(m.ActiveVersion.WithLabelValues("andara.core")); got != 3 {
		t.Errorf("core gauge = %v", got)
	}
	if got := testutil.ToFloat64(m.ActiveVersion.WithLabelValues("town")); got != 7 {
		t.Errorf("town gauge = %v", got)
	}
	zones, _ := l.Inputs()
	if len(zones) != 2 {
		t.Errorf("both packs' Zones build one World, got %d", len(zones))
	}
}

// AC-8 is a state machine, not a check. A pack compiled against a core that has
// not shipped yet is held, and it loads when core catches up — without the
// Builder having to publish a second time.
func TestLoader_CoreSkewHoldsThePackUntilCoreCatchesUp(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	// town@8 was compiled against andara.core@4, which is not active.
	s.publish("town", 8, 4, map[string]string{"town.json": zoneJSON("town", "square")})

	l, m := testLoader(t, s, AllPacks)
	rejects, err := l.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rejects) != 1 || rejects[0].Reason != ReasonCoreVersion {
		t.Fatalf("rejects = %+v, want one core_version", rejects)
	}
	if _, ok := l.Versions()["town"]; ok {
		t.Fatal("town must not be serving while core is behind")
	}
	if held := l.Held(); held["town"] != 8 {
		t.Fatalf("held = %v, want town@8 (AC-8)", held)
	}
	if msg := rejects[0].Err.Error(); !containsAll(msg, "andara.core@4", "andara.core@3") {
		t.Errorf("message %q must name both core versions (AC-8)", msg)
	}

	// Core catches up. town's own pointer does NOT move again.
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	out := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 4})
	for _, r := range out {
		t.Errorf("unexpected rejection after core caught up: %+v", r)
	}
	if got := serving(t, l, "town"); got != 8 {
		t.Fatalf("town is serving %d; core@4 must release the held version (AC-8)", got)
	}
	if held := l.Held(); len(held) != 0 {
		t.Errorf("held = %v, want empty once released", held)
	}
	if n := testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonCoreVersion)); n != 1 {
		t.Errorf("core_version failures = %v; releasing must not count as a new failure", n)
	}
}

// A pack compiled against an OLDER core is fine. Only a core newer than the one
// running is skew — otherwise every pack would have to be republished whenever
// core moved.
func TestLoader_PackCompiledAgainstAnOlderCoreLoads(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 5, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("town", 2, 3, map[string]string{"town.json": zoneJSON("town", "square")})

	l, _ := testLoader(t, s, AllPacks)
	rejects, err := l.LoadAll(context.Background())
	if err != nil || len(rejects) != 0 {
		t.Fatalf("rejects = %+v err = %v", rejects, err)
	}
	if got := serving(t, l, "town"); got != 2 {
		t.Errorf("town serving %d", got)
	}
}

// One Builder's bad pack must not keep every other Builder's good one off the
// World — the reason LoadAll reports rejections rather than returning an error.
func TestLoader_OneBadPackDoesNotKeepTheOthersOff(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("good", 1, 0, map[string]string{"good.json": zoneJSON("good", "hall")})
	s.publish("bad", 1, 0, map[string]string{"bad.json": `{"formatVersion":1,"id":"bad"` /* truncated */})

	l, _ := testLoader(t, s, AllPacks)
	rejects, err := l.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rejects) != 1 || rejects[0].Pack != "bad" {
		t.Fatalf("rejects = %+v", rejects)
	}
	v := l.Versions()
	if v["good"] != 1 || v["andara.core"] != 1 {
		t.Errorf("versions = %v; a bad pack must not take the good ones down", v)
	}
	if _, ok := v["bad"]; ok {
		t.Error("the bad pack must not be serving")
	}
}

// AC-5: a version that fails referential validation is refused, and the
// previous one keeps serving. The finding is the loader's own, unchanged.
func TestLoader_ValidationFailureIsRefusedAndRetained(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, m := testLoader(t, s, "andara.core")
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A dangling Exit: the target Room does not exist.
	s.publish("andara.core", 2, 0, map[string]string{"core.json": `{"formatVersion":1,"id":"core","name":"Core",
		"rooms":[{"id":"void","title":"Void","description":"d",
			"exits":[{"direction":"north","toRoom":"nowhere"}]}]}`})
	rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 2})
	if len(rejects) != 1 || rejects[0].Reason != ReasonValidation {
		t.Fatalf("rejects = %+v", rejects)
	}
	if got := serving(t, l, "andara.core"); got != 1 {
		t.Errorf("serving %d, want the retained 1 (AC-5)", got)
	}
	if n := testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonValidation)); n != 1 {
		t.Errorf("validation failures = %v", n)
	}
	if fs := Findings(rejects[0].Err); len(fs) == 0 {
		t.Error("a refused version must carry the validator's findings")
	}
}

// A warning is advisory by construction. Refusing a version for an unconnected
// Room would make the loader useless to the Builder it is meant to help.
func TestLoader_WarningsDoNotRefuseAVersion(t *testing.T) {
	s := newFakeStore()
	// Two Rooms, no Exits between them: orphan_room, a warning.
	s.publish("andara.core", 1, 0, map[string]string{"core.json": `{"formatVersion":1,"id":"core","name":"Core",
		"rooms":[{"id":"a","title":"A","description":"d"},{"id":"b","title":"B","description":"d"}]}`})
	l, _ := testLoader(t, s, "andara.core")
	rejects, err := l.LoadAll(context.Background())
	if err != nil || len(rejects) != 0 {
		t.Fatalf("rejects = %+v err = %v", rejects, err)
	}
	if got := serving(t, l, "andara.core"); got != 1 {
		t.Errorf("serving %d", got)
	}
}

// ...unless the operator asked for them to be fatal.
func TestLoader_StrictOrphansRefusesTheSameVersion(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": `{"formatVersion":1,"id":"core","name":"Core",
		"rooms":[{"id":"a","title":"A","description":"d"},{"id":"b","title":"B","description":"d"}]}`})
	l := NewLoader(LoaderOptions{Store: s, Packs: []string{"andara.core"}, StrictOrphans: true})
	rejects, err := l.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rejects) != 1 || rejects[0].Reason != ReasonValidation {
		t.Fatalf("rejects = %+v, want the orphan promoted to a refusal", rejects)
	}
}

// A pack nobody follows is not loaded, however loudly its pointer moves.
func TestLoader_UnfollowedPackIsIgnored(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("stranger", 1, 0, map[string]string{"s.json": zoneJSON("stranger", "here")})

	l, _ := testLoader(t, s, "andara.core")
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rejects := l.Apply(context.Background(), PointerMove{Pack: "stranger", Version: 1}); len(rejects) != 0 {
		t.Fatalf("rejects = %+v", rejects)
	}
	if _, ok := l.Versions()["stranger"]; ok {
		t.Error("an unfollowed pack must not be loaded")
	}
}

// --- Follow: debounce and ordering -----------------------------------------

type fakeWatcher struct{ ch chan PointerMove }

func (w *fakeWatcher) Watch(context.Context) (<-chan PointerMove, error) { return w.ch, nil }

// A burst that moves core and a Builder pack together must evaluate the
// Builder pack against the NEW core, not the old one. Ordering inside the
// debounce window is what makes that true.
func TestLoader_FollowAppliesCoreBeforeTheRestOfABurst(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, m := testLoader(t, s, AllPacks)
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Both published at once: abbey@8 needs core@4.
	//
	// The pack is named "abbey" on purpose. Sorting alone would put
	// "andara.core" first for a pack called "town", so a test using "town"
	// passes whether or not the core-first rule exists. "abbey" sorts BEFORE
	// "andara.core", so only the rule can save it.
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("abbey", 8, 4, map[string]string{"abbey.json": zoneJSON("abbey", "nave")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &fakeWatcher{ch: make(chan PointerMove, 4)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Follow(ctx, w, 20*time.Millisecond, nil)
	}()

	// town's move arrives first, which is the order that would fail if the
	// burst were applied as it arrived.
	w.ch <- PointerMove{Pack: "abbey", Version: 8}
	w.ch <- PointerMove{Pack: "andara.core", Version: 4}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Versions()["abbey"] == 8 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if got := l.Versions()["abbey"]; got != 8 {
		t.Fatalf("abbey serving %d; core must be applied first within a burst", got)
	}
	if held := l.Held(); len(held) != 0 {
		t.Errorf("held = %v", held)
	}
	// Applying abbey against the old core would still end with abbey serving —
	// AC-8's held-pack release rescues it when core lands a moment later. What
	// the ordering rule actually buys is that the rescue is never needed: no
	// wasted resolve, and no core_version failure counted for content that was
	// never wrong. That counter is the only thing that can tell the two apart.
	if n := testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonCoreVersion)); n != 0 {
		t.Errorf("core_version failures = %v; a burst carrying its own core must not record skew", n)
	}
}

// The debounce coalesces: a flapping pointer resolves once, not once per write.
func TestLoader_FollowCoalescesABurstIntoOneApply(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	counting := &countingStore{fakeStore: s}
	l, _ := testLoader(t, counting, "andara.core")
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := counting.manifestCalls

	for v := uint64(2); v <= 5; v++ {
		s.publish("andara.core", v, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &fakeWatcher{ch: make(chan PointerMove, 8)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Follow(ctx, w, 40*time.Millisecond, nil)
	}()
	for v := uint64(2); v <= 5; v++ {
		w.ch <- PointerMove{Pack: "andara.core", Version: v}
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Versions()["andara.core"] == 5 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if got := l.Versions()["andara.core"]; got != 5 {
		t.Fatalf("serving %d, want the last version in the burst", got)
	}
	if n := counting.manifestCalls - before; n != 1 {
		t.Errorf("resolved %d versions for a burst of 4; the debounce must coalesce to 1", n)
	}
}

type countingStore struct {
	*fakeStore
	manifestCalls int
}

func (c *countingStore) Manifest(ctx context.Context, pack string, version uint64) (*contentv1.ContentVersion, error) {
	c.manifestCalls++
	return c.fakeStore.Manifest(ctx, pack, version)
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
