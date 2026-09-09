package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/sim"
)

func TestLoadDir_ValidThreeZones(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "valid"))
	if len(verrs) != 0 {
		t.Fatalf("load errors: %v", verrs)
	}
	if len(inputs) != 3 {
		t.Fatalf("inputs = %d, want 3", len(inputs))
	}
	world, errs := sim.BuildWorld(inputs, sim.Options{Source: "dir:" + fixture(t, "valid")})
	if hasFatal(errs) {
		t.Fatalf("build errors: %v", errs)
	}
	plaza, ok := world.Resolve(sim.RoomRef{Zone: "town", Room: "plaza"})
	if !ok {
		t.Fatal("plaza missing")
	}
	var cross bool
	for _, e := range plaza.Exits {
		if e.Direction == "east" {
			cross = e.CrossZone
			if e.To != (sim.RoomRef{Zone: "wilds", Room: "trail"}) {
				t.Errorf("east = %+v", e.To)
			}
		}
	}
	if !cross {
		t.Error("east exit should be CrossZone")
	}
}

func TestLoadDir_DanglingExit(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "dangling"))
	if len(verrs) != 0 {
		t.Fatalf("load errors: %v", verrs)
	}
	_, errs := sim.BuildWorld(inputs, sim.Options{})
	e := requireCode(t, errs, sim.ErrUnknownRoom)
	if !strings.Contains(e.File, "town.json") {
		t.Errorf("File = %q, want town.json", e.File)
	}
}

func TestLoadDir_DuplicateRoom(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "duplicate-room"))
	if len(verrs) != 0 {
		t.Fatalf("load errors: %v", verrs)
	}
	_, errs := sim.BuildWorld(inputs, sim.Options{})
	e := requireCode(t, errs, sim.ErrDuplicateRoom)
	if !strings.Contains(e.Detail, "a.json") || !strings.Contains(e.Detail, "b.json") {
		t.Errorf("Detail %q missing both paths", e.Detail)
	}
}

func TestLoadDir_MissingZone(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "missing-zone"))
	if len(verrs) != 0 {
		t.Fatalf("load errors: %v", verrs)
	}
	_, errs := sim.BuildWorld(inputs, sim.Options{})
	e := requireCode(t, errs, sim.ErrUnknownZone)
	if !strings.Contains(e.Detail, "wilds") {
		t.Errorf("Detail %q does not name missing ZoneID", e.Detail)
	}
}

func TestLoadDir_MalformedJSONLineNumber(t *testing.T) {
	_, verrs := LoadDir(fixture(t, "malformed"))
	e := requireCode(t, verrs, sim.ErrMalformed)
	if e.Line == 0 {
		t.Fatal("Line is 0; syntax errors must name a line number")
	}
	if !strings.Contains(e.File, "town.json") {
		t.Errorf("File = %q", e.File)
	}
}

func TestLoadDir_UnsupportedVersion(t *testing.T) {
	inputs, verrs := LoadDir(fixture(t, "unsupported-version"))
	if len(verrs) != 0 {
		t.Fatalf("load errors: %v", verrs)
	}
	_, errs := sim.BuildWorld(inputs, sim.Options{})
	requireCode(t, errs, sim.ErrUnsupportedVersion)
}

func TestLoadDir_Empty(t *testing.T) {
	dir := t.TempDir()
	_, verrs := LoadDir(dir)
	e := requireCode(t, verrs, sim.ErrEmptyContent)
	if !strings.Contains(e.Detail, dir) {
		t.Errorf("Detail %q does not name the content source path", e.Detail)
	}
}

func TestLoadDir_MissingPath(t *testing.T) {
	_, verrs := LoadDir(filepath.Join(t.TempDir(), "nope"))
	if !hasCode(verrs, sim.ErrEmptyContent) && !hasCode(verrs, sim.ErrMalformed) {
		t.Fatalf("want empty or malformed, got %v", verrs)
	}
}

func TestLoadDir_FortyRooms(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(`{"formatVersion":1,"id":"town","name":"Town","rooms":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		id := "r" + itoa2(i)
		b.WriteString(`{"id":"` + id + `","title":"Room ` + id + `","description":"d"`)
		if i < 39 {
			next := "r" + itoa2(i+1)
			b.WriteString(`,"exits":[{"direction":"east","toRoom":"` + next + `"}]`)
		}
		b.WriteByte('}')
	}
	b.WriteString(`]}`)
	path := filepath.Join(dir, "town.json")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, verrs := LoadDir(dir)
	if len(verrs) != 0 {
		t.Fatalf("load: %v", verrs)
	}
	world, errs := sim.BuildWorld(inputs, sim.Options{})
	if hasFatal(errs) {
		t.Fatalf("build: %v", errs)
	}
	if len(world.Zones["town"].Rooms) != 40 {
		t.Fatalf("rooms = %d", len(world.Zones["town"].Rooms))
	}
}

func TestLoadDir_IgnoresNonJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, verrs := LoadDir(dir)
	requireCode(t, verrs, sim.ErrEmptyContent)
}

func TestLoad_KafkaNotImplemented(t *testing.T) {
	_, verrs := Load(SourceKafka, "")
	e := requireCode(t, verrs, sim.ErrEmptyContent)
	if !strings.Contains(e.Detail, "kafka") {
		t.Errorf("Detail %q should name kafka", e.Detail)
	}
	if !strings.Contains(e.Detail, "AW-SRV-012") {
		t.Errorf("Detail %q should name AW-SRV-012", e.Detail)
	}
}

func TestLoad_UnknownSource(t *testing.T) {
	_, verrs := Load("s3", "/x")
	requireCode(t, verrs, sim.ErrMalformed)
}

func TestLoadDir_DeterministicFileOrder(t *testing.T) {
	a, _ := LoadDir(fixture(t, "valid"))
	b, _ := LoadDir(fixture(t, "valid"))
	if len(a) != len(b) {
		t.Fatal("length mismatch")
	}
	for i := range a {
		if a[i].File != b[i].File {
			t.Fatalf("file order %s vs %s", a[i].File, b[i].File)
		}
		if a[i].Def.GetId() != b[i].Def.GetId() {
			t.Fatalf("id order %s vs %s", a[i].Def.GetId(), b[i].Def.GetId())
		}
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "content", name)
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func itoa2(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func hasFatal(errs []sim.ValidationError) bool {
	for _, e := range errs {
		if e.Fatal() {
			return true
		}
	}
	return false
}

func hasCode(errs []sim.ValidationError, code sim.ErrCode) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

func requireCode(t *testing.T, errs []sim.ValidationError, code sim.ErrCode) sim.ValidationError {
	t.Helper()
	for _, e := range errs {
		if e.Code == code {
			return e
		}
	}
	t.Fatalf("missing %s in %v", code, errs)
	return sim.ValidationError{}
}
