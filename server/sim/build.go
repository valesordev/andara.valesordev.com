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
	}
	type roomAcc struct {
		file  string
		id    RoomID
		title string
		desc  string
		exits []exitAcc
	}
	type zoneAcc struct {
		file  string
		name  string
		rooms map[RoomID]*roomAcc
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
		acc := &zoneAcc{
			file:  in.File,
			name:  in.Def.Name,
			rooms: make(map[RoomID]*roomAcc, len(in.Def.Rooms)),
		}
		for _, rd := range in.Def.Rooms {
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
					Zone: zid,
					Room: rid,
					Code: ErrDuplicateRoom,
					Detail: fmt.Sprintf("RoomID %s declared in %s and %s",
						rid, prev.file, in.File),
				})
				continue
			}
			ra := &roomAcc{
				file:  in.File,
				id:    rid,
				title: rd.Title,
				desc:  rd.Description,
			}
			seenDir := make(map[Direction]struct{}, len(rd.Exits))
			for _, ed := range rd.Exits {
				if ed == nil {
					errs = append(errs, ValidationError{
						File:   in.File,
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
						Zone:   zid,
						Room:   rid,
						Code:   ErrMalformed,
						Detail: "exit direction is empty",
					})
					continue
				}
				dir := Direction(ed.Direction)
				if _, dup := seenDir[dir]; dup {
					errs = append(errs, ValidationError{
						File:   in.File,
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
		rids := make([]RoomID, 0, len(z.rooms))
		for rid := range z.rooms {
			rids = append(rids, rid)
		}
		sort.Slice(rids, func(i, j int) bool { return rids[i] < rids[j] })
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

	// Orphans: no inbound intra-zone exit. Legal unless StrictOrphans.
	for _, zid := range order {
		z := byID[zid]
		inbound := make(map[RoomID]struct{}, len(z.rooms))
		for _, r := range z.rooms {
			for _, e := range r.exits {
				if e.toZone == zid {
					inbound[e.toRoom] = struct{}{}
				}
			}
		}
		rids := make([]RoomID, 0, len(z.rooms))
		for rid := range z.rooms {
			rids = append(rids, rid)
		}
		sort.Slice(rids, func(i, j int) bool { return rids[i] < rids[j] })
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
		if e.Code == ErrOrphanRoom && !opts.StrictOrphans {
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
			ID:        zid,
			Name:      z.name,
			Rooms:     make(map[RoomID]*Room, len(z.rooms)),
			Partition: PartitionFor(zid),
		}
		rids := make([]RoomID, 0, len(z.rooms))
		for rid := range z.rooms {
			rids = append(rids, rid)
		}
		sort.Slice(rids, func(i, j int) bool { return rids[i] < rids[j] })
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
			}
		}
		world.Zones[zid] = zone
	}
	return world, errs
}
