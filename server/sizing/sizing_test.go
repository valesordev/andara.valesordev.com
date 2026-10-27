// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sizing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

func jsonLog() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewJSONHandler(&b, nil)), &b
}

// lines is every JSON log line whose msg is msg.
func lines(t *testing.T, b *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for l := range strings.SplitSeq(strings.TrimSpace(b.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// AC-6: anything but ANDARA_ENV=local, sim.source=memory and
// snapshot.interval=0 is refused, with one error line naming the key, its
// value and the value required.
func TestGuard(t *testing.T) {
	ok := func() config.Config {
		return config.Config{Environment: "local", SimSource: "memory"}
	}
	for _, c := range []struct {
		name               string
		edit               func(*config.Config)
		key, value, wanted string
	}{
		{"local, memory, no snapshots", func(*config.Config) {}, "", "", ""},
		{"dev", func(c *config.Config) { c.Environment = "dev" }, "ANDARA_ENV", "dev", "local"},
		{"kafka", func(c *config.Config) { c.SimSource = "kafka" }, "sim.source", "kafka", "memory"},
		{"snapshots", func(c *config.Config) { c.SnapshotInterval = time.Minute }, "snapshot.interval", "1m0s", "0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := ok()
			c.edit(&cfg)
			log, b := jsonLog()
			err := Guard(&cfg, log)
			refused := lines(t, b, "sizing build: refusing to boot")
			if c.key == "" {
				if err != nil || len(refused) != 0 {
					t.Fatalf("refused: %v %v", err, refused)
				}
				return
			}
			var g *GuardError
			if !errors.As(err, &g) || g.Key != c.key {
				t.Fatalf("err = %v, want a GuardError naming %s", err, c.key)
			}
			if len(refused) != 1 {
				t.Fatalf("%d refusal lines, want 1: %s", len(refused), b)
			}
			l := refused[0]
			if l["level"] != "ERROR" || l["key"] != c.key || l["value"] != c.value || l["required"] != c.wanted {
				t.Errorf("refusal line %v, want key=%s value=%s required=%s", l, c.key, c.value, c.wanted)
			}
		})
	}
}

// The production defaults are refused: a sizing binary started with no
// settings never reaches a broker.
func TestGuardRefusesTheDefaults(t *testing.T) {
	cfg := config.Config{Environment: config.DefaultEnvironment, SimSource: config.DefaultSimSource, SnapshotInterval: config.DefaultSnapshotInterval}
	log, _ := jsonLog()
	if err := Guard(&cfg, log); err == nil {
		t.Fatal("the defaults passed the guard")
	}
}

func sizingEngine(t *testing.T, reg *sim.TemplateRegistry) *sim.Engine {
	t.Helper()
	w, err := simtest.SizingWorld()
	if err != nil {
		t.Fatal(err)
	}
	return sim.NewEngine(w, reg, sim.Config{Seed: 7, Partitions: simtest.AllPartitions(), Handlers: sim.Handlers()})
}

// runLoop calls the seeder's BeforeTick and steps e, as the tick loop does,
// until stop closes.
func runLoop(t *testing.T, s *Seeder, e *sim.Engine, stop <-chan struct{}) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.BeforeTick(context.Background(), time.Time{})
			if _, err := e.Step(sim.TickInput{}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	return done
}

// AC-1 and AC-4: the seed places SizingEntities over the World in effect,
// counted per Zone and Room, and says so in one warn.
func TestSeedPlacesTheFixture(t *testing.T) {
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	e := sizingEngine(t, reg)
	log, b := jsonLog()
	s := NewSeeder(func() *sim.Engine { return e }, "/content", log)
	stop := make(chan struct{})
	done := runLoop(t, s, e, stop)
	err = s.Seed(context.Background(), done)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	entities, characters, rooms := 0, 0, map[sim.RoomID]bool{}
	for _, z := range e.State().Zones {
		for _, ent := range z.Entities {
			entities++
			if ent.Name != "" {
				characters++
			}
			rooms[sim.RoomID(string(z.ID)+"/"+string(ent.Room))] = true
		}
	}
	if entities != simtest.SizingEntities || characters != simtest.SizingCharacters || len(rooms) != simtest.SizingRooms {
		t.Errorf("seeded %d Entities (%d Characters) over %d Rooms, want %d (%d) over %d",
			entities, characters, len(rooms), simtest.SizingEntities, simtest.SizingCharacters, simtest.SizingRooms)
	}
	warn := lines(t, b, "sizing build: seeded World, not for serving players")
	if len(warn) != 1 {
		t.Fatalf("%d seed lines, want 1: %s", len(warn), b)
	}
	if l := warn[0]; l["level"] != "WARN" || l["entities"] != float64(simtest.SizingEntities) ||
		l["zones"] != float64(simtest.SizingZones) || l["rooms"] != float64(simtest.SizingRooms) {
		t.Errorf("seed line %v", l)
	}
}

// AC-2: content with no town.Merchant fails the seed, with an error line
// naming the Template and the content dir.
func TestSeedNamesAMissingTemplate(t *testing.T) {
	core, errs := sim.BuildTemplates([]sim.TemplateInput{
		{File: "andara.core.Entity.json", Def: &contentv1.TemplateDefinition{FormatVersion: sim.TemplateFormatVersion, Name: "andara.core.Entity",
			Kind: contentv1.TemplateKind_ENTITY, Chain: []string{"andara.core.Entity"}, Resolved: true}},
		{File: "andara.core.Character.json", Def: &contentv1.TemplateDefinition{FormatVersion: sim.TemplateFormatVersion, Name: "andara.core.Character",
			Kind: contentv1.TemplateKind_ENTITY, Chain: []string{"andara.core.Entity", "andara.core.Character"}, Resolved: true}},
	}, sim.TemplateOptions{})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	e := sizingEngine(t, core)
	log, b := jsonLog()
	s := NewSeeder(func() *sim.Engine { return e }, "/srv/sizing-content", log)
	stop := make(chan struct{})
	done := runLoop(t, s, e, stop)
	err := s.Seed(context.Background(), done)
	close(stop)
	<-done
	var missing *simtest.MissingTemplateError
	if !errors.As(err, &missing) || missing.Ref != "town.Merchant" {
		t.Fatalf("err = %v, want town.Merchant missing", err)
	}
	l := lines(t, b, "sizing build: the content has no Template the fixture needs")
	if len(l) != 1 || l[0]["level"] != "ERROR" || l[0]["template"] != "town.Merchant" || l[0]["content_dir"] != "/srv/sizing-content" {
		t.Fatalf("error lines %v; log %s", l, b)
	}
	for _, z := range e.State().Zones {
		if len(z.Entities) != 0 {
			t.Fatalf("zone %s holds %d Entities after a failed seed", z.ID, len(z.Entities))
		}
	}
}

// Before Seed asks, BeforeTick seeds nothing: the content isn't in effect
// yet. A loop that stops first ends the wait.
func TestSeedWaitsForTheAsk(t *testing.T) {
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	e := sizingEngine(t, reg)
	log, _ := jsonLog()
	s := NewSeeder(func() *sim.Engine { return e }, "/content", log)
	for range 3 {
		s.BeforeTick(context.Background(), time.Time{})
	}
	for _, z := range e.State().Zones {
		if len(z.Entities) != 0 {
			t.Fatal("seeded before Seed asked")
		}
	}
	stopped := make(chan struct{})
	close(stopped)
	if err := s.Seed(context.Background(), stopped); !errors.Is(err, ErrLoopStopped) {
		t.Fatalf("err = %v, want ErrLoopStopped", err)
	}
}

// AC-5, at the Engine: two Worlds seeded with the same sim.seed reach the
// same State Hash at the same tick, whichever tick each was seeded on.
func TestSeededWorldsHashAlike(t *testing.T) {
	reg, err := simtest.Templates()
	if err != nil {
		t.Fatal(err)
	}
	const at = 12
	hashAt := func(seedOn sim.Tick) [32]byte {
		e := sizingEngine(t, reg)
		log, _ := jsonLog()
		s := NewSeeder(func() *sim.Engine { return e }, "/content", log)
		for e.Tick() < at {
			if e.Tick() == seedOn {
				s.requested.Store(true)
			}
			s.BeforeTick(context.Background(), time.Time{})
			if _, err := e.Step(sim.TickInput{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Seed(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		return e.StateHash()
	}
	if a, b := hashAt(1), hashAt(9); a != b {
		t.Fatalf("seeded on tick 1: %x; on tick 9: %x", a, b)
	}
}
