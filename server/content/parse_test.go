package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/sim"
)

// AC-8: an invalid Zone Definition fails naming the file and the line number.
//
// The pre-parse into map[string]any only catches encoding/json syntax errors.
// Everything protojson rejects — a scalar of the wrong type, a field that does
// not exist in the schema, a value where a message was expected — gets past it,
// and those are the mistakes a Builder makes more often than an unbalanced
// brace. A finding that says "somewhere in this file" is the thing this story
// exists to prevent.
func TestParseZoneJSON_LineNumberForProtojsonErrors(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		wantLine int
	}{
		// "description": 42 — a uint where a string belongs.
		{name: "wrong scalar type", fixture: "bad-field-type", wantLine: 9},
		// "brightness" is not in RoomDefinition. A Builder typo, or a field from
		// a schema version this binary does not have (ADR-0010's component set
		// lands at room field 5 and must not be silently dropped before then).
		{name: "unknown field", fixture: "unknown-field", wantLine: 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verrs := LoadDir(fixture(t, tc.fixture))
			e := requireCode(t, verrs, sim.ErrMalformed)
			if e.Line != tc.wantLine {
				t.Errorf("Line = %d, want %d (detail: %s)", e.Line, tc.wantLine, e.Detail)
			}
			if filepath.Base(e.File) != "town.json" {
				t.Errorf("File = %q, want .../town.json", e.File)
			}
		})
	}
}

// The line is recovered by matching the position protojson embeds in its error
// text, which is an internal format with no compatibility promise. This test
// pins the shape so that a protojson upgrade which reworded it fails here
// instead of quietly costing every Builder their line numbers.
func TestProtojsonLine(t *testing.T) {
	cases := map[string]int{
		`proto: (line 4:3): unknown field "bogus"`:                          4,
		`proto: (line 1:18): invalid value for uint32 field formatVersion`:  1,
		`proto: syntax error (line 12:7): unexpected token "nope"`:          12,
		`proto: (line 0:1): nonsense`:                                       0,
		`unexpected end of JSON input`:                                      0,
		``:                                                                  0,
		`proto: cannot parse invalid wire-format data`:                      0,
		`proto: (line 1048576:2): a line number wider than a fixture needs`: 1048576,
	}
	for msg, want := range cases {
		if got := protojsonLine(msg); got != want {
			t.Errorf("protojsonLine(%q) = %d, want %d", msg, got, want)
		}
	}
}

// Every syntactic failure mode must name a line, so that "the loader told me
// where" is true of the whole class and not only of the one fixture that
// happens to be in testdata.
func TestParseZoneJSON_EveryMalformedShapeCarriesALine(t *testing.T) {
	cases := map[string]string{
		"truncated":      "{\n  \"formatVersion\": 1,\n  \"id\": \"town\",\n  \"rooms\": [\n",
		"trailing comma": "{\n  \"formatVersion\": 1,\n  \"id\": \"town\",\n}",
		"not an object":  "[]",
		"empty file":     "",
		"wrong nesting":  "{\n  \"formatVersion\": 1,\n  \"id\": \"town\",\n  \"rooms\": \"nope\"\n}",
		"bad scalar":     "{\n  \"formatVersion\": \"one\",\n  \"id\": \"town\"\n}",
		"unknown field":  "{\n  \"formatVersion\": 1,\n  \"id\": \"town\",\n  \"bogus\": true\n}",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "town.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, verrs := LoadDir(dir)
			e := requireCode(t, verrs, sim.ErrMalformed)
			if e.Line < 1 {
				t.Errorf("Line = %d, want >= 1 (detail: %s)", e.Line, e.Detail)
			}
			if e.Detail == "" {
				t.Error("Detail is empty")
			}
		})
	}
}

// AC-10 through the real load path: the determinism that matters is two boots
// off the same directory, not two calls on one in-memory slice. Directory
// iteration order is the filesystem's business, so LoadDir sorting file names
// is part of the invariant.
func TestLoadDir_TwoLoadsOfTheSameDirectorySerializeIdentically(t *testing.T) {
	dir := fixture(t, "valid")
	build := func() string {
		inputs, verrs := LoadDir(dir)
		if len(verrs) != 0 {
			t.Fatalf("load errors: %v", verrs)
		}
		world, errs := sim.BuildWorld(inputs, sim.Options{Source: "dir:" + dir})
		if world == nil {
			t.Fatalf("world is nil: %v", errs)
		}
		return string(sim.CanonicalBytes(world))
	}
	want := build()
	if want == "" {
		t.Fatal("canonical bytes are empty")
	}
	for i := 1; i < 20; i++ {
		if got := build(); got != want {
			t.Fatalf("load %d differs:\nwant:\n%s\ngot:\n%s", i, want, got)
		}
	}
}

// The same content under different file names must produce the same World. File
// paths appear in findings; they must not reach the topology.
func TestLoadDir_FileNamesDoNotReachTheTopology(t *testing.T) {
	src := fixture(t, "valid")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}

	build := func(rename func(string) string) string {
		dir := t.TempDir()
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, rename(e.Name())), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		inputs, verrs := LoadDir(dir)
		if len(verrs) != 0 {
			t.Fatalf("load errors: %v", verrs)
		}
		world, errs := sim.BuildWorld(inputs, sim.Options{})
		if world == nil {
			t.Fatalf("world is nil: %v", errs)
		}
		return string(sim.CanonicalBytes(world))
	}

	asIs := build(func(n string) string { return n })
	// Reverse the file names' sort order without touching their contents or the
	// .json extension LoadDir selects on.
	reordered := build(func(n string) string {
		return reverse(strings.TrimSuffix(n, ".json")) + ".json"
	})
	if asIs != reordered {
		t.Errorf("file names reached the topology:\n%s\n---\n%s", asIs, reordered)
	}
}

// AC-6 at the fixture level: an orphan Room is a warning, the World still loads,
// and the finding names the Room.
func TestLoadDir_OrphanFixtureLoadsWithAWarning(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "orphan"))
	if len(verrs) != 0 {
		t.Fatalf("load errors: %v", verrs)
	}
	world, errs := sim.BuildWorld(inputs, sim.Options{})
	if world == nil {
		t.Fatalf("orphans must not be fatal by default: %v", errs)
	}
	e := requireCode(t, errs, sim.ErrOrphanRoom)
	if e.Room != "attic" {
		t.Errorf("orphan Room = %q, want attic", e.Room)
	}
	if e.Fatal() {
		t.Error("orphan_room reports Fatal() = true")
	}

	// Strict mode turns the same content into a refusal.
	strictWorld, strictErrs := sim.BuildWorld(inputs, sim.Options{StrictOrphans: true})
	if strictWorld != nil {
		t.Error("world should be nil under strict_orphans")
	}
	requireCode(t, strictErrs, sim.ErrOrphanRoom)
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
