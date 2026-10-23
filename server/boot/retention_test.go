// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/store"
)

// AW-SRV-055: Admin.TagSnapshotRound, tags on ListSnapshotRounds, and the
// retention sweep's gauge and log line, over an fs store.

// taggingRuntime is a runtime whose snapshot store holds n complete rounds of
// the simtest World, oldest first, and one incomplete round after them.
func taggingRuntime(t *testing.T, n int) (*Runtime, *bytes.Buffer, *store.FS, []sim.Tick, sim.Tick) {
	t.Helper()
	rt, logs := runtime(t, fixture(t, "valid"), false)
	w, err := simtest.World()
	if err != nil {
		t.Fatal(err)
	}
	rt.World = w
	rt.Cfg.SnapshotInterval, rt.Cfg.SnapshotStore, rt.Cfg.SnapshotFSPath = time.Minute, "fs", t.TempDir()
	rt.Cfg.SnapshotKeepRounds, rt.Cfg.SnapshotKeepDeployRounds, rt.Cfg.SnapshotMaxRoundAge = 2, 30, 552*time.Hour
	fs := store.NewFS(rt.Cfg.SnapshotFSPath)
	e, err := simtest.NewEngine(11)
	if err != nil {
		t.Fatal(err)
	}
	remain := simtest.Script(12)
	write := func(whole bool) sim.Tick {
		if _, err := e.Step(simtest.Batch(remain, 2)); err != nil {
			t.Fatal(err)
		}
		snaps := e.SnapshotAll(time.Now().UnixNano())
		if !whole {
			snaps = snaps[1:]
		}
		for i := range snaps {
			b, err := snaps[i].Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := fs.Put(context.Background(), snaps[i].Key(), b); err != nil {
				t.Fatal(err)
			}
		}
		return e.Tick()
	}
	var ticks []sim.Tick
	for range n {
		ticks = append(ticks, write(true))
	}
	return rt, logs, fs, ticks, write(false)
}

func asStatus(t *testing.T, err error) *snapshotStatus {
	t.Helper()
	var ss *snapshotStatus
	if !errors.As(err, &ss) {
		t.Fatalf("err %v, want a snapshotStatus", err)
	}
	return ss
}

// AC-10: the refusals and their statuses, the idempotent re-tag, and the
// actor and tick on the log line.
func TestTagSnapshotRound_StatusesAndIdempotence(t *testing.T) {
	rt, logs, _, ticks, incomplete := taggingRuntime(t, 2)
	a := rt.NewSnapshotAdmin()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "acct-op", Roles: []auth.Role{auth.RoleOperator}})
	tag := func(name string, tick sim.Tick) (*adminv1.TagSnapshotRoundResponse, error) {
		return a.TagSnapshotRound(ctx, &adminv1.TagSnapshotRoundRequest{Name: name, Tick: uint64(tick)})
	}
	if _, err := tag("release:v1", ticks[0]); asStatus(t, err).code != codeInvalidArgument {
		t.Errorf("bad name: %v", err)
	}
	if _, err := tag("deploy:v1", 999999); asStatus(t, err).code != codeNotFound || !strings.Contains(err.Error(), "round 999999 not found") {
		t.Errorf("no round: %v", err)
	}
	if _, err := tag("deploy:v1", incomplete); asStatus(t, err).code != codeFailedPrecondition || !strings.Contains(err.Error(), "is not complete") {
		t.Errorf("incomplete: %v", err)
	}
	resp, err := tag("deploy:v1", 0)
	if err != nil || resp.GetTick() != uint64(ticks[1]) || resp.GetAlreadyTagged() {
		t.Fatalf("newest complete: %v %v", resp, err)
	}
	if resp, err := tag("deploy:v1", ticks[1]); err != nil || !resp.GetAlreadyTagged() {
		t.Fatalf("re-tag: %v %v", resp, err)
	}
	var line map[string]any
	for _, l := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "snapshot round tagged" {
			line = m
			break
		}
	}
	if line == nil || line["actor"] != "acct-op" || line["tick"] != float64(ticks[1]) || line["name"] != "deploy:v1" {
		t.Fatalf("tag log line %v\n%s", line, logs.String())
	}

	list, err := a.ListSnapshotRounds(context.Background(), &adminv1.ListSnapshotRoundsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list.GetRounds() {
		want := []string(nil)
		if r.GetTick() == uint64(ticks[1]) {
			want = []string{"deploy:v1"}
		}
		if !slices.Equal(r.GetTags(), want) {
			t.Errorf("round %d tags %v, want %v", r.GetTick(), r.GetTags(), want)
		}
	}
}

// With snapshots off the RPC is refused as ListSnapshotRounds is.
func TestTagSnapshotRound_SnapshotsDisabled(t *testing.T) {
	rt, _ := runtime(t, fixture(t, "valid"), false)
	_, err := rt.NewSnapshotAdmin().TagSnapshotRound(context.Background(), &adminv1.TagSnapshotRoundRequest{Name: "deploy:v1"})
	if ss := asStatus(t, err); ss.code != codeFailedPrecondition || ss.reason != "snapshots_disabled" {
		t.Fatalf("got %v", err)
	}
}

// §7: the sweep sets andara_snapshot_rounds_retained{kind} and logs deleted,
// kept_tagged and kept_newest.
func TestSweepSnapshots_GaugeAndLogLine(t *testing.T) {
	rt, logs, fs, ticks, _ := taggingRuntime(t, 4)
	if _, err := rt.NewSnapshotAdmin().TagSnapshotRound(context.Background(), &adminv1.TagSnapshotRoundRequest{Name: "deploy:old", Tick: uint64(ticks[0])}); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	m := NewRetentionMetrics(reg)
	rt.sweepSnapshots(context.Background(), fs, m, time.Now())
	// Five rounds: the incomplete newest and ticks[3] are the two untagged
	// kept (ticks[3] the newest complete), ticks[0] the tagged one; ticks[1]
	// and ticks[2] go.
	if u, g := testutil.ToFloat64(m.Retained.WithLabelValues("untagged")), testutil.ToFloat64(m.Retained.WithLabelValues("tagged")); u != 2 || g != 1 {
		t.Fatalf("retained untagged=%v tagged=%v", u, g)
	}
	var line map[string]any
	for _, l := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "snapshot retention sweep" {
			line = m
		}
	}
	if line == nil || line["level"] != "INFO" || line["deleted"] != float64(2) || line["kept_tagged"] != float64(1) || line["kept_newest"] != float64(2) {
		t.Fatalf("sweep log line %v", line)
	}
	if n, err := testutil.GatherAndCount(reg, "andara_snapshot_rounds_retained"); err != nil || n != 2 {
		t.Fatalf("series %d %v, want both kinds", n, err)
	}
}
