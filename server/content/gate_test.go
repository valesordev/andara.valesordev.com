// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/sim"
)

// The publish gate's report (errors.md §1 rule 10, #312). The incumbent is
// pack town (Zones town, docks, wilds, purgatory; docks, wilds and purgatory
// have Exits into town.plaza), and the publisher is pack acme, which sorts
// before town, as sre.verify does: without that, these cases pass before the
// fix.

type rm struct {
	id    string
	exits []ex
}

type ex struct{ dir, zone, room string }

// gateZone is a Zone Definition as a compiled blob.
func gateZone(t *testing.T, id, fallback string, rooms ...rm) []byte {
	t.Helper()
	def := &contentv1.ZoneDefinition{FormatVersion: 1, Id: id, Name: id, FallbackRoom: fallback}
	for _, r := range rooms {
		rd := &contentv1.RoomDefinition{Id: r.id, Title: r.id, Description: "A place called " + r.id + "."}
		for _, e := range r.exits {
			rd.Exits = append(rd.Exits, &contentv1.ExitDefinition{Direction: e.dir, ToZone: e.zone, ToRoom: e.room})
		}
		def.Rooms = append(def.Rooms, rd)
	}
	b, err := protojson.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// packFiles is a pack's blobs: its source header and the Zone files.
func packFiles(pack string, zones map[string][]byte) map[string][]byte {
	files := map[string][]byte{"src/pack.aw": []byte("pack " + pack + " requires andara.core@1\n")}
	for p, b := range zones {
		files[p] = b
	}
	return files
}

// gateHarness holds both packs for a Builder and serves them.
func gateHarness(t *testing.T) *pubHarness {
	t.Helper()
	return newPubHarness(t, func(ao *AdminOptions, lo *LoaderOptions) {
		ao.Accounts = packHolders{alice: {"town", "acme"}, bob: {"town", "acme"}}
		lo.Packs = []string{CorePack, "town", "acme"}
	})
}

// liveAs publishes pack as alice, approves it as bob and activates it.
func (h *pubHarness) liveAs(pack string, files map[string][]byte) uint64 {
	h.t.Helper()
	pv, err := h.publish(builder(alice), pack, files)
	if err != nil {
		h.t.Fatalf("publish %s: %v", pack, err)
	}
	if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: pack, Version: pv.GetVersion()}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: pack, Version: pv.GetVersion()}); err != nil {
		h.t.Fatal(err)
	}
	h.follow(pack, pv.GetVersion())
	return pv.GetVersion()
}

// refusedWith publishes as alice, expects the gate's refusal, and returns its
// findings.
func (h *pubHarness) refusedWith(pack string, files map[string][]byte) []*contentv1.Diagnostic {
	h.t.Helper()
	_, err := h.publish(builder(alice), pack, files)
	var ae *AdminError
	if !errors.As(err, &ae) {
		h.t.Fatalf("publish %s: got %v, want a refusal", pack, err)
	}
	pf, ok := ae.Detail.(*adminv1.PublishFindings)
	if !ok || len(pf.GetFindings()) == 0 {
		h.t.Fatalf("a refusal with no findings: %v (detail %v)", err, ae.Detail)
	}
	return pf.GetFindings()
}

func codes(ds []*contentv1.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if d.GetSeverity() == contentv1.Severity_ERROR {
			out = append(out, d.GetCode())
		}
	}
	return out
}

// town is the incumbent, live as pack town: AC-1's Zone `town` has Rooms
// hall and plaza.
func (h *pubHarness) townLive() uint64 { return h.live(townFiles(h.t)) }

// AC-1: a cross-pack Zone clash is one finding, on the publisher's blob, worded for both
// packs, and nothing of the incumbent's is dropped.
func TestGate_AClashIsOneFindingOnThePublishersBlob(t *testing.T) {
	h := gateHarness(t)
	n := h.townLive()
	before := len(h.auditRecords())

	files := packFiles("acme", map[string][]byte{"z.json": gateZone(t, "town", "market", rm{id: "market"})})
	got := h.refusedWith("acme", files)
	if len(got) != 1 || got[0].GetCode() != "duplicate_zone" || got[0].GetFile() != "z.json" || got[0].GetPack() != "" {
		t.Fatalf("findings = %v, want exactly one duplicate_zone on acme's z.json", got)
	}
	want := "ZoneID town declared in pack acme and in active pack town@" + itoa(n)
	if got[0].GetMessage() != want {
		t.Errorf("message = %q, want %q", got[0].GetMessage(), want)
	}
	for _, code := range []sim.ErrCode{sim.ErrUnknownRoom, sim.ErrUnknownZone, sim.ErrDuplicateRoom, sim.ErrDuplicateZone} {
		wantN := 0.0
		if code == sim.ErrDuplicateZone {
			wantN = 1
		}
		if v := testutil.ToFloat64(h.m.ValidationFailures.WithLabelValues(string(code))); v != wantN {
			t.Errorf("validation_failures_total{%s} = %v, want %v", code, v, wantN)
		}
	}
	recs := h.auditSince(before)
	if len(recs) != 1 || recs[0].GetAction() != auth.ActionReject || recs[0].GetFindingsCount() != 1 {
		t.Errorf("audit = %v, want one reject with findings_count 1", recs)
	}
}

// AC-2: the dropped Zone's Rooms are not duplicate_room.
func TestGate_TheDroppedZonesRoomsAreNotDuplicateRooms(t *testing.T) {
	h := gateHarness(t)
	h.townLive()
	files := packFiles("acme", map[string][]byte{"z.json": gateZone(t, "town", "market", rm{id: "market"}, rm{id: "plaza"})})
	if got := codes(h.refusedWith("acme", files)); !slices.Equal(got, []string{"duplicate_zone"}) {
		t.Fatalf("findings = %v, want only duplicate_zone", got)
	}
}

// AC-3: an Exit into the dropped Zone's own Room isn't reported, until the clash is fixed.
// An Exit into a Room neither declares is, beside the clash.
func TestGate_AnExitIntoTheDroppedZoneIsReportedOnlyWhenTheClashDoesNotExplainIt(t *testing.T) {
	h := gateHarness(t)
	h.townLive()
	glade := func(room string) []byte {
		return gateZone(t, "glade", "g", rm{id: "g", exits: []ex{{dir: "north", zone: "town", room: room}}})
	}
	mk := func(townID, glRoom string) map[string][]byte {
		return packFiles("acme", map[string][]byte{
			"z.json":     gateZone(t, townID, "market", rm{id: "market", exits: []ex{{dir: "south", zone: "glade", room: "g"}}}),
			"glade.json": glade(glRoom),
		})
	}
	// One finding: the Exit into town.market is the Builder's own Zone.
	if got := codes(h.refusedWith("acme", mk("town", "market"))); !slices.Equal(got, []string{"duplicate_zone"}) {
		t.Fatalf("with the clash: %v, want only duplicate_zone", got)
	}
	// The Builder renames their Zone, the clash is gone, and town.market is a
	// Room that doesn't exist in the World: the Exit's own finding.
	if got := codes(h.refusedWith("acme", mk("market-town", "market"))); !slices.Equal(got, []string{"unknown_room"}) {
		t.Fatalf("after the rename: %v, want the Exit's own unknown_room", got)
	}
	// An Exit into town.missing, which neither town's nor acme's `town`
	// declares, is reported beside the clash: the clash doesn't explain it.
	got := codes(h.refusedWith("acme", mk("town", "missing")))
	slices.Sort(got)
	if !slices.Equal(got, []string{"duplicate_zone", "unknown_room"}) {
		t.Fatalf("with the clash and a missing target: %v", got)
	}
}

// AC-4: one pack with two files declaring a Zone, and a Room twice, keeps
// both findings (rule 10.4 reaches only a clash with an active pack).
func TestGate_TwoFilesOfOnePackKeepBothFindings(t *testing.T) {
	h := gateHarness(t)
	h.townLive()
	files := packFiles("acme", map[string][]byte{
		"a.json": gateZone(t, "x", "r", rm{id: "r"}),
		"b.json": gateZone(t, "x", "r", rm{id: "r"}),
	})
	got := codes(h.refusedWith("acme", files))
	slices.Sort(got)
	if !slices.Equal(got, []string{"duplicate_room", "duplicate_zone"}) {
		t.Fatalf("findings = %v, want duplicate_zone and duplicate_room", got)
	}
}

// AC-5: attribution is by (pack, path). town and acme both hold a blob
// town.json; town's carries a missing_reverse_exit warning and acme's Zone is
// valid. A path filter would report town's.
func TestGate_AWarningInAnotherPacksFileOfTheSameNameIsNotTheirs(t *testing.T) {
	h := gateHarness(t)
	h.liveAs("town", packFiles("town", map[string][]byte{
		"town.json": gateZone(t, "town", "a", rm{id: "a", exits: []ex{{dir: "east", room: "b"}}}, rm{id: "b"}),
	}))
	resp, err := h.publish(builder(alice), "acme", packFiles("acme", map[string][]byte{
		"town.json": gateZone(t, "acme-town", "a", rm{id: "a", exits: []ex{{dir: "east", room: "b"}}}, rm{id: "b", exits: []ex{{dir: "west", room: "a"}}}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetWarnings()) != 0 {
		t.Fatalf("warnings = %v, want none: town's one-way Exit isn't acme's", resp.GetWarnings())
	}
}

// AC-6: the publisher's own warning is reported, on its own file.
func TestGate_ThePublishersOwnWarningIsReported(t *testing.T) {
	h := gateHarness(t)
	h.townLive()
	resp, err := h.publish(builder(alice), "acme", packFiles("acme", map[string][]byte{
		"z.json": gateZone(t, "glade", "a", rm{id: "a", exits: []ex{{dir: "east", room: "b"}}}, rm{id: "b"}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range resp.GetWarnings() {
		if w.GetCode() == "missing_reverse_exit" && w.GetFile() == "z.json" && w.GetPack() == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want acme's own missing_reverse_exit on z.json", resp.GetWarnings())
	}
}

// AC-7: a new version that removes a Zone another pack exits into is
// refused, and the incumbent's finding is reported as the incumbent's.
func TestGate_AnotherPacksFindingIsReportedAsItsOwn(t *testing.T) {
	h := gateHarness(t)
	town1 := packFiles("town", map[string][]byte{"town.json": gateZone(t, "town", "gate", rm{id: "gate"})})
	h.liveAs("town", town1)
	h.liveAs("acme", packFiles("acme", map[string][]byte{
		"glade.json": gateZone(t, "glade", "x", rm{id: "x", exits: []ex{{dir: "west", zone: "town", room: "gate"}}}),
	}))
	h.liveAs("town", packFiles("town", map[string][]byte{
		"town.json": gateZone(t, "town", "gate", rm{id: "gate", exits: []ex{{dir: "east", zone: "glade", room: "x"}}}),
	}))

	got := h.refusedWith("acme", packFiles("acme", map[string][]byte{
		"other.json": gateZone(t, "other", "o", rm{id: "o"}),
	}))
	if len(got) != 1 {
		t.Fatalf("findings = %v, want exactly town's", got)
	}
	f := got[0]
	if f.GetCode() != "unknown_zone" || f.GetPack() != "town" || f.GetFile() != "town.json" || len(f.GetChain()) != 0 || f.GetLine() != 0 || f.GetCol() != 0 {
		t.Fatalf("finding = %v, want town's unknown_zone on town.json with pack town, no chain, no position", f)
	}
}

// AC-8: an error in an active pack that only the publish makes visible
// (content.strict_orphans turned on after it went live) refuses the publish
// with that finding, not an empty refusal.
func TestGate_AStrictOrphanInAnActivePackRefusesWithItsFinding(t *testing.T) {
	h := gateHarness(t)
	h.liveAs("town", packFiles("town", map[string][]byte{
		"town.json": gateZone(t, "town", "a", rm{id: "a", exits: []ex{{dir: "east", room: "b"}}}, rm{id: "b", exits: []ex{{dir: "west", room: "a"}}}, rm{id: "lonely"}),
	}))
	h.loader.strictOrphans = true

	got := h.refusedWith("acme", packFiles("acme", map[string][]byte{
		"z.json": gateZone(t, "glade", "g", rm{id: "g"}),
	}))
	if len(got) != 1 || got[0].GetCode() != "orphan_room" || got[0].GetPack() != "town" || got[0].GetFile() != "town.json" {
		t.Fatalf("findings = %v, want town's orphan_room with pack town", got)
	}
}

// AC-9: a warning the publish newly causes in an active pack is reported, once.
func TestGate_AWarningThePublishNewlyCausesIsReportedOnce(t *testing.T) {
	h := gateHarness(t)
	h.liveAs("town", packFiles("town", map[string][]byte{
		"town.json": gateZone(t, "town", "gate", rm{id: "gate"}),
	}))
	h.liveAs("acme", packFiles("acme", map[string][]byte{
		"glade.json": gateZone(t, "glade", "x", rm{id: "x", exits: []ex{{dir: "west", zone: "town", room: "gate"}}}),
	}))
	// town gains an Exit into glade, and an unrelated one-way Exit in the same file.
	h.liveAs("town", packFiles("town", map[string][]byte{
		"town.json": gateZone(t, "town", "gate",
			rm{id: "gate", exits: []ex{{dir: "east", zone: "glade", room: "x"}, {dir: "north", room: "tower"}}},
			rm{id: "tower"}),
	}))

	// acme drops the reverse Exit: town's Exit east is now one-way.
	resp, err := h.publish(builder(alice), "acme", packFiles("acme", map[string][]byte{
		"glade.json": gateZone(t, "glade", "x", rm{id: "x"}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var caused []*contentv1.Diagnostic
	for _, w := range resp.GetWarnings() {
		if w.GetCode() == "missing_reverse_exit" {
			caused = append(caused, w)
		}
	}
	if len(caused) != 1 {
		t.Fatalf("warnings = %v, want exactly the one newly caused missing_reverse_exit (not town's other one-way Exit)", resp.GetWarnings())
	}
	if w := caused[0]; w.GetPack() != "town" || w.GetFile() != "town.json" || len(w.GetChain()) != 0 || w.GetLine() != 0 || w.GetCol() != 0 {
		t.Fatalf("warning = %v, want pack town on town.json, no chain, no position", w)
	}
	h.follow("acme", h.activateAs("acme", resp.GetVersion()))

	// Publishing again with no change to that Exit: town's warning is in
	// effect already, so it is not reported again.
	resp2, err := h.publish(builder(alice), "acme", packFiles("acme", map[string][]byte{
		"glade.json": gateZone(t, "glade", "x", rm{id: "x"}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range resp2.GetWarnings() {
		if w.GetPack() == "town" {
			t.Fatalf("warnings = %v: town's own warning, already in effect, was reported again", resp2.GetWarnings())
		}
	}
}

// activateAs approves and activates a published version and returns it.
func (h *pubHarness) activateAs(pack string, v uint64) uint64 {
	h.t.Helper()
	if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: pack, Version: v}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: pack, Version: v}); err != nil {
		h.t.Fatal(err)
	}
	return v
}

// The tags the gate puts on a file name never reach a Builder: a finding's text names the
// path alone.
func TestGate_NoTagReachesAFinding(t *testing.T) {
	h := gateHarness(t)
	h.townLive()
	got := h.refusedWith("acme", packFiles("acme", map[string][]byte{
		"a.json": gateZone(t, "x", "r", rm{id: "r"}),
		"b.json": gateZone(t, "x", "r", rm{id: "r"}),
	}))
	for _, d := range got {
		if strings.ContainsRune(d.GetMessage(), 0) || strings.ContainsRune(d.GetFile(), 0) {
			t.Errorf("a tag leaked into %v", d)
		}
	}
}

// Packs `b` and `ab` hold the same path. Replacing a tag in a finding's text
// must not match inside another pack's tag, whichever order the map yields.
func TestGate_ATagIsNeverASubstringOfAnother(t *testing.T) {
	origins := map[string]origin{
		tagFile("b", "z.json"):  {pack: "b", path: "z.json"},
		tagFile("ab", "z.json"): {pack: "ab", path: "z.json"},
	}
	detail := "declared in " + tagFile("ab", "z.json") + " and " + tagFile("b", "z.json")
	for i := 0; i < 200; i++ {
		if got := untag(detail, origins); got != "declared in z.json and z.json" {
			t.Fatalf("untag = %q on try %d", got, i)
		}
	}
}

// A refusal that has the publisher's own error and another pack's: both are
// reported, and `pack` is set only on the other pack's.
func TestGate_AnOwnErrorAndAnotherPacksAreBothReported(t *testing.T) {
	h := gateHarness(t)
	h.liveAs("town", packFiles("town", map[string][]byte{"town.json": gateZone(t, "town", "gate", rm{id: "gate"})}))
	h.liveAs("acme", packFiles("acme", map[string][]byte{
		"glade.json": gateZone(t, "glade", "x", rm{id: "x", exits: []ex{{dir: "west", zone: "town", room: "gate"}}}),
	}))
	h.liveAs("town", packFiles("town", map[string][]byte{
		"town.json": gateZone(t, "town", "gate", rm{id: "gate", exits: []ex{{dir: "east", zone: "glade", room: "x"}}}),
	}))

	// acme drops glade, which town exits into, and declares Zone `town` itself.
	got := h.refusedWith("acme", packFiles("acme", map[string][]byte{
		"z.json": gateZone(t, "town", "market", rm{id: "market"}),
	}))
	var own, foreign []string
	for _, d := range got {
		if d.GetPack() == "" {
			own = append(own, d.GetCode())
		} else if d.GetPack() == "town" {
			foreign = append(foreign, d.GetCode())
		}
	}
	if !slices.Equal(own, []string{"duplicate_zone"}) || !slices.Equal(foreign, []string{"unknown_zone"}) || len(got) != 2 {
		t.Fatalf("findings = %v: want acme's duplicate_zone and town's unknown_zone, pack set on town's alone", got)
	}
}

// A Room with two Exits in one direction: the build keeps the first and
// reports it, so whether the clash explains an unknown_room is judged on the
// first Exit's target, not on a later duplicate's. Here the first Exit names a
// Room nobody declares and the duplicate names the dropped Zone's own Room.
func TestGate_TheClashIsJudgedOnTheExitTheBuildKept(t *testing.T) {
	h := gateHarness(t)
	h.townLive()
	got := codes(h.refusedWith("acme", packFiles("acme", map[string][]byte{
		"z.json": gateZone(t, "town", "market", rm{id: "market"}),
		"glade.json": gateZone(t, "glade", "g", rm{id: "g", exits: []ex{
			{dir: "north", zone: "town", room: "missing"},
			{dir: "north", zone: "town", room: "market"},
		}}),
	})))
	slices.Sort(got)
	if want := []string{"duplicate_direction", "duplicate_zone", "unknown_room"}; !slices.Equal(got, want) {
		t.Fatalf("findings = %v, want %v: the first Exit's missing target is not explained by the clash", got, want)
	}
}
