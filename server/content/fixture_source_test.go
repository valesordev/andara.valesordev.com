// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/valesordev/andara/content/lang"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// TestDevFixtureSourceMatchesTestContent is AW-SRV-037 AC-5 and AC-6: the dev
// fixture's Content Language source, content/fixtures/town/, compiles against
// the shipped andara.core seed to exactly the Zones and town.* Templates the
// stack, the kind cluster and the boot tests load from testdata/content/valid/.
// `make content-seed` publishes the source (AW-INF-021) and everything else
// loads the JSON, so a drift here would mean dev serves a World no test ran.
func TestDevFixtureSourceMatchesTestContent(t *testing.T) {
	seed, errs := LoadTemplatesDir(filepath.Join("..", "..", "content", "core"))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	core := &lang.Pack{Name: lang.CorePack, Version: 1}
	for _, in := range seed {
		core.Templates = append(core.Templates, in.Def)
	}

	out, ds := lang.Compile(filepath.Join("..", "..", "content", "fixtures", "town"), core, nil)
	if out == nil || lang.HasError(ds) {
		t.Fatalf("content/fixtures/town does not compile: %v", ds)
	}

	valid := fixture(t, "valid")
	var zones, templates int
	for _, b := range out.Blobs {
		if b.MediaType != lang.BlobMediaType {
			continue
		}
		if strings.HasPrefix(b.Path, TemplatesSubdir+"/") {
			templates++
		} else {
			zones++
		}
		path := filepath.Join(valid, filepath.FromSlash(b.Path))
		want, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("content/fixtures/town compiles %s, which testdata/content/valid lacks: %v", b.Path, err)
			continue
		}
		if !bytes.Equal(b.Bytes, want) {
			t.Errorf("%s differs from what content/fixtures/town compiles it to:\n--- compiled ---\n%s\n--- %s ---\n%s", path, b.Bytes, path, want)
		}
	}
	if zones != 4 || templates != 3 {
		t.Errorf("compiled %d Zones and %d Templates, want 4 and 3", zones, templates)
	}
}

// AW-SRV-037 AC-3: over the World the valid fixture's files describe, a
// Character in purgatory/start walks out into town/plaza, and a bystander
// there reads the arrival. from_direction is whatever the move rule gives an
// Exit with no reverse; this story doesn't assert or change it.
func TestPurgatoryWalksOutToThePlaza(t *testing.T) {
	valid := fixture(t, "valid")
	zones, errs := LoadDir(valid)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	world, verrs := sim.BuildWorld(zones, sim.Options{Source: "dir:" + valid})
	if hasFatal(verrs) {
		t.Fatal(verrs)
	}
	tins, errs := LoadTemplatesDir(valid)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	reg, verrs := sim.BuildTemplates(tins, sim.TemplateOptions{})
	if hasFatal(verrs) {
		t.Fatal(verrs)
	}
	e := sim.NewEngine(world, reg, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()})
	simtest.Place(e, "hero", "purgatory", "start")
	simtest.Place(e, "bob", "town", "plaza")

	step := func(cmd *logv1.LoggedCommand) sim.StepResult {
		t.Helper()
		p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
		res, err := e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: e.State().Offsets[p], Command: cmd}}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := step(simtest.Move("purgatory", "hero", "out"))
	if len(res.Outbound) != 1 || res.Outbound[0].GetZoneId() != "town" || res.Outbound[0].GetArrive().GetRoomId() != "plaza" {
		t.Fatalf("out from purgatory/start: outbound %v, events %v", res.Outbound, res.Events)
	}
	res = step(res.Outbound[0])
	if got := e.State().Zones["town"].Entities["hero"]; got == nil || got.Room != "plaza" {
		t.Fatalf("hero in town = %+v", got)
	}
	// The arrival is Room-scoped, so it reaches every occupant of the plaza,
	// bob among them; and bob's own look names the newcomer.
	var seen bool
	for _, ev := range res.Events {
		a := ev.Envelope.GetCharacterArrived()
		if ev.Type == sim.EvCharacterArrived && a.GetCharacterName() == "hero" && a.GetZoneId() == "town" && a.GetRoomId() == "plaza" {
			seen = ev.Scope.Room == (sim.RoomRef{Zone: "town", Room: "plaza"})
		}
	}
	if !seen {
		t.Fatalf("no arrival scoped to the plaza: %v", res.Events)
	}
	look := step(simtest.Look("town", "bob")).Events
	if len(look) != 1 || !slices.Contains(look[0].Envelope.GetRoomDescribed().GetOccupants(), "hero") {
		t.Fatalf("bob's look in the plaza: %v", look)
	}
}
