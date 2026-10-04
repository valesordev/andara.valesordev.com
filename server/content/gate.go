// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/valesordev/andara/server/sim"
)

// The publish gate's World and what it reports (errors.md §1 rule 10, #312).
//
// The gate builds one World from every active pack's blobs and the
// publisher's, and refuses on any error in it. What it reports is narrower
// than what it refuses: the Builder who publishes can fix their own pack, so
// the report is theirs, plus what their publish newly causes in a pack that
// is already active.

// origin is where an Input came from. sim.Input.File is the path alone, and
// two packs can each hold a `town.json`, so the gate tags each input's File
// with its pack for the build and reads the tag back out of what the build
// reports (rule 10.2). The tag holds a NUL, which no pack name or blob path
// can (UnsafeBlobPath).
type origin struct{ pack, path string }

func tagFile(pack, path string) string { return pack + "\x00" + path }

// taggedInputs is the build's inputs for packs, in the order given, with each
// File tagged and the tag's origin recorded.
func taggedInputs(packs []*Resolved) ([]sim.Input, []sim.TemplateInput, map[string]origin) {
	var (
		zones     []sim.Input
		templates []sim.TemplateInput
	)
	origins := map[string]origin{}
	for _, p := range packs {
		for _, z := range p.Zones {
			tag := tagFile(p.Pack, z.File)
			origins[tag] = origin{pack: p.Pack, path: z.File}
			z.File = tag
			zones = append(zones, z)
		}
		for _, t := range p.Templates {
			tag := tagFile(p.Pack, t.File)
			origins[tag] = origin{pack: p.Pack, path: t.File}
			t.File = tag
			templates = append(templates, t)
		}
	}
	return zones, templates, origins
}

// sortedPacks is packs by name.
func sortedPacks(packs map[string]*Resolved) []*Resolved {
	names := make([]string, 0, len(packs))
	for p := range packs {
		names = append(names, p)
	}
	sort.Strings(names)
	out := make([]*Resolved, 0, len(names))
	for _, p := range names {
		out = append(out, packs[p])
	}
	return out
}

// untag replaces every tagged file name in a finding's text with its path.
func untag(detail string, origins map[string]origin) string {
	if !strings.Contains(detail, "\x00") {
		return detail
	}
	for tag, o := range origins {
		detail = strings.ReplaceAll(detail, tag, o.path)
	}
	return detail
}

// attributed is f with its File and Detail untagged, and the pack its File
// belongs to. A finding with no File (an empty World, a removed Zone) is
// about the publish and belongs to the publisher.
func attributed(f sim.ValidationError, origins map[string]origin, publisher string) (sim.ValidationError, string) {
	pack := publisher
	if o, ok := origins[f.File]; ok {
		pack, f.File = o.pack, o.path
	}
	f.Detail = untag(f.Detail, origins)
	return f, pack
}

// gate is one publish's report.
type gate struct {
	cand *Resolved
	// clash is each Zone id the publisher declares that an active pack
	// already does, with the first active pack that holds it. The publisher's
	// copy is the one the build drops, because the publisher is last (rule
	// 10.1).
	clash map[string]*Resolved
}

func newGate(incumbents []*Resolved, cand *Resolved) *gate {
	held := map[string]*Resolved{}
	for _, p := range incumbents {
		for _, z := range p.Zones {
			if _, ok := held[z.Def.GetId()]; !ok {
				held[z.Def.GetId()] = p
			}
		}
	}
	g := &gate{cand: cand, clash: map[string]*Resolved{}}
	for _, z := range cand.Zones {
		if h, ok := held[z.Def.GetId()]; ok {
			g.clash[z.Def.GetId()] = h
		}
	}
	return g
}

// errors is what the gate reports of the build's errors (rule 10.3 to 10.6).
func (g *gate) errors(raw []sim.ValidationError, origins map[string]origin) []sim.ValidationError {
	var out []sim.ValidationError
	reported := map[sim.ZoneID]bool{}
	for _, r := range raw {
		f, pack := attributed(r, origins, g.cand.Pack)
		if pack != g.cand.Pack {
			// Another pack's, whatever its cause (rule 10.6).
			f.Pack, f.Line = pack, 0
			out = append(out, f)
			continue
		}
		switch f.Code {
		case sim.ErrDuplicateZone:
			if h, ok := g.clash[string(f.Zone)]; ok {
				// The root, once (rule 10.4), worded for both packs (10.5).
				if reported[f.Zone] {
					continue
				}
				reported[f.Zone] = true
				f.Detail = fmt.Sprintf("ZoneID %s declared in pack %s and in active pack %s@%d", f.Zone, g.cand.Pack, h.Pack, h.Version)
			}
		case sim.ErrDuplicateRoom:
			if _, ok := g.clash[string(f.Zone)]; ok {
				continue // a Room of the dropped Zone
			}
		case sim.ErrUnknownRoom:
			if g.explained(f) {
				continue
			}
		}
		out = append(out, f)
	}
	if len(out) == 0 && len(raw) > 0 {
		// A refusal never has zero findings (rule 10.6). The root of a clash
		// always stays, so this is only a guard: report what the build said.
		for _, r := range raw {
			f, pack := attributed(r, origins, g.cand.Pack)
			if pack != g.cand.Pack {
				f.Pack, f.Line = pack, 0
			}
			out = append(out, f)
		}
	}
	return out
}

// explained reports whether an unknown_room exists only because the
// publisher's Zone was dropped: an Exit, in any of the publisher's Zones,
// into the dropped Zone's id whose target Room the dropped Zone declares (rule
// 10.4). The finding names the Exit's source, and its target is in the
// message alone, so the target is read from the publisher's own definitions.
func (g *gate) explained(f sim.ValidationError) bool {
	if len(g.clash) == 0 {
		return false
	}
	for _, z := range g.cand.Zones {
		d := z.Def
		if d.GetId() != string(f.Zone) {
			continue
		}
		for _, r := range d.GetRooms() {
			if r.GetId() != string(f.Room) {
				continue
			}
			for _, e := range r.GetExits() {
				if sim.Direction(e.GetDirection()) != f.Exit {
					continue
				}
				toZone := e.GetToZone()
				if toZone == "" {
					toZone = d.GetId()
				}
				if _, dropped := g.clash[toZone]; dropped && g.declares(toZone, e.GetToRoom()) {
					return true
				}
			}
		}
	}
	return false
}

// declares reports whether the publisher's Zone declares the Room.
func (g *gate) declares(zone, room string) bool {
	for _, z := range g.cand.Zones {
		if z.Def.GetId() != zone {
			continue
		}
		for _, r := range z.Def.GetRooms() {
			if r.GetId() == room {
				return true
			}
		}
	}
	return false
}

// warningKey names a warning for the comparison of rule 10.3: the pack, the
// file, the code and the Zone, Room and Exit it names. The sim's Chain is
// empty for a reverse-exit warning, so it can't be the key.
func warningKey(f sim.ValidationError, pack string) string {
	return strings.Join([]string{pack, f.File, string(f.Code), string(f.Zone), string(f.Room), string(f.Exit)}, "\x00")
}

// warnings is what the gate reports of the build's warnings: the publisher's
// own, and on a publish that succeeds, another pack's that this publish newly
// causes (rule 10.3). inEffect builds the warnings of the World in effect.
func (g *gate) warnings(raw []sim.ValidationError, origins map[string]origin, accepted bool, inEffect func() []sim.ValidationError) []sim.ValidationError {
	var own, foreign []sim.ValidationError
	for _, r := range raw {
		f, pack := attributed(r, origins, g.cand.Pack)
		if pack == g.cand.Pack {
			own = append(own, f)
			continue
		}
		f.Pack, f.Line = pack, 0
		foreign = append(foreign, f)
	}
	if !accepted || len(foreign) == 0 {
		return own
	}
	before := map[string]bool{}
	for _, w := range inEffect() {
		before[warningKey(w, w.Pack)] = true
	}
	for _, f := range foreign {
		if !before[warningKey(f, f.Pack)] {
			own = append(own, f)
		}
	}
	return own
}

// buildGate is the publish gate's build (AW-SRV-013 AC-1): the World the
// publisher's version would make with the packs in effect, judged as rule 10
// says. serving is the World in effect, the publisher's own current version
// included, which is what the new warnings are compared with.
func (l *Loader) buildGate(ctx context.Context, serving map[string]*Resolved, cand *Resolved) (sim.Topology, []sim.ValidationError, []sim.ValidationError) {
	base := make(map[string]*Resolved, len(serving))
	for p, r := range serving {
		if p != cand.Pack {
			base[p] = r
		}
	}
	incumbents := sortedPacks(base)
	// Incumbents first, then the publisher: the Zone the build drops as a
	// duplicate is the publisher's, never one that is already active.
	order := append(append([]*Resolved{}, incumbents...), cand)
	zones, templates, origins := taggedInputs(order)

	// The empty-content transition, as build judges it.
	if len(zones) == 0 && countZones(base) > 0 {
		return sim.Topology{}, []sim.ValidationError{{
			Code: sim.ErrEmptyContent,
			Detail: fmt.Sprintf("%s@%d would leave the World with no Zones; the previous version keeps serving",
				cand.Pack, cand.Version),
		}}, nil
	}

	_, span := l.tracer.Start(ctx, "content.build")
	defer span.End()
	bstart := time.Now()
	defer func() { l.metrics.LoadDuration.WithLabelValues(PhaseBuild).Observe(time.Since(bstart).Seconds()) }()

	built, rawErrs, rawWarns := buildContent(zones, templates, l.strictOrphans)
	g := newGate(incumbents, cand)
	refusing := g.errors(rawErrs, origins)
	warnings := g.warnings(rawWarns, origins, len(refusing) == 0, func() []sim.ValidationError {
		ez, et, eo := taggedInputs(sortedPacks(serving))
		_, _, ew := buildContent(ez, et, l.strictOrphans)
		out := make([]sim.ValidationError, 0, len(ew))
		for _, w := range ew {
			f, pack := attributed(w, eo, cand.Pack)
			f.Pack = pack
			out = append(out, f)
		}
		return out
	})
	return built, refusing, warnings
}
