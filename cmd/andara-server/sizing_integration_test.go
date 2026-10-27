// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build sizing && integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/valesordev/andara/internal/testpki"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// sizingContent writes a content dir the way SRE's generator will: the
// sizing fixture's Zones (SizingWorld's z00..z15, SizingRooms Rooms in
// north-south chains) beside the core Templates and, with merchant,
// town.Merchant.
func sizingContent(t *testing.T, merchant bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := abs(t, filepath.Join("..", "..", "testdata", "content", "valid", "templates"))
	names := []string{"andara.core.Entity", "andara.core.Character", "andara.core.Npc", "andara.core.Item"}
	if merchant {
		names = append(names, "town.Merchant")
	}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(src, n+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "templates", n+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type exit struct {
		Direction string `json:"direction"`
		ToRoom    string `json:"toRoom"`
	}
	type room struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Exits       []exit `json:"exits,omitempty"`
	}
	per := simtest.SizingRooms / simtest.SizingZones
	for z := range simtest.SizingZones {
		n := per
		if z == simtest.SizingZones-1 {
			n = simtest.SizingRooms - per*(simtest.SizingZones-1)
		}
		rooms := make([]room, n)
		for r := range rooms {
			rooms[r] = room{ID: fmt.Sprintf("r%04d", r), Title: "Room", Description: "A room."}
			if r > 0 {
				rooms[r].Exits = []exit{{"north", rooms[r-1].ID}}
				rooms[r-1].Exits = append(rooms[r-1].Exits, exit{"south", rooms[r].ID})
			}
		}
		id := fmt.Sprintf("z%02d", z)
		b, err := json.Marshal(map[string]any{"formatVersion": 1, "id": id, "name": "Zone " + id, "fallbackRoom": "r0000", "rooms": rooms})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// sizingRun is one boot of the sizing binary, as the loop saw it.
type sizingRun struct {
	mu                     sync.Mutex
	seededAt               sim.Tick
	entities, zones, rooms int
	hashAt                 map[sim.Tick][32]byte
	httpAddr               string
	stderr                 *lockedBuffer
	done                   chan int
}

// bootSizing runs the sizing build in-process against content, recording the
// seeded World's shape on the first tick after the seed and its State Hash
// at each tick in hashTicks.
func bootSizing(t *testing.T, content string, hashTicks ...sim.Tick) *sizingRun {
	t.Helper()
	pki := testpki.New(t)
	r := &sizingRun{hashAt: map[sim.Tick][32]byte{}, httpAddr: freeAddr(t), stderr: &lockedBuffer{}, done: make(chan int, 1)}
	observeSeeded = func(e *sim.Engine) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.seededAt == 0 {
			r.seededAt = e.Tick()
			rooms := map[string]bool{}
			for _, z := range e.State().Zones {
				r.zones++
				for _, ent := range z.Entities {
					r.entities++
					rooms[string(z.ID)+"/"+string(ent.Room)] = true
				}
			}
			r.rooms = len(rooms)
		}
		for _, at := range hashTicks {
			if e.Tick() == at {
				r.hashAt[at] = e.StateHash()
			}
		}
	}
	t.Cleanup(func() { observeSeeded = nil })
	go func() {
		r.done <- run([]string{
			"--content-source=dir", "--content-path=" + content,
			"--grpc-listen=" + freeAddr(t), "--http-port=" + r.httpAddr,
			"--tls-cert-file=" + pki.CertFile, "--tls-key-file=" + pki.KeyFile,
			"--auth-store=memory", "--sim-source=memory", "--snapshot-interval=0",
			"--auth-token-key-file=" + keyFile(t), "--character-spawn-room=z00/r0000",
			"--sim-seed=7",
		}, emptyEnv, &bytesDiscard{}, r.stderr)
	}()
	return r
}

type bytesDiscard struct{}

func (bytesDiscard) Write(p []byte) (int, error) { return len(p), nil }

func (r *sizingRun) stop(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-r.done:
		if code != 0 {
			t.Fatalf("exit %d after SIGTERM; stderr=%s", code, r.stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("server did not exit; stderr=%s", r.stderr.String())
	}
}

func (r *sizingRun) ready(t *testing.T) {
	t.Helper()
	waitUntil(t, func() bool {
		select {
		case code := <-r.done:
			r.done <- code
			t.Fatalf("server exited %d before ready; stderr=%s", code, r.stderr.String())
		default:
		}
		resp, err := http.Get("http://" + r.httpAddr + "/readyz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "/readyz to answer 200")
}

var tickCount = regexp.MustCompile(`(?m)^andara_tick_duration_seconds_count (\d+)$`)

func ticksObserved(t *testing.T, httpAddr string) int {
	t.Helper()
	m := tickCount.FindStringSubmatch(metrics(t, httpAddr))
	if m == nil {
		t.Fatal("no andara_tick_duration_seconds_count in /metrics")
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// AC-1 and AC-4: ready means seeded. The World holds SizingEntities over
// SizingZones Zones and SizingRooms Rooms, the tick histogram keeps observing
// with them present, one warn says what was seeded, and andara_build_info
// carries build_tags="sizing".
func TestSizingBuildBootsTheFixture(t *testing.T) {
	r := bootSizing(t, sizingContent(t, true))
	r.ready(t)
	r.mu.Lock()
	seededAt, entities, zones, rooms := r.seededAt, r.entities, r.zones, r.rooms
	r.mu.Unlock()
	if seededAt == 0 {
		t.Fatal("ready before the World was seeded")
	}
	if entities != simtest.SizingEntities || zones != simtest.SizingZones || rooms != simtest.SizingRooms {
		t.Errorf("World holds %d Entities over %d Zones and %d Rooms, want %d over %d and %d",
			entities, zones, rooms, simtest.SizingEntities, simtest.SizingZones, simtest.SizingRooms)
	}
	before := ticksObserved(t, r.httpAddr)
	waitUntil(t, func() bool { return ticksObserved(t, r.httpAddr) > before+5 }, "andara_tick_duration_seconds to keep observing")
	if m := metrics(t, r.httpAddr); !strings.Contains(m, `build_tags="sizing"`) {
		t.Errorf("andara_build_info lacks build_tags=\"sizing\":\n%s", grepLines(m, "andara_build_info"))
	}
	r.stop(t)
	out := r.stderr.String()
	if n := strings.Count(out, `"msg":"sizing build: seeded World, not for serving players"`); n != 1 {
		t.Fatalf("%d seed lines, want 1:\n%s", n, out)
	}
	for _, want := range []string{`"level":"WARN","msg":"sizing build: seeded World, not for serving players"`,
		fmt.Sprintf(`"entities":%d`, simtest.SizingEntities), fmt.Sprintf(`"zones":%d`, simtest.SizingZones), fmt.Sprintf(`"rooms":%d`, simtest.SizingRooms)} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %s", want)
		}
	}
}

// AC-2: content without town.Merchant exits 1 before serving, with an error
// line naming the Template and the content dir.
func TestSizingBuildRefusesContentWithoutTheMerchant(t *testing.T) {
	content := sizingContent(t, false)
	r := bootSizing(t, content)
	var code int
	select {
	case code = <-r.done:
	case <-time.After(60 * time.Second):
		r.stop(t)
		t.Fatalf("still running without town.Merchant; stderr=%s", r.stderr.String())
	}
	out := r.stderr.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, out)
	}
	line := grepLines(out, "sizing build: the content has no Template the fixture needs")
	if !strings.Contains(line, `"level":"ERROR"`) || !strings.Contains(line, `"template":"town.Merchant"`) || !strings.Contains(line, `"content_dir":"`+content+`"`) {
		t.Fatalf("no error line naming town.Merchant and %s:\n%s", content, out)
	}
	if strings.Contains(out, "grpc listen") {
		t.Errorf("the Gateway served before the seed failed:\n%s", out)
	}
}

// AC-6: off local, off the memory source, or with snapshots on, the sizing
// build exits 1 before anything is built, naming the key, its value and the
// value required. The content validates, so the guard is the only exit 1.
func TestSizingBuildRefusesToBootOutsideLocal(t *testing.T) {
	content := sizingContent(t, true)
	for _, c := range []struct {
		arg, env, key, value, required string
	}{
		{"", "", "", "", ""},
		{"", "dev", "ANDARA_ENV", "dev", "local"},
		{"--sim-source=kafka", "", "sim.source", "kafka", "memory"},
		{"--snapshot-interval=1m", "", "snapshot.interval", "1m0s", "0"},
	} {
		env := func(k string) (string, bool) {
			if k == "ANDARA_ENV" && c.env != "" {
				return c.env, true
			}
			return emptyEnv(k)
		}
		args := []string{"--content-source=dir", "--content-path=" + content, "--sim-source=memory", "--snapshot-interval=0", "--validate-only"}
		if c.arg != "" {
			args = append(args, c.arg)
		}
		stderr := &lockedBuffer{}
		code := run(args, env, &bytesDiscard{}, stderr)
		line := grepLines(stderr.String(), "sizing build: refusing to boot")
		if c.key == "" {
			if code != 0 || line != "" {
				t.Fatalf("local, memory, no snapshots: exit %d; %s", code, stderr.String())
			}
			continue
		}
		if code != 1 {
			t.Errorf("%s: exit %d, want 1", c.key, code)
		}
		for _, want := range []string{`"level":"ERROR"`, `"key":"` + c.key + `"`, `"value":"` + c.value + `"`, `"required":"` + c.required + `"`} {
			if !strings.Contains(line, want) {
				t.Errorf("%s: refusal lacks %s:\n%s", c.key, want, stderr.String())
			}
		}
	}
}

// AC-5: two boots with the same sim.seed, the loop running 60 s with no
// Commands, reach the same State Hash at the same tick.
func TestSizingBuildHashesAlikeAcrossBoots(t *testing.T) {
	// 60 s of ticks, plus headroom for the boot to seed first.
	at := sim.Tick(60*config.DefaultSimTickRate + 100)
	content := sizingContent(t, true)
	hash := func() [32]byte {
		r := bootSizing(t, content, at)
		r.ready(t)
		eventuallyHash := func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			_, ok := r.hashAt[at]
			return ok
		}
		deadline := time.Now().Add(3 * time.Minute)
		for !eventuallyHash() {
			if time.Now().After(deadline) {
				r.stop(t)
				t.Fatalf("tick %d never ran", at)
			}
			time.Sleep(time.Second)
		}
		r.stop(t)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.seededAt >= at {
			t.Fatalf("seeded at tick %d, after %d", r.seededAt, at)
		}
		return r.hashAt[at]
	}
	if a, b := hash(), hash(); a != b {
		t.Fatalf("State Hash at tick %d: %x, then %x", at, a, b)
	}
}

func grepLines(s, sub string) string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
