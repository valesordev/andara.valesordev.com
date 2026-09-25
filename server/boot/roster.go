// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/valesordev/andara/server/roster"
	"github.com/valesordev/andara/server/sim"
)

// StartRoster builds the Character roster (AW-SRV-014) over the account
// store, the routing table, and the Command log. It runs after
// StartIngress and before the gateway, which takes it as its Roster seam.
//
// character.spawn_room is resolved against the loaded content here, and
// the World must hold andara.core.Character: a server that would spawn a
// Character into a Room that does not exist, or from a Template it does
// not have, refuses to boot rather than refuse the first player.
func (rt *Runtime) StartRoster(ctx context.Context) error {
	cfg := rt.Cfg
	if rt.Accounts == nil || rt.Bindings == nil || rt.commandLog == nil {
		return fmt.Errorf("roster: accounts and ingress must be started first")
	}
	zone, room, err := cfg.SpawnRoom()
	if err != nil {
		return err
	}
	// Checked against the content in effect, after reconcile
	// (CheckSpawnInEffect): the roster is built before the loop runs, when
	// no content is in effect yet.
	spawn := sim.RoomRef{Zone: sim.ZoneID(zone), Room: sim.RoomID(room)}
	r, err := roster.New(roster.Options{
		Accounts:        rt.Accounts,
		Bindings:        rt.Bindings,
		Log:             rt.commandLog,
		SpawnRoom:       spawn,
		ProduceDeadline: cfg.IngressProduceDeadline,
		Metrics:         roster.NewMetrics(rt.Tel.Reg),
		Logger:          rt.Tel.Log,
		Tracer:          rt.Tel.Tracer,
	})
	if err != nil {
		return err
	}
	rt.Roster = r
	// A Character that crosses a Zone moves the roster with it, so the
	// next BindCharacter after a crash is routed to the Zone it is in
	// rather than re-routed by the sim a tick later (AW-SRV-014).
	rt.Bindings.OnZoneChange = r.ObserveMove
	rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "character roster configured",
		slog.String("spawn_room", cfg.CharacterSpawnRoom),
		slog.Int("max_per_account", cfg.CharacterMaxPerAccount),
		slog.String("name_pattern", cfg.CharacterNamePattern),
	)
	return nil
}

// spawnRoom is character.spawn_room as a RoomRef; zero when unset or malformed.
func (rt *Runtime) spawnRoom() sim.RoomRef {
	zone, room, err := rt.Cfg.SpawnRoom()
	if err != nil {
		return sim.RoomRef{}
	}
	return sim.RoomRef{Zone: sim.ZoneID(zone), Room: sim.RoomID(room)}
}

// checkSpawn refuses content a Character cannot be made in: no
// character.spawn_room, or no andara.core.Character — against the content in
// effect, which is what the roster spawns into.
func (rt *Runtime) checkSpawn() error {
	zone, room, err := rt.Cfg.SpawnRoom()
	if err != nil {
		return err
	}
	spawn := sim.RoomRef{Zone: sim.ZoneID(zone), Room: sim.RoomID(room)}
	if _, ok := rt.World.Resolve(spawn); !ok {
		return fmt.Errorf("character.spawn_room %q does not resolve against the loaded content", rt.Cfg.CharacterSpawnRoom)
	}
	if _, ok := rt.Templates.Get(sim.CharacterTemplate); !ok {
		return fmt.Errorf("the loaded content has no %s template; a Character cannot be made from it", sim.CharacterTemplate)
	}
	return nil
}

// CheckSpawnInEffect is checkSpawn against the content in effect, after
// ReconcileContent (AW-SRV-012).
func (rt *Runtime) CheckSpawnInEffect() error { return rt.checkSpawn() }

// observeCharacters sets andara_characters_total from the sim's Zone
// state: the bodies, present and dormant. Called on the loop goroutine.
func (rt *Runtime) observeCharacters(engine *sim.Engine) {
	if rt.Roster == nil {
		return
	}
	c := engine.Characters()
	m := rt.Roster.Metrics().Characters
	m.WithLabelValues(roster.StatePresent).Set(float64(c.Present))
	m.WithLabelValues(roster.StateDormant).Set(float64(c.Dormant))
}
