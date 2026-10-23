// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
)

// AW-SRV-055: round tags and the retention sweep.

// sweepNow is the clock every sweep here runs at; fixture rounds are dated
// back from it.
var sweepNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

type retained struct {
	e      *sim.Engine
	fs     *store.FS
	owned  []sim.ZoneID
	ticks  []sim.Tick // oldest first
	remain map[int32][]sim.Record
}

// retentionFixture writes n complete rounds a tick apart, oldest first, the
// i-th taken age(i) before sweepNow.
func retentionFixture(t *testing.T, n int, age func(i int) time.Duration) *retained {
	t.Helper()
	e, err := simtest.NewEngine(11)
	if err != nil {
		t.Fatal(err)
	}
	f := &retained{e: e, fs: store.NewFS(t.TempDir()), owned: e.State().SortedZoneIDs(), remain: simtest.Script(12)}
	if len(f.owned) < 2 {
		t.Fatalf("the fixture needs two Zones, has %v", f.owned)
	}
	for i := range n {
		f.round(t, sweepNow.Add(-age(i)), true)
	}
	return f
}

// round steps one tick and writes its round, every Zone's object or (whole
// false) all but the first's.
func (f *retained) round(t *testing.T, at time.Time, whole bool) sim.Tick {
	t.Helper()
	if _, err := f.e.Step(simtest.Batch(f.remain, 2)); err != nil {
		t.Fatal(err)
	}
	snaps := f.e.SnapshotAll(at.UnixNano())
	if !whole {
		snaps = snaps[1:]
	}
	writeRound(t, f.fs, snaps)
	f.ticks = append(f.ticks, f.e.Tick())
	return f.e.Tick()
}

func (f *retained) tag(t *testing.T, tick sim.Tick, name string) store.TagResult {
	t.Helper()
	res, err := store.TagRoundAt(context.Background(), f.fs, f.owned, nil, name, tick)
	if err != nil {
		t.Fatalf("tag %d %s: %v", tick, name, err)
	}
	return res
}

func (f *retained) sweep(t *testing.T, p store.Retention) store.SweepResult {
	t.Helper()
	res, err := store.NewSweeper().Sweep(context.Background(), f.fs, f.owned, nil, p, sweepNow)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return res
}

// left is the ticks of the rounds the store still holds, oldest first.
func (f *retained) left(t *testing.T) []sim.Tick {
	t.Helper()
	rounds, err := store.ListRounds(context.Background(), f.fs, f.owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []sim.Tick
	for _, r := range rounds {
		out = append([]sim.Tick{r.Tick}, out...)
	}
	return out
}

func minutes(i int) time.Duration { return time.Duration(10-i) * time.Minute }

func pick(ticks []sim.Tick, idx ...int) []sim.Tick {
	out := make([]sim.Tick, 0, len(idx))
	for _, i := range idx {
		out = append(out, ticks[i])
	}
	return out
}

// AC-7: past keep_rounds the oldest untagged rounds go, the tagged ones stay.
func TestSweepDeletesTheOldestUntaggedAndKeepsTagged(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 7, minutes)
	f.tag(t, f.ticks[0], "deploy:v1")
	f.tag(t, f.ticks[2], "rollback:"+"2")
	res := f.sweep(t, store.Retention{KeepRounds: 3, KeepTagged: 30, MaxAge: 24 * time.Hour})
	if want := pick(f.ticks, 0, 2, 4, 5, 6); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want %v", f.left(t), want)
	}
	if res.Deleted != 2 || res.KeptTagged != 2 || res.KeptUntagged != 3 || res.NewestComplete != f.ticks[6] {
		t.Fatalf("result %+v", res)
	}
	// The deleted rounds' directories are gone, not left empty.
	for _, i := range []int{1, 3} {
		for _, z := range f.owned {
			if _, err := os.Stat(filepath.Join(f.fs.Root, strings.TrimSuffix(sim.SnapshotRoundPrefix(z, f.ticks[i]), "/"))); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("round %d zone %s: %v", f.ticks[i], z, err)
			}
		}
	}
}

// With no tag anywhere the sweep is keep_rounds alone (Data / state impact).
func TestSweepWithoutTagsIsKeepRoundsAlone(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 5, minutes)
	res := f.sweep(t, store.Retention{KeepRounds: 2, KeepTagged: 30, MaxAge: 24 * time.Hour})
	if want := pick(f.ticks, 3, 4); !slices.Equal(f.left(t), want) || res.Deleted != 3 {
		t.Fatalf("left %v (%+v), want %v", f.left(t), res, want)
	}
}

// AC-7: never the newest complete round, even when a newer incomplete round
// takes the only untagged slot.
func TestSweepKeepsTheNewestCompleteRound(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 3, minutes)
	f.round(t, sweepNow, false) // the newest, incomplete
	res := f.sweep(t, store.Retention{KeepRounds: 1, KeepTagged: 0, MaxAge: 24 * time.Hour})
	if want := pick(f.ticks, 2, 3); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want %v", f.left(t), want)
	}
	if res.NewestComplete != f.ticks[2] || res.KeptUntagged != 2 || res.Deleted != 2 {
		t.Fatalf("result %+v", res)
	}
}

// AC-8: past keep_deploy_rounds the oldest tagged rounds go; N counts tagged
// rounds, however far apart they are.
func TestSweepKeepsTheNewestTaggedRounds(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 6, func(i int) time.Duration { return time.Duration(6-i) * 72 * time.Hour })
	for _, i := range []int{0, 1, 2, 3} {
		f.tag(t, f.ticks[i], "deploy:v"+string(rune('0'+i)))
	}
	res := f.sweep(t, store.Retention{KeepRounds: 10, KeepTagged: 2, MaxAge: 552 * time.Hour})
	if want := pick(f.ticks, 2, 3, 4, 5); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want %v", f.left(t), want)
	}
	if res.KeptTagged != 2 || res.Deleted != 2 {
		t.Fatalf("result %+v", res)
	}
}

// AC-9: a round older than max_round_age goes, tagged or not, except the
// newest complete round; AC-11: and except the pinned round.
func TestSweepDeletesRoundsPastMaxAge(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 5, func(i int) time.Duration { return time.Duration(30-i) * 24 * time.Hour })
	f.tag(t, f.ticks[3], "deploy:old")
	res := f.sweep(t, store.Retention{KeepRounds: 120, KeepTagged: 30, MaxAge: 27 * 24 * time.Hour, Pin: f.ticks[1]})
	// 30, 29, 28 days are past 27; 27 days is not; the newest (26 days) is.
	if want := pick(f.ticks, 1, 3, 4); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want %v", f.left(t), want)
	}
	if res.Deleted != 2 || res.NewestComplete != f.ticks[4] {
		t.Fatalf("result %+v", res)
	}
	// Every round past the age: only the newest complete one survives.
	f.sweep(t, store.Retention{KeepRounds: 120, KeepTagged: 30, MaxAge: time.Hour})
	if want := pick(f.ticks, 4); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want only the newest complete %v", f.left(t), want)
	}
}

// AC-11: the round recovery.pin_round names is kept past every count.
func TestSweepNeverDeletesThePinnedRound(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 5, minutes)
	f.tag(t, f.ticks[0], "rollback:pinned")
	f.tag(t, f.ticks[1], "deploy:v1")
	f.sweep(t, store.Retention{KeepRounds: 1, KeepTagged: 0, MaxAge: time.Minute, Pin: f.ticks[0]})
	if want := pick(f.ticks, 0, 4); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want the pinned and the newest complete %v", f.left(t), want)
	}
}

// A round whose age can't be read is judged by the counts alone.
func TestSweepKeepsARoundItCannotDate(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 3, func(int) time.Duration { return 0 })
	// taken_at 0: SnapshotAll(0) at the fixture's clock origin.
	if _, err := f.e.Step(simtest.Batch(f.remain, 2)); err != nil {
		t.Fatal(err)
	}
	snaps := f.e.SnapshotAll(0)
	writeRound(t, f.fs, snaps)
	f.ticks = append(f.ticks, f.e.Tick())
	f.round(t, sweepNow, true)
	f.sweep(t, store.Retention{KeepRounds: 10, KeepTagged: 0, MaxAge: time.Hour})
	if want := f.ticks; !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want %v", f.left(t), want)
	}
}

// AC-12: a tag on only some of a round's objects keeps the round, isn't
// reported, and tagging again completes it.
func TestPartialTagKeepsTheRoundAndRetagCompletesIt(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 4, minutes)
	r := roundAt(t, f, f.ticks[0])
	if err := f.fs.Put(context.Background(), store.TagKey(r.Zones[0].Key, "deploy:half"), nil); err != nil {
		t.Fatal(err)
	}
	r = roundAt(t, f, f.ticks[0])
	if !r.Tagged || len(r.Tags) != 0 {
		t.Fatalf("partly tagged round: tagged=%v tags=%v", r.Tagged, r.Tags)
	}
	f.sweep(t, store.Retention{KeepRounds: 1, KeepTagged: 5, MaxAge: 24 * time.Hour})
	if want := pick(f.ticks, 0, 3); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want the partly tagged round kept: %v", f.left(t), want)
	}
	if res := f.tag(t, f.ticks[0], "deploy:half"); res.AlreadyTagged {
		t.Fatal("a re-tag that completes a partial tag reported already_tagged")
	}
	if r := roundAt(t, f, f.ticks[0]); !slices.Equal(r.Tags, []string{"deploy:half"}) {
		t.Fatalf("tags after the re-tag: %v", r.Tags)
	}
	if res := f.tag(t, f.ticks[0], "deploy:half"); !res.AlreadyTagged || res.Round.Tick != f.ticks[0] {
		t.Fatalf("the second re-tag: %+v", res)
	}
}

func roundAt(t *testing.T, f *retained, tick sim.Tick) store.Round {
	t.Helper()
	rounds, err := store.ListRounds(context.Background(), f.fs, f.owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rounds {
		if r.Tick == tick {
			return r
		}
	}
	t.Fatalf("no round at %d", tick)
	return store.Round{}
}

// AC-10, the store half: the grammar, a tick with no round, an incomplete
// round, and tick 0 as the newest complete round.
func TestTagRoundRefusals(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 2, minutes)
	incomplete := f.round(t, sweepNow, false)
	ctx := context.Background()
	for _, name := range []string{"", "deploy:", "release:v1", "deploy:a/b", "deploy:" + strings.Repeat("x", 201), "Deploy:v1"} {
		if _, err := store.TagRoundAt(ctx, f.fs, f.owned, nil, name, f.ticks[0]); !errors.Is(err, store.ErrTagName) {
			t.Errorf("name %q: %v, want ErrTagName", name, err)
		}
	}
	for _, name := range []string{"deploy:v1.2.3+build@sha256-ab_c", "rollback:4200", "deploy:" + strings.Repeat("x", 200)} {
		if !store.ValidTagName(name) {
			t.Errorf("name %q refused", name)
		}
	}
	if _, err := store.TagRoundAt(ctx, f.fs, f.owned, nil, "deploy:v1", 999999); !errors.Is(err, store.ErrRoundNotFound) {
		t.Errorf("no round: %v", err)
	}
	var ri *sim.ErrRoundIncomplete
	if _, err := store.TagRoundAt(ctx, f.fs, f.owned, nil, "deploy:v1", incomplete); !errors.As(err, &ri) || ri.Cause != sim.RoundMissing {
		t.Errorf("incomplete round: %v", err)
	}
	if r := roundAt(t, f, incomplete); r.Tagged {
		t.Error("a refused tag wrote a tag")
	}
	res, err := store.TagRoundAt(ctx, f.fs, f.owned, nil, "deploy:v1", 0)
	if err != nil || res.Round.Tick != f.ticks[1] || res.AlreadyTagged {
		t.Fatalf("tick 0: %+v %v, want the newest complete %d", res, err, f.ticks[1])
	}
	empty := store.NewFS(t.TempDir())
	if _, err := store.TagRoundAt(ctx, empty, f.owned, nil, "deploy:v1", 0); !errors.Is(err, store.ErrRoundNotFound) {
		t.Errorf("no complete round: %v", err)
	}
}

// The tags ListRounds reports are sorted, and a tag object doesn't show up as
// a snapshot key.
func TestTagsAreListedSortedAndApartFromKeys(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 1, minutes)
	f.tag(t, f.ticks[0], "rollback:1")
	f.tag(t, f.ticks[0], "deploy:z")
	f.tag(t, f.ticks[0], "deploy:a")
	if r := roundAt(t, f, f.ticks[0]); !slices.Equal(r.Tags, []string{"deploy:a", "deploy:z", "rollback:1"}) || !r.Complete {
		t.Fatalf("round %+v", r)
	}
	keys, err := f.fs.List(context.Background(), f.owned[0])
	if err != nil || len(keys) != 1 {
		t.Fatalf("List: %v %v", keys, err)
	}
}

// countingStore counts Gets, to show what a listing reads.
type countingStore struct {
	*store.FS
	gets atomic.Int64
}

func (c *countingStore) Get(ctx context.Context, key string) ([]byte, error) {
	c.gets.Add(1)
	return c.FS.Get(ctx, key)
}

// ListSnapshotRounds' limit: rounds past it, and rounds the filter refuses,
// are listed and never read.
func TestListRoundsWhereReadsOnlyWhatItReturns(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 6, minutes)
	cs := &countingStore{FS: f.fs}
	rounds, err := store.ListRoundsWhere(context.Background(), cs, f.owned, nil, 2, nil)
	if err != nil || len(rounds) != 2 || rounds[0].Tick != f.ticks[5] || !rounds[1].Complete {
		t.Fatalf("rounds %+v %v", rounds, err)
	}
	if got, want := cs.gets.Load(), int64(2*len(f.owned)); got != want {
		t.Fatalf("read %d objects for 2 rounds, want %d", got, want)
	}
	cs.gets.Store(0)
	skip := f.ticks[5]
	rounds, err = store.ListRoundsWhere(context.Background(), cs, f.owned, nil, 1, func(r store.Round) bool { return r.Tick != skip })
	if err != nil || len(rounds) != 1 || rounds[0].Tick != f.ticks[4] || cs.gets.Load() != int64(len(f.owned)) {
		t.Fatalf("filtered: %+v %v, %d reads", rounds, err, cs.gets.Load())
	}
}

// A Sweeper remembers each round's date: a second sweep over the same rounds
// reads only the newest complete round.
func TestSweeperRemembersRoundDates(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 5, minutes)
	cs := &countingStore{FS: f.fs}
	sw := store.NewSweeper()
	p := store.Retention{KeepRounds: 10, KeepTagged: 0, MaxAge: time.Hour}
	if _, err := sw.Sweep(context.Background(), cs, f.owned, nil, p, sweepNow); err != nil {
		t.Fatal(err)
	}
	cs.gets.Store(0)
	if _, err := sw.Sweep(context.Background(), cs, f.owned, nil, p, sweepNow); err != nil {
		t.Fatal(err)
	}
	if got, want := cs.gets.Load(), int64(len(f.owned)); got != want {
		t.Fatalf("second sweep read %d objects, want %d (the newest complete round)", got, want)
	}
}

// A round being written when one sweep runs is not remembered as incomplete:
// once its last object lands, the next sweep keeps it as the newest complete.
func TestSweeperRechecksANewestRoundCaughtMidWrite(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 3, minutes)
	sw := store.NewSweeper()
	p := store.Retention{KeepRounds: 10, KeepTagged: 0}
	if _, err := f.e.Step(simtest.Batch(f.remain, 2)); err != nil {
		t.Fatal(err)
	}
	snaps := f.e.SnapshotAll(sweepNow.UnixNano())
	writeRound(t, f.fs, snaps[1:])
	res, err := sw.Sweep(context.Background(), f.fs, f.owned, nil, p, sweepNow)
	if err != nil || res.NewestComplete != f.ticks[2] {
		t.Fatalf("mid-write: newest complete %d, %v; want %d", res.NewestComplete, err, f.ticks[2])
	}
	writeRound(t, f.fs, snaps[:1])
	res, err = sw.Sweep(context.Background(), f.fs, f.owned, nil, p, sweepNow)
	if err != nil || res.NewestComplete != f.e.Tick() {
		t.Fatalf("written: newest complete %d, %v; want %d", res.NewestComplete, err, f.e.Tick())
	}
}

// staleTags lists a Zone's tags as they were before any tag was written: what a
// sweep sees when a tag lands after it listed.
type staleTags struct{ *store.FS }

func (staleTags) ZoneTags(context.Context, sim.ZoneID) (map[string][]string, error) {
	return map[string][]string{}, nil
}

// A round tagged after the sweep listed it untagged is kept, tag and all: the
// sweep reads a round's tags again, under the lock, before it deletes it.
func TestSweepKeepsARoundTaggedAfterItWasListed(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 4, minutes)
	f.tag(t, f.ticks[0], "deploy:late")
	res, err := store.NewSweeper().Sweep(context.Background(), staleTags{f.fs}, f.owned, nil, store.Retention{KeepRounds: 1}, sweepNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := pick(f.ticks, 0, 3); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want %v", f.left(t), want)
	}
	if res.Deleted != 2 || res.KeptTagged != 1 || res.KeptUntagged != 1 {
		t.Fatalf("result %+v, want 2 deleted, 1 tagged and 1 untagged kept", res)
	}
	rounds, err := store.ListRounds(context.Background(), f.fs, f.owned, nil)
	if err != nil || !slices.Equal(rounds[len(rounds)-1].Tags, []string{"deploy:late"}) {
		t.Fatalf("the late tag: %+v %v", rounds, err)
	}
}

// A file under a tag directory that isn't a tag name is no tag to either
// reader, so it neither keeps a round nor stops the sweep deleting it.
func TestSweepIgnoresAFileThatIsNotATag(t *testing.T) {
	t.Parallel()
	f := retentionFixture(t, 3, minutes)
	rounds, err := store.ListRounds(context.Background(), f.fs, f.owned, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldest := rounds[len(rounds)-1]
	stray := filepath.Join(f.fs.Root, filepath.FromSlash(oldest.Zones[0].Key)+".tag", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(stray), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.sweep(t, store.Retention{KeepRounds: 2})
	if want := pick(f.ticks, 1, 2); !slices.Equal(f.left(t), want) {
		t.Fatalf("left %v, want %v", f.left(t), want)
	}
}

// The lock a tag waits on gives up with the tag's context.
func TestSweeperLockHonoursTheContext(t *testing.T) {
	t.Parallel()
	sw := store.NewSweeper()
	if err := sw.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := sw.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a held lock: %v, want the context's deadline", err)
	}
	sw.Unlock()
	if err := sw.Lock(context.Background()); err != nil {
		t.Fatalf("after Unlock: %v", err)
	}
}

// Reading the store while a sweep deletes from it: a directory the sweep
// prunes mid-walk is an empty one, not an unavailable store.
func TestListRoundsWhileASweepDeletes(t *testing.T) {
	t.Parallel()
	for attempt := range 20 {
		f := retentionFixture(t, 40, minutes)
		done := make(chan error, 1)
		go func() {
			_, err := store.NewSweeper().Sweep(context.Background(), f.fs, f.owned, nil, store.Retention{KeepRounds: 1}, sweepNow)
			done <- err
		}()
		for swept := false; !swept; {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("attempt %d: sweep: %v", attempt, err)
				}
				swept = true
			default:
			}
			if _, err := store.ListRounds(context.Background(), f.fs, f.owned, nil); err != nil {
				t.Fatalf("attempt %d: listing during the sweep: %v", attempt, err)
			}
		}
	}
}
