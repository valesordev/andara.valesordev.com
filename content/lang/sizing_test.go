// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valesordev/andara/content/lang"
)

// CompileBudget is AC-7: a 2,000-Room pack compiles in under 5 s on the kind
// box. It is a Builder-facing budget rather than a tick budget — the number
// that decides whether `content compile` is something you run on every save or
// something you go and make coffee during.
const CompileBudget = 5 * time.Second

// sizingRooms matches server/simtest.SizingRooms, the World scale AW-SRV-006 is
// measured against. Not imported: simtest is a server-side fixture package and
// content/lang may not reach past server/sim (AC-8). The story's own number is
// 2,000 either way, and a drift between the two is a question for whoever moves
// one of them.
const sizingRooms = 2000

// TestSizingPackCompilesInBudget is AC-7.
//
// The pack is generated rather than checked in: 2,000 Rooms is 100 files of
// content nobody would read, and what the test is about is the shape of the
// work — every Room resolving its Exits against every other Room in its Zone,
// which is where an accidental quadratic would live.
func TestSizingPackCompilesInBudget(t *testing.T) {
	dir := t.TempDir()
	writeSizingPack(t, dir)

	core := compileCore(t)
	start := time.Now()
	out, ds := lang.Compile(dir, core, nil)
	elapsed := time.Since(start)

	if out == nil {
		t.Fatalf("the sizing pack does not compile: %v", ds)
	}
	if n := countRooms(out); n != sizingRooms {
		t.Fatalf("compiled %d Rooms, want %d", n, sizingRooms)
	}
	for _, d := range ds {
		if d.Severity == lang.SeverityError {
			t.Errorf("unexpected finding: %s", d.String())
		}
	}

	limit := CompileBudget * compileFactor
	t.Logf("%d Rooms across %d Zones compiled in %s (budget %s, limit %s at %dx)",
		sizingRooms, sizingZones, elapsed.Round(time.Millisecond), CompileBudget, limit, compileFactor)
	if elapsed > limit {
		t.Fatalf("compiling %d Rooms took %s, past the %s limit: either name resolution has gone quadratic, or work that belongs once per pack has moved inside a per-Room loop",
			sizingRooms, elapsed.Round(time.Millisecond), limit)
	}
}

const sizingZones = 16

// writeSizingPack lays out sizingRooms Rooms across sizingZones Zones, each
// Zone a north-south chain so the Rooms are connected and the orphan warning
// stays quiet — the same shape server/simtest.SizingWorld builds.
func writeSizingPack(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "pack.aw"), "pack p requires andara.core@1\n")

	per := sizingRooms / sizingZones
	for z := 0; z < sizingZones; z++ {
		var sb strings.Builder
		fmt.Fprintf(&sb, "zone z%02d \"Zone %d\" {\n", z, z)
		for r := 0; r < per; r++ {
			fmt.Fprintf(&sb, "  room r%04d \"Room %d\" {\n", r, r)
			fmt.Fprintf(&sb, "    desc \"A room in zone %d.\"\n", z)
			if r > 0 {
				fmt.Fprintf(&sb, "    exit north -> r%04d\n", r-1)
			}
			if r < per-1 {
				fmt.Fprintf(&sb, "    exit south -> r%04d\n", r+1)
			}
			sb.WriteString("  }\n")
			if r < per-1 {
				sb.WriteString("\n")
			}
		}
		sb.WriteString("}\n")
		write(t, filepath.Join(dir, fmt.Sprintf("z%02d.aw", z)), sb.String())
	}
}

func countRooms(o *lang.Output) int {
	n := 0
	for _, z := range o.Zones {
		n += len(z.GetRooms())
	}
	return n
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
