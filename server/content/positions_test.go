// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/sim"
)

// AC-6 asks for the line of a bad Direction, and protojson has thrown positions
// away by the time a ZoneDefinition exists. This is the second pass that keeps
// them. The fixture's `norht` is deliberately not on line 1: a walker that
// returned a constant would pass a fixture where the mistake is at the top.
func TestZonePositions_LocatesExitsRoomsAndComponents(t *testing.T) {
	data := []byte(`{
  "formatVersion": 1,
  "id": "town",
  "name": "Town",
  "components": [
    {"type": "andara.core.NoRecall"}
  ],
  "rooms": [
    {
      "id": "plaza",
      "title": "Plaza",
      "exits": [
        {"direction": "north", "toRoom": "hall"},
        {"direction": "norht", "toRoom": "hall"}
      ],
      "components": [
        {"type": "andara.core.Dark"},
        {"type": "andara.core.Drak"}
      ]
    },
    {
      "id": "hall",
      "title": "Hall"
    }
  ]
}`)
	pos := zonePositions(data)
	if pos == nil {
		t.Fatal("no positions recovered")
	}
	if got := pos.ZoneComponents; len(got) != 1 || got[0] != 6 {
		t.Errorf("zone component lines = %v, want [6]", got)
	}
	if len(pos.Rooms) != 2 {
		t.Fatalf("room positions = %v, want two Rooms", pos.Rooms)
	}
	if pos.Rooms[0].Line != 9 {
		t.Errorf("plaza line = %d, want 9", pos.Rooms[0].Line)
	}
	if got := pos.Rooms[0].Exits; len(got) != 2 || got[0] != 13 || got[1] != 14 {
		t.Errorf("plaza exit lines = %v, want [13 14]", got)
	}
	if got := pos.Rooms[0].Components; len(got) != 2 || got[0] != 17 || got[1] != 18 {
		t.Errorf("plaza component lines = %v, want [17 18]", got)
	}
	if pos.Rooms[1].Line != 21 {
		t.Errorf("hall line = %d, want 21", pos.Rooms[1].Line)
	}
	if len(pos.Rooms[1].Exits) != 0 {
		t.Errorf("hall has no exits but recorded %v", pos.Rooms[1].Exits)
	}
}

// Losing a position must never be able to turn valid content into a boot
// failure, so the walk reports what it reached and stops. These inputs are all
// ones protojson rejects on its own with its own message.
func TestZonePositions_NeverFailsTheLoad(t *testing.T) {
	for _, data := range []string{
		``,
		`[]`,
		`"a string"`,
		`{"rooms": {}}`,
		`{"rooms": [{"id": "plaza", "exits": {}}]}`,
		`{"rooms": [{"id": "plaza"`,
		`{"components": 7}`,
	} {
		// The contract is "does not panic and does not report a finding";
		// whatever it returns is a best effort at positions.
		_ = zonePositions([]byte(data))
	}
}

// AC-6 end to end through the adapter: the line in the finding is the line in
// the file, which is the whole reason the position walk exists.
func TestLoadDir_BadDirectionNamesFileAndLine(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "bad-direction"))
	if len(verrs) != 0 {
		t.Fatalf("unexpected parse findings: %v", verrs)
	}
	world, errs := sim.BuildWorld(inputs, sim.Options{})
	if world != nil {
		t.Error("a misspelled Direction must refuse the load")
	}
	var found *sim.ValidationError
	for i := range errs {
		if errs[i].Code == sim.ErrUnknownDirection {
			found = &errs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no unknown_direction finding: %v", errs)
	}
	if !strings.HasSuffix(found.File, filepath.Join("bad-direction", "town.json")) {
		t.Errorf("File = %q, want the fixture path", found.File)
	}
	if found.Line != 11 {
		t.Errorf("Line = %d, want 11 — the line `norht` is on", found.Line)
	}
	if found.Room != "plaza" {
		t.Errorf("Room = %q, want plaza", found.Room)
	}
}

// AC-1 and AC-4 against a real file: Components on a Room resolve, a Zone's
// Component stays on the Zone, and the Component set on a Room that declares
// none is empty.
func TestLoadDir_ComponentsOnRoomsAndZones(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "components"))
	if len(verrs) != 0 {
		t.Fatalf("unexpected parse findings: %v", verrs)
	}
	world, errs := sim.BuildWorld(inputs, sim.Options{})
	if world == nil {
		t.Fatalf("world is nil: %v", errs)
	}
	z := world.Zones[sim.ZoneID("town")]
	if _, ok := z.Component("andara.core.NoRecall"); !ok {
		t.Errorf("Zone Component not resolvable: %v", z.Components)
	}
	cellar := z.Rooms[sim.RoomID("cellar")]
	for _, want := range []sim.ComponentType{
		"andara.core.Dark", "andara.core.Indoors", "andara.core.NoMagic",
	} {
		if _, ok := cellar.Component(want); !ok {
			t.Errorf("%s not resolvable on cellar: %v", want, cellar.Components)
		}
	}
	// AC-4: the Zone's Component did not descend.
	if _, ok := cellar.Component("andara.core.NoRecall"); ok {
		t.Error("a Zone-level Component resolved on a Room")
	}
	if plaza := z.Rooms[sim.RoomID("plaza")]; len(plaza.Components) != 0 {
		t.Errorf("a Room that declares no Components carries %v", plaza.Components)
	}
}

// AC-5, through the loader rather than through hand-built structs: the fixture
// authors cellar's Components out of type order, so a sort that does not happen
// is visible in the bytes.
func TestLoadDir_ComponentsSerializeDeterministically(t *testing.T) {
	load := func() []byte {
		inputs, verrs := LoadDir(fixture(t, "components"))
		if len(verrs) != 0 {
			t.Fatalf("unexpected parse findings: %v", verrs)
		}
		world, errs := sim.BuildWorld(inputs, sim.Options{})
		if world == nil {
			t.Fatalf("world is nil: %v", errs)
		}
		return sim.CanonicalBytes(world)
	}
	first := string(load())
	for i := 0; i < 20; i++ {
		if got := string(load()); got != first {
			t.Fatalf("serialization differs between loads:\n%s\n---\n%s", first, got)
		}
	}
	want := "room_component\ttown\tcellar\tandara.core.Dark\n" +
		"room_component\ttown\tcellar\tandara.core.Indoors\n" +
		"room_component\ttown\tcellar\tandara.core.NoMagic\n"
	if !strings.Contains(first, want) {
		t.Errorf("Components are not sorted by type in the serialization:\n%s", first)
	}
}

// AC-8: the fixtures AW-SRV-001 shipped predate the `components` field, which
// is exactly what makes them the right regression test. They must load
// unchanged, with empty Component sets, and serialize to the bytes they would
// have serialized to before this story.
func TestLoadDir_PreComponentFixturesLoadUnchanged(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "valid"))
	if len(verrs) != 0 {
		t.Fatalf("unexpected parse findings: %v", verrs)
	}
	world, errs := sim.BuildWorld(inputs, sim.Options{})
	if world == nil {
		t.Fatalf("world is nil: %v", errs)
	}
	for zid, z := range world.Zones {
		if len(z.Components) != 0 {
			t.Errorf("Zone %s carries %v; the fixture declares none", zid, z.Components)
		}
		for rid, r := range z.Rooms {
			if len(r.Components) != 0 {
				t.Errorf("Room %s/%s carries %v; the fixture declares none", zid, rid, r.Components)
			}
		}
	}
	if got := string(sim.CanonicalBytes(world)); strings.Contains(got, "component") {
		t.Errorf("component-less content emitted a component record:\n%s", got)
	}
}

// The `components` field is additive (ADR-0007 rule 1), so a file that predates
// it parses without protojson objecting, and a file that carries it parses on a
// binary that understands it. Both fixtures are checked rather than asserted
// about, because "additive" is a claim about the parser, not about the schema
// text.
func TestParseZoneJSON_ComponentsFieldIsAdditive(t *testing.T) {
	for _, name := range []string{"valid", "components"} {
		path := filepath.Join(fixture(t, name), "town.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, verr := parseZoneJSON(path, data); verr != nil {
			t.Errorf("%s/town.json did not parse: %v", name, verr)
		}
	}
}
