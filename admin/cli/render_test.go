// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// The rendering contract, golden-filed over a recorded Event stream that
// covers every row of AW-CLI-004's table — and a synthetic future Event
// for the unknown-type row, built as bytes because no Go type for it
// exists, which is the point. Regenerate with `make goldens`.
func TestRender_Golden(t *testing.T) {
	events := recordedEvents(t, filepath.Join("testdata", "play", "events.jsonl"))
	events = append(events, futureEvent(t))

	var b strings.Builder
	for _, env := range events {
		fmt.Fprintf(&b, "# %s event_id=%d tick=%d client_ref=%q\n", eventName(env), env.GetEventId(), env.GetTick(), env.GetClientRef())
		lines := renderEvent(env)
		if len(lines) == 0 {
			b.WriteString("(nothing)\n")
		}
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
	}
	got := b.String()

	path := filepath.Join("testdata", "play", "transcript.txt")
	if *updateGoldens || os.Getenv("UPDATE_GOLDENS") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `make goldens`)", path, err)
	}
	if string(want) != got {
		t.Errorf("rendering changed\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// Every row of the table is in the recording, so a row dropped from the
// file is noticed rather than silently uncovered.
func TestRender_RecordingCoversTheTable(t *testing.T) {
	seen := map[string]bool{}
	for _, env := range recordedEvents(t, filepath.Join("testdata", "play", "events.jsonl")) {
		seen[eventName(env)] = true
	}
	for _, want := range []string{"RoomDescribed", "CharacterArrived", "CharacterLeft", "CommandRejected", "Heartbeat", "Resync", "ZoneFaulted", "SubscriberDropped", "SimulationStopped"} {
		if !seen[want] {
			t.Errorf("the recording has no %s", want)
		}
	}
}

// The unknown-type row: a payload field this client's schema does not
// have renders one line and never panics, and reports as unknown.
func TestRender_UnknownPayload(t *testing.T) {
	env := futureEvent(t)
	if got := eventName(env); got != "unknown" {
		t.Errorf("eventName = %q, want unknown", got)
	}
	lines := renderEvent(env)
	if len(lines) != 1 || !strings.Contains(lines[0], "event 99") {
		t.Errorf("rendered %q", lines)
	}
	if len(renderEvent(&gamev1.EventEnvelope{})) != 1 {
		t.Error("an empty envelope did not render one line")
	}
	if len(renderEvent(nil)) != 1 {
		t.Error("a nil envelope did not render one line")
	}
}

func recordedEvents(t *testing.T, path string) []*gamev1.EventEnvelope {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []*gamev1.EventEnvelope
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		env := &gamev1.EventEnvelope{}
		if err := protojson.Unmarshal([]byte(line), env); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		out = append(out, env)
	}
	return out
}

// futureEvent is an envelope whose payload is field 40, a message type
// this schema does not define: what a newer server would send.
func futureEvent(t *testing.T) *gamev1.EventEnvelope {
	t.Helper()
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 99)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, 500)
	b = protowire.AppendTag(b, 40, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte{0x0a, 0x03, 'n', 'e', 'w'})
	env := &gamev1.EventEnvelope{}
	if err := proto.Unmarshal(b, env); err != nil {
		t.Fatal(err)
	}
	return env
}
