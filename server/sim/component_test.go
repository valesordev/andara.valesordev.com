// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

func comp(t string, fields ...*contentv1.ComponentField) *contentv1.ComponentValue {
	return &contentv1.ComponentValue{Type: t, Fields: fields}
}

// zoneWith is zone() plus a Zone-level Component set, which zone() has no
// reason to grow a parameter for.
func zoneWith(file, id, name string, comps []*contentv1.ComponentValue, rooms ...*contentv1.RoomDefinition) Input {
	in := zone(file, id, name, rooms...)
	in.Def.Components = comps
	return in
}

// AC-1: a Room that declares a Component resolves, carrying exactly that one.
func TestBuildWorld_RoomComponentResolves(t *testing.T) {
	rd := room("cellar", "Cellar")
	rd.Components = []*contentv1.ComponentValue{comp("andara.core.Dark")}

	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town",
			room("plaza", "Plaza", exit("down", "", "cellar")),
			rd,
		),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("unexpected errors: %v", errs)
	}
	cellar, ok := world.Resolve(RoomRef{Zone: "town", Room: "cellar"})
	if !ok {
		t.Fatal("cellar did not resolve")
	}
	if len(cellar.Components) != 1 {
		t.Fatalf("component set = %v, want exactly one", cellar.Components)
	}
	if _, ok := cellar.Component("andara.core.Dark"); !ok {
		t.Errorf("Dark is not resolvable on the Room: %v", cellar.Components)
	}
	if _, ok := cellar.Component("andara.core.NoMagic"); ok {
		t.Error("a Component the Room never declared resolves on it")
	}
}

// AC-2: an unregistered Component type is fatal, and the message has to tell a
// Builder that spelling is not the problem — Component types are server-defined
// (ADR-0010 decision 7), so the fix is an issue, not another guess.
func TestBuildWorld_UnknownComponentTypeIsFatal(t *testing.T) {
	rd := room("plaza", "Plaza")
	rd.Components = []*contentv1.ComponentValue{comp("andara.core.Drak")}

	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town", rd),
	}, Options{})
	if world != nil {
		t.Error("an unknown Component type must refuse the load")
	}
	e := requireCode(t, errs, ErrUnknownComponent)
	if e.File != "town.json" {
		t.Errorf("File = %q, want town.json", e.File)
	}
	if e.Room != "plaza" {
		t.Errorf("Room = %q, want plaza", e.Room)
	}
	for _, want := range []string{"andara.core.Drak", "defined on the server", "ADR-0010 decision 7"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("detail does not say %q:\n%s", want, e.Detail)
		}
	}
}

// AC-3: ADR-0010 decision 3 keys Components by type, so two of one type has no
// override semantics and every answer to "which wins" would be silent.
func TestBuildWorld_DuplicateComponentTypeIsFatal(t *testing.T) {
	rd := room("plaza", "Plaza")
	rd.Components = []*contentv1.ComponentValue{
		comp("andara.core.Dark"),
		comp("andara.core.Dark"),
	}

	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town", rd),
	}, Options{})
	if world != nil {
		t.Error("a duplicated Component type must refuse the load")
	}
	e := requireCode(t, errs, ErrDuplicateComponent)
	if !strings.Contains(e.Detail, "andara.core.Dark") {
		t.Errorf("detail does not name the type:\n%s", e.Detail)
	}
	if e.Room != "plaza" {
		t.Errorf("Room = %q, want plaza", e.Room)
	}
}

// AC-4: a Zone-level Component is the Zone's own. It does not descend onto the
// Zone's Rooms. This test pins the non-merging behavior precisely so that a
// later decision to merge is a visible change to a test, not a silent change to
// what content means.
func TestBuildWorld_ZoneComponentsDoNotDescendToRooms(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zoneWith("town.json", "town", "Town",
			[]*contentv1.ComponentValue{comp("andara.core.NoRecall")},
			room("plaza", "Plaza", exit("north", "", "hall")),
			room("hall", "Hall", exit("south", "", "plaza")),
		),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("unexpected errors: %v", errs)
	}
	z := world.Zones[ZoneID("town")]
	if _, ok := z.Component("andara.core.NoRecall"); !ok {
		t.Errorf("NoRecall is not resolvable on the Zone: %v", z.Components)
	}
	for _, rid := range []RoomID{"plaza", "hall"} {
		r := z.Rooms[rid]
		if len(r.Components) != 0 {
			t.Errorf("Room %s carries %v; Zone Components do not descend", rid, r.Components)
		}
		if _, ok := r.Component("andara.core.NoRecall"); ok {
			t.Errorf("Room %s resolved a Zone-level Component", rid)
		}
	}
}

// AC-5: Components are sorted by type in the World, so the serialization a
// State Hash would be taken over cannot depend on authoring order. The set here
// is authored deliberately out of order.
func TestBuildWorld_ComponentsSortedByType(t *testing.T) {
	rd := room("cellar", "Cellar")
	rd.Components = []*contentv1.ComponentValue{
		comp("andara.core.NoMagic"),
		comp("andara.core.Dark"),
		comp("andara.core.Indoors"),
	}
	world, errs := BuildWorld([]Input{
		zone("town.json", "town", "Town", rd),
	}, Options{})
	if fatal(errs) {
		t.Fatalf("unexpected errors: %v", errs)
	}
	got := make([]string, 0, 3)
	for _, c := range world.Zones["town"].Rooms["cellar"].Components {
		got = append(got, string(c.Type))
	}
	want := []string{"andara.core.Dark", "andara.core.Indoors", "andara.core.NoMagic"}
	if !slices.Equal(got, want) {
		t.Errorf("component order = %v, want %v", got, want)
	}
}

// AC-8: content authored before Components existed loads unchanged, with an
// empty Component set and the same canonical bytes it had before. The second
// half is the part that matters: a Room with no Components must serialize
// exactly as it did, or every State Hash taken before this story is invalidated
// by a field nobody used.
func TestCanonicalBytes_ComponentlessContentIsUnchanged(t *testing.T) {
	mk := func() []Input {
		return []Input{
			zone("town.json", "town", "Town",
				room("plaza", "Plaza", exit("north", "", "hall")),
				room("hall", "Hall", exit("south", "", "plaza")),
			),
		}
	}
	world, errs := BuildWorld(mk(), Options{})
	if fatal(errs) {
		t.Fatalf("unexpected errors: %v", errs)
	}
	got := string(CanonicalBytes(world))
	want := "zone\ttown\tTown\t" + strconv.Itoa(int(PartitionFor("town"))) + "\n" +
		"zone_fallback\ttown\tplaza\n" +
		"room\ttown\thall\tHall\td\n" +
		"exit\ttown\thall\tsouth\ttown\tplaza\tlocal\n" +
		"room\ttown\tplaza\tPlaza\td\n" +
		"exit\ttown\tplaza\tnorth\ttown\thall\tlocal\n"
	if got != want {
		t.Errorf("component-less serialization changed:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// A Component's fields are part of what is hashed, so the encoding has to stay
// injective across the oneof: the string "1" and the integer 1 are different
// content and must not produce the same bytes.
func TestCanonicalBytes_FieldKindIsInTheEncoding(t *testing.T) {
	mk := func(f ComponentField) *World {
		return &World{Zones: map[ZoneID]*Zone{
			"town": {ID: "town", Name: "Town", Rooms: map[RoomID]*Room{
				"plaza": {ID: "plaza", Title: "Plaza", Components: []Component{
					{Type: "andara.core.Dark", Fields: []ComponentField{f}},
				}},
			}},
		}}
	}
	asString := CanonicalBytes(mk(ComponentField{Name: "n", Kind: FieldString, Str: "1"}))
	asInt := CanonicalBytes(mk(ComponentField{Name: "n", Kind: FieldInt, Int: 1}))
	if string(asString) == string(asInt) {
		t.Errorf("a string field and an int field serialize identically:\n%s", asString)
	}
}

// A World assembled by hand has not been through the loader, so the serializer
// sorts rather than trusting its input. A serializer that quietly depends on
// sorted input is a determinism bug waiting for its first caller.
func TestCanonicalBytes_SortsUnsortedComponentSets(t *testing.T) {
	mk := func(comps []Component) *World {
		return &World{Zones: map[ZoneID]*Zone{
			"town": {ID: "town", Name: "Town", Rooms: map[RoomID]*Room{
				"plaza": {ID: "plaza", Title: "Plaza", Components: comps},
			}},
		}}
	}
	a := CanonicalBytes(mk([]Component{
		{Type: "andara.core.Dark"},
		{Type: "andara.core.NoMagic"},
	}))
	b := CanonicalBytes(mk([]Component{
		{Type: "andara.core.NoMagic"},
		{Type: "andara.core.Dark"},
	}))
	if string(a) != string(b) {
		t.Errorf("authoring order leaked into the serialization:\n%s\n---\n%s", a, b)
	}
}

// An undeclared field is rejected rather than carried. Not an AC, but the four
// seeded Component types are markers, so without this every one of them accepts
// arbitrary named values that no system reads and the State Hash still covers.
func TestBuildWorld_UndeclaredComponentFieldIsFatal(t *testing.T) {
	rd := room("plaza", "Plaza")
	rd.Components = []*contentv1.ComponentValue{
		comp("andara.core.Dark", &contentv1.ComponentField{
			Name:  "level",
			Value: &contentv1.ComponentField_IntValue{IntValue: 3},
		}),
	}
	world, errs := BuildWorld([]Input{zone("town.json", "town", "Town", rd)}, Options{})
	if world != nil {
		t.Error("a field the registry does not declare must refuse the load")
	}
	e := requireCode(t, errs, ErrInvalidComponentField)
	if !strings.Contains(e.Detail, "level") {
		t.Errorf("detail does not name the field:\n%s", e.Detail)
	}
}

// An empty type is a malformed Component, not an unknown one: there is no type
// to tell the Builder to file an issue about.
func TestBuildWorld_EmptyComponentTypeIsMalformed(t *testing.T) {
	rd := room("plaza", "Plaza")
	rd.Components = []*contentv1.ComponentValue{comp("")}
	_, errs := BuildWorld([]Input{zone("town.json", "town", "Town", rd)}, Options{})
	requireCode(t, errs, ErrMalformed)
	if hasCode(errs, ErrUnknownComponent) {
		t.Error("an empty type reported as an unknown type sends a Builder to file an issue about nothing")
	}
}

// A Zone-level Component gets the same registry treatment as a Room's, and the
// finding names the Zone rather than leaving a Builder to guess which level of
// the file is wrong.
func TestBuildWorld_UnknownZoneComponentIsFatal(t *testing.T) {
	world, errs := BuildWorld([]Input{
		zoneWith("town.json", "town", "Town",
			[]*contentv1.ComponentValue{comp("andara.core.Weather")},
			room("plaza", "Plaza"),
		),
	}, Options{})
	if world != nil {
		t.Error("an unknown Zone Component must refuse the load")
	}
	e := requireCode(t, errs, ErrUnknownComponent)
	if e.Room != "" {
		t.Errorf("Room = %q on a Zone-level finding, want empty", e.Room)
	}
	if !strings.Contains(e.Detail, "Zone town") {
		t.Errorf("detail does not name the Zone:\n%s", e.Detail)
	}
}

func TestComponentRegistry_SeedVocabulary(t *testing.T) {
	want := []ComponentType{
		"andara.core.Behavior", // AW-SRV-022
		"andara.core.Dark",
		"andara.core.Indoors",
		"andara.core.Memory", // AW-SRV-022
		"andara.core.NoMagic",
		"andara.core.NoRecall",
	}
	if got := ComponentTypes(); !slices.Equal(got, want) {
		t.Errorf("registry = %v, want %v", got, want)
	}
	for _, ct := range want {
		if !KnownComponentType(ct) {
			t.Errorf("%s is listed but not known", ct)
		}
	}
	if KnownComponentType("pets.Aggro") {
		t.Error("a Builder-namespaced type is known; Builders do not define types (ADR-0010 decision 7)")
	}
}
