// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"sort"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// zoneDecl pairs a declaration with the file it came from, which a finding
// needs and the AST deliberately does not carry.
type zoneDecl struct {
	file string
	d    *ZoneDecl
}

// resolveZones builds every ZoneDefinition in the pack.
//
// Names are unique per kind across the pack, not per file (semantics.md §2),
// and Exits resolve within the pack and nowhere else: a to_zone naming a Zone
// in another pack is unknown_zone, because a pack is the unit of publication
// and an Exit may not leave it (semantics.md §3).
func (r *resolver) resolveZones() []*contentv1.ZoneDefinition {
	var decls []zoneDecl
	for _, f := range r.files {
		for _, d := range f.Decls {
			if zd, isZone := d.(*ZoneDecl); isZone {
				decls = append(decls, zoneDecl{f.Path, zd})
			}
		}
	}

	// Pass one: the Zone set, so an Exit can be resolved against every Zone in
	// the pack regardless of declaration order (semantics.md §2: order is not
	// meaning).
	byID := map[string]zoneDecl{}
	var kept []zoneDecl
	for _, zd := range decls {
		if prev, dup := byID[zd.d.ID]; dup {
			r.report(zd.file, zd.d.Pos, CodeDuplicateZone,
				fmt.Sprintf("Zone %q is declared in %s and again in %s; an id is unique within its pack", zd.d.ID, prev.file, zd.file),
				zd.d.ID)
			continue
		}
		byID[zd.d.ID] = zd
		kept = append(kept, zd)
	}

	rooms := map[string]map[string]*RoomDecl{}
	for _, zd := range kept {
		rooms[zd.d.ID] = map[string]*RoomDecl{}
	}

	out := make([]*contentv1.ZoneDefinition, 0, len(kept))
	for _, zd := range kept {
		out = append(out, r.buildZone(zd, byID, rooms))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out
}

func (r *resolver) buildZone(zd zoneDecl, byID map[string]zoneDecl, rooms map[string]map[string]*RoomDecl) *contentv1.ZoneDefinition {
	z := zd.d
	chain := []string{z.ID}

	// At most one fallback per Zone; a second is duplicate_declaration at the
	// second keyword. fallback itself is PENDING AW-SRV-012 — the field does
	// not exist in zone.proto, so the value is validated and dropped
	// (semantics.md §9).
	for _, f := range z.Fallbacks[min(1, len(z.Fallbacks)):] {
		r.report(zd.file, f.Pos, CodeDuplicateDecl,
			fmt.Sprintf("Zone %q declares `fallback` more than once; the first is at %s", z.ID, z.Fallbacks[0].Pos), chain...)
	}

	def := &contentv1.ZoneDefinition{
		FormatVersion: FormatVersion,
		Id:            z.ID,
		Name:          z.Name,
		Components:    r.buildComponents(zd.file, z.Components, chain),
	}

	// Pass one over Rooms: the id set, so an Exit resolves to a Room declared
	// later in the file or in another file entirely.
	local := rooms[z.ID]
	var kept []*RoomDecl
	for _, rd := range z.Rooms {
		if _, dup := local[rd.ID]; dup {
			r.report(zd.file, rd.Pos, CodeDuplicateRoom,
				fmt.Sprintf("Room %q is declared twice in Zone %q; an id is unique within its Zone", rd.ID, z.ID),
				z.ID, rd.ID)
			continue
		}
		local[rd.ID] = rd
		kept = append(kept, rd)
	}
	for _, rd := range kept {
		def.Rooms = append(def.Rooms, r.buildRoom(zd, rd, byID))
	}

	// Sorted by id, like every repeated field, so that two compiles of the same
	// source produce identical bytes and the loader never has to re-sort
	// (semantics.md §7 rule 4).
	sort.Slice(def.Rooms, func(i, j int) bool { return def.Rooms[i].GetId() < def.Rooms[j].GetId() })

	r.warnOrphans(zd, kept)
	return def
}

func (r *resolver) buildRoom(zd zoneDecl, rd *RoomDecl, byID map[string]zoneDecl) *contentv1.RoomDefinition {
	z := zd.d
	chain := []string{z.ID, rd.ID}

	// At most one desc per Room. A Room with no desc compiles with an empty
	// description, which is legal: an unwritten room is a Builder mid-work
	// (semantics.md §3).
	for _, d := range rd.Descs[min(1, len(rd.Descs)):] {
		r.report(zd.file, d.Pos, CodeDuplicateDecl,
			fmt.Sprintf("Room %q declares `desc` more than once; the first is at %s", rd.ID, rd.Descs[0].Pos), chain...)
	}
	desc := ""
	if len(rd.Descs) > 0 {
		d := rd.Descs[0]
		if d.BadEsc != nil {
			r.report(zd.file, *d.BadEsc, CodeInvalidEscape,
				fmt.Sprintf("%s is not an escape this language has; the three that exist are \\n, \\\" and \\\\", d.BadEscWh),
				chain...)
		}
		desc = d.Value
	}

	room := &contentv1.RoomDefinition{
		Id:          rd.ID,
		Title:       rd.Title,
		Description: desc,
		Components:  r.buildComponents(zd.file, rd.Components, chain),
	}

	seenDir := map[string]*ExitDecl{}
	for _, e := range rd.Exits {
		ex := r.buildExit(zd, rd, e, byID, seenDir)
		if ex != nil {
			room.Exits = append(room.Exits, ex)
		}
	}
	// Exits sort lexicographically by direction string — east, north, south —
	// not in compass order. That is what the loader already does
	// (TestBuildWorld_ExitsSortedByDirection), and a compiler emitting a
	// different order would make "two loads of the same content serialize
	// identically" a property of the loader's re-sort rather than of the
	// content (semantics.md §7).
	sort.Slice(room.Exits, func(i, j int) bool { return room.Exits[i].GetDirection() < room.Exits[j].GetDirection() })
	return room
}

func (r *resolver) buildExit(zd zoneDecl, rd *RoomDecl, e *ExitDecl, byID map[string]zoneDecl, seenDir map[string]*ExitDecl) *contentv1.ExitDefinition {
	z := zd.d
	carrier := []string{z.ID, rd.ID}
	withDir := append(append([]string{}, carrier...), e.Direction)

	if !sim.Direction(e.Direction).Valid() {
		// The chain stops at the Room: the direction is the offending value
		// and is already at the position, so repeating it below the finding
		// would say the same thing twice.
		r.report(zd.file, e.DirPos, CodeUnknownDirection,
			fmt.Sprintf("no Direction %q; the twelve are %s", e.Direction, sim.DirectionsList()), carrier...)
		return nil
	}
	if prev, dup := seenDir[e.Direction]; dup {
		r.report(zd.file, e.Pos, CodeDuplicateDirection,
			fmt.Sprintf("Room %q already has an Exit %s, to %s; this one goes to %s",
				rd.ID, e.Direction, exitTarget(prev), exitTarget(e)), withDir...)
		return nil
	}
	seenDir[e.Direction] = e

	// perceives is PENDING AW-SRV-029: the senses are validated and dropped,
	// because ExitDefinition has no field to hold them (semantics.md §9).
	r.checkSenses(zd.file, e, withDir)

	targetZone := z.ID
	if e.ToZone != "" {
		targetZone = e.ToZone
		tz, known := byID[e.ToZone]
		if !known {
			r.report(zd.file, e.RefPos, CodeUnknownZone,
				fmt.Sprintf("no Zone %q in pack %q; a pack is the unit of publication and an Exit may not leave it", e.ToZone, r.pack),
				withDir...)
			return nil
		}
		if !hasRoom(tz.d, e.ToRoom) {
			r.report(zd.file, e.RefPos, CodeUnknownRoom,
				fmt.Sprintf("Zone %q has no Room %q", e.ToZone, e.ToRoom), withDir...)
			return nil
		}
	} else if !hasRoom(z, e.ToRoom) {
		r.report(zd.file, e.RefPos, CodeUnknownRoom,
			fmt.Sprintf("Zone %q has no Room %q", z.ID, e.ToRoom), withDir...)
		return nil
	}

	r.warnReverse(zd, rd, e, byID, targetZone)
	return &contentv1.ExitDefinition{Direction: e.Direction, ToZone: e.ToZone, ToRoom: e.ToRoom}
}

// checkSenses validates a perceives clause against the server's closed sense
// vocabulary. PENDING AW-SRV-029, which defines the registry; until it lands
// the permitted set is the two semantics.md §9 names.
func (r *resolver) checkSenses(file string, e *ExitDecl, chain []string) {
	for _, s := range e.Perceives {
		if !knownSense(s.Name) {
			r.report(file, s.Pos, CodeUnknownSense,
				fmt.Sprintf("no sense %q; senses are server-defined and the permitted ones are %s", s.Name, sensesList()),
				chain...)
		}
	}
}

// warnReverse emits missing_reverse_exit, naming both Rooms. Legal, because a
// chute or a trapdoor is a real thing, and never silent, because far more often
// it is a Builder forgetting the way back (semantics.md §3).
func (r *resolver) warnReverse(zd zoneDecl, rd *RoomDecl, e *ExitDecl, byID map[string]zoneDecl, targetZone string) {
	rev, ok := sim.Direction(e.Direction).Reverse()
	if !ok {
		return
	}
	tz, known := byID[targetZone]
	if !known {
		return
	}
	var target *RoomDecl
	for _, cand := range tz.d.Rooms {
		if cand.ID == e.ToRoom {
			target = cand
			break
		}
	}
	if target == nil {
		return
	}
	for _, back := range target.Exits {
		if back.Direction != string(rev) {
			continue
		}
		backZone := tz.d.ID
		if back.ToZone != "" {
			backZone = back.ToZone
		}
		if backZone == zd.d.ID && back.ToRoom == rd.ID {
			return
		}
	}
	r.warn(zd.file, e.Pos, CodeMissingReverseExit,
		fmt.Sprintf("Room %q exits %s to %s, and %s has no %s Exit back; a one-way Exit is legal, and more often it is a forgotten return",
			rd.ID, e.Direction, qualify(targetZone, e.ToRoom), qualify(targetZone, e.ToRoom), rev),
		zd.d.ID, rd.ID, e.Direction)
}

// warnOrphans emits orphan_room for a Room that no intra-Zone Exit connects to
// any other Room in its Zone. A Builder mid-work (AW-SRV-001 AC-6): legal,
// never silent.
//
// The test is an *incident* Exit, in either direction, not an inbound one.
// corpus/valid/warn-missing-reverse-exit/ is the case that fixes this: `loft`
// has a one-way chute down to `cellar` and nothing reaches `loft`, and the
// sidecar carries missing_reverse_exit and no orphan_room. A Room a Builder can
// walk out of is connected to the Zone; the warning is for the Room that is
// joined to nothing.
//
// A Zone with a single Room has no orphan either — AC-6 scopes the finding to a
// Room unreachable from "any other Room in that Zone", and where there is no
// other Room the question is vacuous.
//
// This is wider than the loader's rule, which counts inbound Exits only
// (sim.BuildWorld). See docs/feedback/AW-CLI-006-content-language-compiler.md §6.
func (r *resolver) warnOrphans(zd zoneDecl, rooms []*RoomDecl) {
	if len(rooms) < 2 {
		return
	}
	joined := map[string]bool{}
	for _, rd := range rooms {
		for _, e := range rd.Exits {
			if e.ToZone != "" && e.ToZone != zd.d.ID {
				continue // leaving the Zone does not join it to this Room
			}
			if e.ToRoom == rd.ID {
				continue // a self-loop leaves the Room as joined as it was
			}
			joined[rd.ID] = true
			joined[e.ToRoom] = true
		}
	}
	for _, rd := range rooms {
		if !joined[rd.ID] {
			r.warn(zd.file, rd.Pos, CodeOrphanRoom,
				fmt.Sprintf("no Exit joins Room %q to any other Room in Zone %q", rd.ID, zd.d.ID), zd.d.ID, rd.ID)
		}
	}
}

func hasRoom(z *ZoneDecl, id string) bool {
	for _, rd := range z.Rooms {
		if rd.ID == id {
			return true
		}
	}
	return false
}

func exitTarget(e *ExitDecl) string { return qualify(e.ToZone, e.ToRoom) }

func qualify(zone, room string) string {
	if zone == "" {
		return room
	}
	return zone + "." + room
}

// senses is the vocabulary semantics.md §9 names, pending AW-SRV-029's
// registry. Kept here rather than in sim because the field it validates does
// not exist yet: a construct with no protobuf field is a schema change first,
// and putting the vocabulary in sim would imply the loader enforces it.
var senses = []string{"sight", "sound"}

func knownSense(s string) bool {
	for _, k := range senses {
		if k == s {
			return true
		}
	}
	return false
}

func sensesList() string {
	out := ""
	for i, s := range senses {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
