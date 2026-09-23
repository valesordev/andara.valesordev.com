// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
)

// seedSnapshots writes one real round into a temporary fs store and returns
// the directory. Real objects, produced by the same encoder the server uses,
// so this exercises the listing and the decode rather than a fixture's idea of
// what an envelope looks like.
func seedSnapshots(t *testing.T, ticks ...sim.Tick) string {
	t.Helper()
	dir := t.TempDir()
	ws := store.NewFS(dir)
	e, err := simtest.NewEngine(1)
	if err != nil {
		t.Fatalf("simtest.NewEngine: %v", err)
	}
	simtest.Place(e, "hero", "town", "plaza")
	for _, want := range ticks {
		for e.Tick() < want {
			if _, err := e.Step(sim.TickInput{}); err != nil {
				t.Fatalf("Step: %v", err)
			}
		}
		// A distinct offset per round, so the listing has something to order.
		e.State().Offsets[sim.PartitionFor("town")] = int64(want)
		for _, s := range e.SnapshotAll(1758500000000000000) {
			b, err := s.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if err := ws.Put(context.Background(), s.Key(), b); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}
	return dir
}

func TestSnapshotListPrintsARowPerRoundNewestFirst(t *testing.T) {
	dir := seedSnapshots(t, 10, 20, 30)
	res := runCLI(t, []string{"snapshot", "list", "--zone", "town", "--fs-path", dir}, isolatedEnv(t, nil))
	if res.exit != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 4 { // header plus three rounds
		t.Fatalf("expected a header and three rows, got:\n%s", res.stdout)
	}
	if !strings.HasPrefix(lines[0], "TICK") {
		t.Errorf("missing header: %q", lines[0])
	}
	// Newest offset first.
	for i, want := range []string{"30", "20", "10"} {
		if !strings.HasPrefix(lines[i+1], want) {
			t.Errorf("row %d = %q, want it to start with tick %s", i, lines[i+1], want)
		}
	}
}

func TestSnapshotListJSONCarriesTheFullHash(t *testing.T) {
	dir := seedSnapshots(t, 10)
	res := runCLI(t, []string{"snapshot", "list", "--zone", "town", "--fs-path", dir, "-o", "json"}, isolatedEnv(t, nil))
	if res.exit != 0 {
		t.Fatalf("exit %d\nstderr: %s", res.exit, res.stderr)
	}
	var out struct {
		Zone      string `json:"zone"`
		Count     int    `json:"count"`
		Snapshots []struct {
			Tick         uint64 `json:"tick"`
			StateVersion uint32 `json:"state_version"`
			Offset       int64  `json:"offset"`
			Key          string `json:"key"`
			Bytes        int    `json:"bytes"`
			StateHash    string `json:"state_hash"`
			Error        string `json:"error"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, res.stdout)
	}
	if out.Zone != "town" || out.Count != 1 {
		t.Fatalf("zone %q, count %d", out.Zone, out.Count)
	}
	row := out.Snapshots[0]
	if row.Error != "" {
		t.Fatalf("row carries an error: %s", row.Error)
	}
	if row.Tick != 10 || row.Offset != 10 || row.StateVersion != sim.StateVersion {
		t.Errorf("row = %+v", row)
	}
	if row.Bytes == 0 || row.Key == "" {
		t.Errorf("row = %+v", row)
	}
	// The full 32-byte hash, not the table's abbreviation.
	if b, err := hex.DecodeString(row.StateHash); err != nil || len(b) != 32 {
		t.Errorf("state_hash = %q (%d bytes decoded, err %v), want 32 bytes of hex", row.StateHash, len(b), err)
	}
}

func TestSnapshotListOnAnEmptyStoreSaysSo(t *testing.T) {
	res := runCLI(t, []string{"snapshot", "list", "--zone", "town", "--fs-path", t.TempDir()}, isolatedEnv(t, nil))
	if res.exit != 0 {
		t.Fatalf("exit %d\nstderr: %s", res.exit, res.stderr)
	}
	if !strings.Contains(res.stdout, "no snapshots for zone town") {
		t.Errorf("stdout = %q", res.stdout)
	}
}

// An unreadable object is still a row: "there is something here and it cannot
// be read" is the answer the operator came for, and dropping it would make a
// corrupt round look like a missing one.
func TestSnapshotListReportsAnUnreadableObjectAsARow(t *testing.T) {
	dir := seedSnapshots(t, 10)
	ws := store.NewFS(dir)
	if err := ws.Put(context.Background(), sim.SnapshotKey("town", sim.StateVersion, 20, 20), []byte("not an envelope")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	res := runCLI(t, []string{"snapshot", "list", "--zone", "town", "--fs-path", dir, "-o", "json"}, isolatedEnv(t, nil))
	if res.exit != 0 {
		t.Fatalf("exit %d\nstderr: %s", res.exit, res.stderr)
	}
	var out struct {
		Count     int `json:"count"`
		Snapshots []struct {
			Error string `json:"error"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 2 {
		t.Fatalf("count = %d, want the good object and the bad one", out.Count)
	}
	if out.Snapshots[0].Error == "" {
		t.Error("the unreadable object was listed without an error")
	}
	if out.Snapshots[1].Error != "" {
		t.Errorf("the readable object carries an error: %s", out.Snapshots[1].Error)
	}
}

func TestSnapshotListRequiresAZone(t *testing.T) {
	res := runCLI(t, []string{"snapshot", "list", "--fs-path", t.TempDir()}, isolatedEnv(t, nil))
	if res.exit == 0 {
		t.Fatal("snapshot list without --zone succeeded")
	}
	if !strings.Contains(res.stderr, "--zone") {
		t.Errorf("stderr does not name the missing flag: %q", res.stderr)
	}
}
