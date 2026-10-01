// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"testing"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-042 AC-8: on a World with no Zones, the first Zone-bearing version
// must hold character.spawn_room. Activating one that doesn't is refused
// spawn_room_removed naming the Room, and the pointer stays where it was;
// one that holds it activates.
func TestActivateVersion_TheFirstZonesMustHoldTheSpawnRoom(t *testing.T) {
	h := newPubHarness(t, func(_ *AdminOptions, l *LoaderOptions) {
		l.SpawnRoom = sim.RoomRef{Zone: "purgatory", Room: "start"}
	})
	// Only andara.core is in effect: a World with no Zones, as a waiting
	// server's is.
	if zones, _ := h.loader.Inputs(); len(zones) != 0 {
		t.Fatalf("the harness starts with %d Zones in effect", len(zones))
	}
	approved := func(files map[string][]byte) uint64 {
		t.Helper()
		pv, err := h.publish(builder(alice), "town", files)
		if err != nil {
			t.Fatal(err) // the publish gate judges a version alone, with no spawn rule
		}
		if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: pv.GetVersion()}); err != nil {
			t.Fatal(err)
		}
		return pv.GetVersion()
	}
	files := townFiles(t)
	files["purgatory.json"] = bytes.ReplaceAll(files["purgatory.json"], []byte(`"start"`), []byte(`"waiting"`))
	v := approved(files)
	_, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: v})
	ae := adminError(t, err, CodeFailedPrecondition, RefusalSpawnRoomRemoved)
	if ar, ok := ae.Detail.(*adminv1.ActivationRefusal); !ok || ar.GetReason() != RefusalSpawnRoomRemoved || !slices.Equal(ar.GetSubjects(), []string{"purgatory/start"}) {
		t.Fatalf("detail %v", ae.Detail)
	}
	if got := h.reg.Pointers()["town"]; got != 0 {
		t.Fatalf("the pointer moved to town@%d", got)
	}
	if zones, _ := h.loader.Inputs(); len(zones) != 0 {
		t.Fatal("the refused version came into effect")
	}

	good := approved(townFiles(t))
	if _, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: good}); err != nil {
		t.Fatalf("a first version holding the spawn Room: %v", err)
	}
	h.follow("town", good)
}

// recordingProducer keeps every swap the Loader produces, then hands it on.
type recordingProducer struct {
	mu   sync.Mutex
	next SwapProducer
	cmds []*logv1.LoggedCommand
}

func (r *recordingProducer) ProduceSwap(ctx context.Context, cmd *logv1.LoggedCommand) error {
	r.mu.Lock()
	r.cmds = append(r.cmds, cmd)
	r.mu.Unlock()
	return r.next.ProduceSwap(ctx, cmd)
}

// AW-SRV-042 AC-9: every swap the Loader produces carries zone_count, the
// Zones in the whole World after it: 0 for andara.core's Templates alone. A
// fresh Engine replaying the recorded swaps holds exactly that many Zones
// after each.
func TestLoader_SwapsCarryTheWorldsZoneCount(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{})
	s.publish("town", 1, 0, map[string]string{"town.json": zoneJSON("town", "square")})
	s.publish("docks", 1, 0, map[string]string{"docks.json": zoneJSON("docks", "pier")})
	l := NewLoader(LoaderOptions{Store: s, Packs: []string{AllPacks}, Metrics: NewMetrics(nil)})
	rec := &recordingProducer{next: attachEngine(l)}
	l.SetProducer(rec)
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("load: %v %v", rejects, err)
	}
	// core first, then the packs in order, each adding a Zone.
	want := []uint32{0, 1, 2}
	if len(rec.cmds) != 3 {
		t.Fatalf("%d swaps produced, want 3", len(rec.cmds))
	}

	replay := sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 1, Partitions: []int32{sim.WorldPartition}, Content: l})
	for i, cmd := range rec.cmds {
		cs := cmd.GetContentSwap()
		if got := cs.GetZoneCount(); got != want[i] {
			t.Errorf("swap %d, %s@%d: zone_count %d, want %d", i, cs.GetPackId(), cs.GetVersion(), got, want[i])
		}
		if _, err := replay.Step(sim.TickInput{Records: []sim.Record{{Partition: sim.WorldPartition, Offset: int64(i), Command: cmd}}}); err != nil {
			t.Fatal(err)
		}
		if got := uint32(len(replay.World().Zones)); got != cs.GetZoneCount() {
			t.Errorf("replaying %s@%d: %d Zones, zone_count says %d", cs.GetPackId(), cs.GetVersion(), got, cs.GetZoneCount())
		}
	}
}
