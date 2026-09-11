// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"fmt"
	"sort"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// Input is a ZoneDefinition plus the provenance the validator needs to name
// files in findings. File is empty when the caller has no path (Kafka later).
type Input struct {
	File string
	Def  *contentv1.ZoneDefinition
	// Pos carries source line numbers, when the adapter that produced Def
	// could recover them. Nil is legal and costs only the line in a finding.
	Pos *Positions
}

// Positions is the source line of each part of a Zone Definition a finding can
// name. It is plain data with no reference to a file format, which is what lets
// the sim core stay dependency-free while still reporting a line: protojson has
// discarded positions by the time a ZoneDefinition exists, so the adapter that
// read the bytes is the only thing that can supply them.
//
// Indices are into the corresponding repeated field of the ZoneDefinition. A
// short or nil slice yields line 0, which reads as "not line-scoped" — the same
// as every finding AW-SRV-001 emitted.
type Positions struct {
	// ZoneComponents is indexed by ZoneDefinition.components.
	ZoneComponents []int
	// Rooms is indexed by ZoneDefinition.rooms.
	Rooms []RoomPositions
}

// RoomPositions is the source line of a Room and of the parts within it.
type RoomPositions struct {
	Line int
	// Exits is indexed by RoomDefinition.exits.
	Exits []int
	// Components is indexed by RoomDefinition.components.
	Components []int
}

func lineAt(lines []int, i int) int {
	if i < 0 || i >= len(lines) {
		return 0
	}
	return lines[i]
}

func (in Input) roomPos(ri int) RoomPositions {
	if in.Pos == nil || ri < 0 || ri >= len(in.Pos.Rooms) {
		return RoomPositions{}
	}
	return in.Pos.Rooms[ri]
}

// zoneComponentLine returns the source line of ZoneDefinition.components[ci].
func (in Input) zoneComponentLine(ci int) int {
	if in.Pos == nil {
		return 0
	}
	return lineAt(in.Pos.ZoneComponents, ci)
}

// roomLine returns the source line of ZoneDefinition.rooms[ri].
func (in Input) roomLine(ri int) int { return in.roomPos(ri).Line }

// exitLine returns the source line of rooms[ri].exits[ei].
func (in Input) exitLine(ri, ei int) int { return lineAt(in.roomPos(ri).Exits, ei) }

// roomComponentLine returns the source line of rooms[ri].components[ci].
func (in Input) roomComponentLine(ri, ci int) int {
	return lineAt(in.roomPos(ri).Components, ci)
}

// Options controls BuildWorld policy that callers share (CLI, publish, boot).
type Options struct {
	// StrictOrphans treats inbound-unreachable Rooms as fatal instead of warnings.
	StrictOrphans bool
	// Source is named in ErrEmptyContent (e.g. "dir:./content" or "kafka").
	Source string
}

// BuildWorld validates defs and constructs an immutable World topology.
// All findings are returned; the first error is never the only error.
// World is non-nil only when there are no fatal findings. Orphans are
// warnings (code orphan_room) unless Options.StrictOrphans is set.
func BuildWorld(inputs []Input, opts Options) (*World, []ValidationError) {
	var errs []ValidationError
	if len(inputs) == 0 {
		src := opts.Source
		if src == "" {
			src = "empty input"
		}
		return nil, []ValidationError{{
			Code:   ErrEmptyContent,
			Detail: "no Zones were found in " + src,
		}}
	}

	type exitAcc struct {
		dir    Direction
		toZone ZoneID
		toRoom RoomID
		line   int
	}
	type roomAcc struct {
		file       string
		id         RoomID
		title      string
		desc       string
		exits      []exitAcc
		components []Component
	}
	type zoneAcc struct {
		file       string
		name       string
		rooms      map[RoomID]*roomAcc
		components []Component
	}

	byID := make(map[ZoneID]*zoneAcc, len(inputs))
	order := make([]ZoneID, 0, len(inputs))

	for _, in := range inputs {
		if in.Def == nil {
			errs = append(errs, ValidationError{
				File:   in.File,
				Code:   ErrMalformed,
				Detail: "zone definition is empty",
			})
			continue
		}
		if in.Def.FormatVersion < MinFormatVersion || in.Def.FormatVersion > MaxFormatVersion {
			errs = append(errs, ValidationError{
				File: in.File,
				Zone: ZoneID(in.Def.Id),
				Code: ErrUnsupportedVersion,
				Detail: fmt.Sprintf("format_version %d is outside supported range %d-%d",
					in.Def.FormatVersion, MinFormatVersion, MaxFormatVersion),
			})
			continue
		}
		if in.Def.Id == "" {
			errs = append(errs, ValidationError{
				File:   in.File,
				Code:   ErrMalformed,
				Detail: "zone id is empty",
			})
			continue
		}
		zid := ZoneID(in.Def.Id)
		if existing, ok := byID[zid]; ok {
			errs = append(errs, ValidationError{
				File: in.File,
				Zone: zid,
				Code: ErrDuplicateZone,
				Detail: fmt.Sprintf("ZoneID %s declared in %s and %s",
					zid, existing.file, in.File),
			})
			// Still scan rooms so duplicate_room across the two files is reported.
			for _, rd := range in.Def.Rooms {
				if rd == nil || rd.Id == "" {
					continue
				}
				rid := RoomID(rd.Id)
				if prev, ok := existing.rooms[rid]; ok {
					errs = append(errs, ValidationError{
						File: in.File,
						Zone: zid,
						Room: rid,
						Code: ErrDuplicateRoom,
						Detail: fmt.Sprintf("RoomID %s declared in %s and %s",
							rid, prev.file, in.File),
					})
				}
			}
			continue
		}
		zoneComps, zoneCompErrs := validateComponents(in.Def.Components, componentSite{
			file: in.File,
			zone: zid,
			line: in.zoneComponentLine,
			what: "Zone " + string(zid),
		})
		errs = append(errs, zoneCompErrs...)
		acc := &zoneAcc{
			file:       in.File,
			name:       in.Def.Name,
			rooms:      make(map[RoomID]*roomAcc, len(in.Def.Rooms)),
			components: zoneComps,
		}
		for ri, rd := range in.Def.Rooms {
			if rd == nil {
				errs = append(errs, ValidationError{
					File:   in.File,
					Zone:   zid,
					Code:   ErrMalformed,
					Detail: "room definition is empty",
				})
				continue
			}
			if rd.Id == "" {
				errs = append(errs, ValidationError{
					File:   in.File,
					Zone:   zid,
					Code:   ErrMalformed,
					Detail: "room id is empty",
				})
				continue
			}
			rid := RoomID(rd.Id)
			if prev, ok := acc.rooms[rid]; ok {
				errs = append(errs, ValidationError{
					File: in.File,
					Line: in.roomLine(ri),
					Zone: zid,
					Room: rid,
					Code: ErrDuplicateRoom,
					Detail: fmt.Sprintf("RoomID %s declared in %s and %s",
						rid, prev.file, in.File),
				})
				continue
			}
			roomComps, roomCompErrs := validateComponents(rd.Components, componentSite{
				file: in.File,
				zone: zid,
				room: rid,
				line: func(ci int) int { return in.roomComponentLine(ri, ci) },
				what: "Room " + string(rid),
			})
			errs = append(errs, roomCompErrs...)
			ra := &roomAcc{
				file:       in.File,
				id:         rid,
				title:      rd.Title,
				desc:       rd.Description,
				components: roomComps,
			}
			seenDir := make(map[Direction]struct{}, len(rd.Exits))
			for ei, ed := range rd.Exits {
				exitLine := in.exitLine(ri, ei)
				if ed == nil {
					errs = append(errs, ValidationError{
						File:   in.File,
						Line:   exitLine,
						Zone:   zid,
						Room:   rid,
						Code:   ErrMalformed,
						Detail: "exit definition is empty",
					})
					continue
				}
				if ed.Direction == "" {
					errs = append(errs, ValidationError{
						File:   in.File,
						Line:   exitLine,
						Zone:   zid,
						Room:   rid,
						Code:   ErrMalformed,
						Detail: "exit direction is empty",
					})
					continue
				}
				dir := Direction(ed.Direction)
				// The closed Direction set, enforced here rather than as a
				// protobuf enum so a typo names its own file and line instead
				// of being dropped as an unknown member on the wire.
				if !dir.Valid() {
					errs = append(errs, ValidationError{
						File: in.File,
						Line: exitLine,
						Zone: zid,
						Room: rid,
						Code: ErrUnknownDirection,
						Detail: fmt.Sprintf("exit direction %q is not a Direction; the permitted set is %s",
							ed.Direction, DirectionsList()),
					})
					continue
				}
				if _, dup := seenDir[dir]; dup {
					errs = append(errs, ValidationError{
						File:   in.File,
						Line:   exitLine,
						Zone:   zid,
						Room:   rid,
						Code:   ErrMalformed,
						Detail: fmt.Sprintf("duplicate exit direction %q", dir),
					})
					continue
				}
				seenDir[dir] = struct{}{}
				toZone := ZoneID(ed.ToZone)
				if toZone == "" {
					toZone = zid
				}
				if ed.ToRoom == "" {
					errs = append(errs, ValidationError{
						File:   in.File,
						Line:   exitLine,
						Zone:   zid,
						Room:   rid,
						Code:   ErrMalformed,
						Detail: fmt.Sprintf("exit %s has empty target room", dir),
					})
					continue
				}
				ra.exits = append(ra.exits, exitAcc{
					dir:    dir,
					toZone: toZone,
					toRoom: RoomID(ed.ToRoom),
					line:   exitLine,
				})
			}
			acc.rooms[rid] = ra
		}
		byID[zid] = acc
		order = append(order, zid)
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	// Referential checks against the collected set.
	for _, zid := range order {
		z := byID[zid]
		rids := sortedRoomIDs(z.rooms)
		for _, rid := range rids {
			r := z.rooms[rid]
			for _, e := range r.exits {
				if _, ok := byID[e.toZone]; !ok {
					errs = append(errs, ValidationError{
						File: r.file,
						Zone: zid,
						Room: rid,
						Code: ErrUnknownZone,
						Detail: fmt.Sprintf("exit %s targets missing ZoneID %s",
							e.dir, e.toZone),
					})
					continue
				}
				if _, ok := byID[e.toZone].rooms[e.toRoom]; !ok {
					errs = append(errs, ValidationError{
						File: r.file,
						Zone: zid,
						Room: rid,
						Code: ErrUnknownRoom,
						Detail: fmt.Sprintf("exit %s targets unresolved room %s in zone %s",
							e.dir, e.toRoom, e.toZone),
					})
				}
			}
		}
	}

	// Reverse Exits: an Exit whose target Room has no Exit back along the
	// reverse Direction. One-way Exits are legal and useful — a chute, a
	// trapdoor — so this is a warning, never a refusal. It exists because the
	// far more common cause is a Builder forgetting the way back, and a Room
	// players fall into and cannot leave is not something to discover in
	// production. Cross-Zone Exits count: the reverse is looked up in whatever
	// Zone the Exit lands in.
	for _, zid := range order {
		z := byID[zid]
		rids := sortedRoomIDs(z.rooms)
		for _, rid := range rids {
			r := z.rooms[rid]
			for _, e := range r.exits {
				rev, ok := e.dir.Reverse()
				if !ok {
					continue // not a canonical Direction; already reported
				}
				tz, ok := byID[e.toZone]
				if !ok {
					continue // unresolved target; already reported
				}
				tr, ok := tz.rooms[e.toRoom]
				if !ok {
					continue // unresolved target; already reported
				}
				back := false
				for _, te := range tr.exits {
					if te.dir == rev && te.toZone == zid && te.toRoom == rid {
						back = true
						break
					}
				}
				if back {
					continue
				}
				errs = append(errs, ValidationError{
					File: r.file,
					Line: e.line,
					Zone: zid,
					Room: rid,
					Code: ErrMissingReverseExit,
					Detail: fmt.Sprintf(
						"exit %s from %s/%s reaches %s/%s, which has no %s Exit back",
						e.dir, zid, rid, e.toZone, e.toRoom, rev),
				})
			}
		}
	}

	// Orphans: no inbound intra-zone exit from another Room. Legal unless
	// StrictOrphans. A Room's own Exit back to itself is not an inbound edge —
	// AC-6 scopes reachability to "any other Room in that Zone", and a self-loop
	// leaves the Room as unreachable as it was.
	for _, zid := range order {
		z := byID[zid]
		inbound := make(map[RoomID]struct{}, len(z.rooms))
		for _, r := range z.rooms {
			for _, e := range r.exits {
				if e.toZone != zid || e.toRoom == r.id {
					continue
				}
				inbound[e.toRoom] = struct{}{}
			}
		}
		rids := sortedRoomIDs(z.rooms)
		for _, rid := range rids {
			if _, ok := inbound[rid]; ok {
				continue
			}
			r := z.rooms[rid]
			errs = append(errs, ValidationError{
				File:   r.file,
				Zone:   zid,
				Room:   rid,
				Code:   ErrOrphanRoom,
				Detail: fmt.Sprintf("Room %s is reachable by no Exit from any other Room in Zone %s", rid, zid),
			})
		}
	}

	fatal := false
	for _, e := range errs {
		if IsWarning(e, opts.StrictOrphans) {
			continue
		}
		fatal = true
		break
	}
	if fatal {
		return nil, errs
	}

	world := &World{Zones: make(map[ZoneID]*Zone, len(byID))}
	for _, zid := range order {
		z := byID[zid]
		zone := &Zone{
			ID:         zid,
			Name:       z.name,
			Rooms:      make(map[RoomID]*Room, len(z.rooms)),
			Partition:  PartitionFor(zid),
			Components: z.components,
		}
		rids := sortedRoomIDs(z.rooms)
		for _, rid := range rids {
			ra := z.rooms[rid]
			exits := make([]Exit, len(ra.exits))
			for i, e := range ra.exits {
				exits[i] = Exit{
					Direction: e.dir,
					To:        RoomRef{Zone: e.toZone, Room: e.toRoom},
					CrossZone: e.toZone != zid,
				}
			}
			sort.Slice(exits, func(i, j int) bool {
				return exits[i].Direction < exits[j].Direction
			})
			zone.Rooms[rid] = &Room{
				ID:          rid,
				Title:       ra.title,
				Description: ra.desc,
				Exits:       exits,
				Components:  ra.components,
			}
		}
		world.Zones[zid] = zone
	}
	return world, errs
}

// sortedRoomIDs returns a map's RoomIDs in a stable order. Findings are emitted
// in this order so that two loads of the same content report the same problems
// in the same sequence — a validator whose output depends on map iteration is
// unreadable in a diff and untestable in CI.
func sortedRoomIDs[T any](m map[RoomID]T) []RoomID {
	out := make([]RoomID, 0, len(m))
	for rid := range m {
		out = append(out, rid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// componentSite is the provenance a component finding needs. what names the
// carrier in prose — "Room plaza", "Zone town" — because a Builder reading the
// message should not have to infer which of the two levels they got wrong.
type componentSite struct {
	file string
	zone ZoneID
	room RoomID
	line func(i int) int
	what string
}

func (s componentSite) lineOf(i int) int {
	if s.line == nil {
		return 0
	}
	return s.line(i)
}

func (s componentSite) finding(i int, code ErrCode, detail string) ValidationError {
	return ValidationError{
		File:   s.file,
		Line:   s.lineOf(i),
		Zone:   s.zone,
		Room:   s.room,
		Code:   code,
		Detail: detail,
	}
}

// validateComponents checks a Component set against the server registry and
// returns it sorted by type, with its fields sorted by name.
//
// Sorting here rather than at serialization time is deliberate: the World is
// what every later reader sees, and a Component set that is only sorted on the
// way out is a set that arrives unsorted at the State Hash the first time
// someone adds a second serializer.
func validateComponents(defs []*contentv1.ComponentValue, site componentSite) ([]Component, []ValidationError) {
	if len(defs) == 0 {
		return nil, nil
	}
	var (
		errs  []ValidationError
		comps = make([]Component, 0, len(defs))
		seen  = make(map[ComponentType]struct{}, len(defs))
	)
	for i, cd := range defs {
		if cd == nil {
			errs = append(errs, site.finding(i, ErrMalformed,
				"component definition is empty on "+site.what))
			continue
		}
		ct := ComponentType(cd.Type)
		if ct == "" {
			errs = append(errs, site.finding(i, ErrMalformed,
				"component type is empty on "+site.what))
			continue
		}
		spec, known := componentRegistry[ct]
		if !known {
			// ADR-0010 decision 7 in the message, not just in the ADR: the
			// Builder cannot fix this by spelling it differently, and needs to
			// be told that before they spend an afternoon trying.
			errs = append(errs, site.finding(i, ErrUnknownComponent, fmt.Sprintf(
				"component type %q on %s is not registered on this server; "+
					"component types are defined on the server and composed by content "+
					"(ADR-0010 decision 7), so a new type is a server change, not a content fix",
				cd.Type, site.what)))
			continue
		}
		if _, dup := seen[ct]; dup {
			// ADR-0010 decision 3 keys components by type. Two of a thing has
			// no override semantics, and every answer to "which one wins" is
			// silent.
			errs = append(errs, site.finding(i, ErrDuplicateComponent, fmt.Sprintf(
				"component type %q is declared more than once on %s; a Component set is keyed by type "+
					"(ADR-0010 decision 3)", cd.Type, site.what)))
			continue
		}
		seen[ct] = struct{}{}

		fields, fieldErrs := validateComponentFields(cd, spec, site, i)
		errs = append(errs, fieldErrs...)
		if len(fieldErrs) > 0 {
			continue
		}
		comps = append(comps, Component{Type: ct, Fields: fields})
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i].Type < comps[j].Type })
	if len(comps) == 0 {
		return nil, errs
	}
	return comps, errs
}

// validateComponentFields checks one Component's fields against its registry
// declaration. An undeclared field is rejected rather than carried: a field no
// system reads would still be hashed into World state, which is a silent way to
// make two Worlds differ over something that means nothing.
func validateComponentFields(cd *contentv1.ComponentValue, spec componentSpec, site componentSite, i int) ([]ComponentField, []ValidationError) {
	if len(cd.Fields) == 0 {
		return nil, nil
	}
	var (
		errs   []ValidationError
		fields = make([]ComponentField, 0, len(cd.Fields))
		seen   = make(map[string]struct{}, len(cd.Fields))
	)
	for _, fd := range cd.Fields {
		if fd == nil || fd.Name == "" {
			errs = append(errs, site.finding(i, ErrInvalidComponentField,
				fmt.Sprintf("component %q on %s has a field with no name", cd.Type, site.what)))
			continue
		}
		want, declared := spec.fields[fd.Name]
		if !declared {
			errs = append(errs, site.finding(i, ErrInvalidComponentField, fmt.Sprintf(
				"component %q on %s has no field %q; component types are defined on the server "+
					"(ADR-0010 decision 7)", cd.Type, site.what, fd.Name)))
			continue
		}
		if _, dup := seen[fd.Name]; dup {
			errs = append(errs, site.finding(i, ErrInvalidComponentField, fmt.Sprintf(
				"component %q on %s declares field %q more than once", cd.Type, site.what, fd.Name)))
			continue
		}
		seen[fd.Name] = struct{}{}

		f, kind := fieldValue(fd)
		if kind != want {
			got := "a " + kind.String()
			if kind == FieldUnset {
				got = "no value"
			}
			errs = append(errs, site.finding(i, ErrInvalidComponentField, fmt.Sprintf(
				"component %q on %s sets field %q to %s; it is declared %s",
				cd.Type, site.what, fd.Name, got, want)))
			continue
		}
		fields = append(fields, f)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	if len(fields) == 0 {
		return nil, errs
	}
	return fields, errs
}

// fieldValue converts one wire field to its Go form and reports which arm of
// the oneof was set. An unset oneof yields FieldUnset, which matches no
// declaration and is therefore rejected — a named field with no value is a
// Builder mistake, not an empty string.
func fieldValue(fd *contentv1.ComponentField) (ComponentField, FieldKind) {
	f := ComponentField{Name: fd.Name}
	switch v := fd.Value.(type) {
	case *contentv1.ComponentField_StringValue:
		f.Kind, f.Str = FieldString, v.StringValue
	case *contentv1.ComponentField_IntValue:
		f.Kind, f.Int = FieldInt, v.IntValue
	case *contentv1.ComponentField_BoolValue:
		f.Kind, f.Bool = FieldBool, v.BoolValue
	default:
		f.Kind = FieldUnset
	}
	return f, f.Kind
}
