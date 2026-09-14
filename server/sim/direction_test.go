// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"strings"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// The canonical set and its reverses, transcribed from docs/glossary.md rather
// than from direction.go. Two copies is the point: the glossary is the
// authority, and a test written from the implementation proves only that the
// implementation is itself.
var glossaryDirections = map[Direction]Direction{
	"north":     "south",
	"south":     "north",
	"east":      "west",
	"west":      "east",
	"northeast": "southwest",
	"southwest": "northeast",
	"northwest": "southeast",
	"southeast": "northwest",
	"up":        "down",
	"down":      "up",
	"in":        "out",
	"out":       "in",
}

func TestDirection_ClosedSetMatchesGlossary(t *testing.T) {
	if len(Directions()) != 12 {
		t.Fatalf("canonical set has %d Directions, want the glossary's twelve", len(Directions()))
	}
	for d, want := range glossaryDirections {
		if !d.Valid() {
			t.Errorf("%s is in the glossary and not in the canonical set", d)
			continue
		}
		got, ok := d.Reverse()
		if !ok {
			t.Errorf("%s has no reverse", d)
			continue
		}
		if got != want {
			t.Errorf("reverse of %s = %s, want %s", d, got, want)
		}
	}
	for _, d := range Directions() {
		if _, ok := glossaryDirections[d]; !ok {
			t.Errorf("%s is in the canonical set and not in the glossary", d)
		}
	}
}

// AC-6's rejections, at the unit level. Case is significant: folding it would
// make content that round-trips through a case-insensitive tool hash
// differently from the file the Builder wrote.
func TestDirection_RejectsOutsideTheSet(t *testing.T) {
	for _, d := range []Direction{"norht", "North", "NORTH", "", " north", "north ", "n", "ne", "u", "forward"} {
		if d.Valid() {
			t.Errorf("%q is accepted as a Direction", d)
		}
		if _, ok := d.Reverse(); ok {
			t.Errorf("%q has a reverse but is not a Direction", d)
		}
	}
}

// Directions returns a copy, so a caller cannot reorder the vocabulary that the
// rejection messages quote.
func TestDirections_ReturnsACopy(t *testing.T) {
	got := Directions()
	got[0] = "tampered"
	if Directions()[0] == "tampered" {
		t.Error("Directions() hands out the package's own slice")
	}
}

func TestDirectionsList_NamesAllTwelve(t *testing.T) {
	list := DirectionsList()
	for d := range glossaryDirections {
		if !strings.Contains(list, string(d)) {
			t.Errorf("the permitted-set message omits %s: %s", d, list)
		}
	}
}

// AC-6: a misspelled Direction refuses the load, naming the file, the Room, the
// offending label, and the twelve that are permitted. Without the list a
// Builder who has just typed "norht" has to go and find the glossary.
func TestBuildWorld_UnknownDirectionIsFatal(t *testing.T) {
	in := zone("town.json", "town", "Town",
		room("plaza", "Plaza", exit("norht", "", "hall")),
		room("hall", "Hall", exit("south", "", "plaza")),
	)
	in.Pos = &Positions{Rooms: []RoomPositions{{Line: 6, Exits: []int{11}}}}

	world, errs := BuildWorld([]Input{in}, Options{})
	if world != nil {
		t.Error("a Direction outside the closed set must refuse the load")
	}
	e := requireCode(t, errs, ErrUnknownDirection)
	if e.File != "town.json" {
		t.Errorf("File = %q, want town.json", e.File)
	}
	if e.Line != 11 {
		t.Errorf("Line = %d, want 11", e.Line)
	}
	if e.Room != "plaza" {
		t.Errorf("Room = %q, want plaza", e.Room)
	}
	if !strings.Contains(e.Detail, "norht") {
		t.Errorf("detail does not quote the offending label:\n%s", e.Detail)
	}
	for d := range glossaryDirections {
		if !strings.Contains(e.Detail, string(d)) {
			t.Errorf("detail does not list %s:\n%s", d, e.Detail)
		}
	}
}

// An empty Direction stays malformed_file rather than becoming
// unknown_direction: there is no label to quote back, and AW-SRV-001 already
// classified it.
func TestBuildWorld_EmptyDirectionStaysMalformed(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town", room("plaza", "Plaza", exit("", "", "hall"))),
	}, Options{})
	requireCode(t, errs, ErrMalformed)
	if hasCode(errs, ErrUnknownDirection) {
		t.Error("an empty Direction reported as an unknown one quotes nothing back")
	}
}

// AC-7: a one-way Exit is legal — a chute, a trapdoor — and warned about,
// because the far more common cause is a Builder forgetting the way back. The
// warning names both Rooms and the Direction that is missing.
func TestBuildWorld_MissingReverseExitWarns(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("down", "", "oubliette"), exit("north", "", "hall")),
			room("hall", "Hall", exit("south", "", "plaza")),
			room("oubliette", "Oubliette"),
		),
	}, Options{})
	if world == nil {
		t.Fatalf("a one-way Exit is legal and must not refuse the load: %v", errs)
	}
	if fatal(errs) {
		t.Fatalf("missing_reverse_exit must be advisory: %v", errs)
	}
	found := codes(errs, ErrMissingReverseExit)
	if len(found) != 1 {
		t.Fatalf("warnings = %v, want exactly the plaza→oubliette one", found)
	}
	e := found[0]
	if e.Room != "plaza" {
		t.Errorf("Room = %q, want the Room the Exit leaves from", e.Room)
	}
	for _, want := range []string{"plaza", "oubliette", "up"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("detail does not name %q:\n%s", want, e.Detail)
		}
	}
	if e.Fatal() {
		t.Error("missing_reverse_exit reports Fatal() = true")
	}
}

// A reciprocal pair warns about neither side. The reverse has to point back at
// the originating Room, not merely exist: two Rooms that both go north are not
// a two-way passage.
func TestBuildWorld_ReciprocalExitsDoNotWarn(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("north", "", "hall")),
			room("hall", "Hall", exit("south", "", "plaza")),
		),
	}, Options{})
	if got := codes(errs, ErrMissingReverseExit); len(got) != 0 {
		t.Errorf("a reciprocal pair warned: %v", got)
	}

	_, errs = BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("north", "", "hall")),
			room("hall", "Hall", exit("south", "", "gate")),
			room("gate", "Gate", exit("north", "", "hall")),
		),
	}, Options{})
	if got := codes(errs, ErrMissingReverseExit); len(got) == 0 {
		t.Error("a south Exit that lands somewhere else counted as the way back")
	}
}

// Cross-Zone Exits have reverses too: the reverse is looked up in whatever Zone
// the Exit lands in, or a Zone boundary becomes a place warnings stop.
func TestBuildWorld_ReverseExitCrossesZones(t *testing.T) {
	mk := func(back *contentv1.ExitDefinition) []Input {
		wilds := room("trail", "Trail")
		if back != nil {
			wilds.Exits = []*contentv1.ExitDefinition{back}
		}
		return []Input{
			zone("town.json", "town", "Town",
				room("plaza", "Plaza", exit("east", "wilds", "trail")),
			),
			zone("wilds.json", "wilds", "Wilds", wilds),
		}
	}
	_, errs := BuildWorld(mk(exit("west", "town", "plaza")), Options{})
	if got := codes(errs, ErrMissingReverseExit); len(got) != 0 {
		t.Errorf("a reciprocal cross-Zone pair warned: %v", got)
	}
	_, errs = BuildWorld(mk(nil), Options{})
	if got := codes(errs, ErrMissingReverseExit); len(got) != 1 {
		t.Errorf("a one-way cross-Zone Exit produced %v, want one warning", got)
	}
}

// An Exit whose target does not resolve is already an error; warning that the
// Room it never reaches has no way back would be noise on top of the real
// finding.
func TestBuildWorld_UnresolvedExitDoesNotAlsoWarnAboutItsReverse(t *testing.T) {
	_, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("north", "", "nowhere")),
		),
	}, Options{})
	requireCode(t, errs, ErrUnknownRoom)
	if got := codes(errs, ErrMissingReverseExit); len(got) != 0 {
		t.Errorf("an unresolved Exit also warned about its reverse: %v", got)
	}
}
