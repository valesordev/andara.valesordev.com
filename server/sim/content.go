// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// Content in effect (AW-SRV-012). The log is its source: every version a World
// serves enters through a ContentSwap Command, the first included, and replay
// reads which content each tick ran on rather than re-deriving it from the
// Active Pointers (ADR-0002 §4 applied to content). The Engine holds the
// versions in effect and the digest of the topology they build; a swap is
// prepared outside the core, checked against the digest the log recorded, and
// applied after every other record of its tick.

// Topology is the immutable content a World runs on: its Zones and Rooms, and
// the Templates it instantiates from.
type Topology struct {
	World     *World
	Templates *TemplateRegistry
}

// ContentDigest is ContentSwap.world_digest: SHA-256 over the whole World's
// content topology, every Zone (CanonicalBytes) and then every Template
// (TemplatesCanonicalBytes), each in canonical order. It covers topology and
// nothing mutable, which the State Hash covers. It is whole-World rather than
// per-pack so a divergence in any pack's content is caught, and so the second
// of two swaps in one tick digests a World that includes the first.
func ContentDigest(t Topology) [32]byte {
	h := sha256.New()
	h.Write(CanonicalBytes(t.World))
	h.Write(TemplatesCanonicalBytes(t.Templates))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// TemplatesCanonicalBytes serializes a registry the way CanonicalBytes
// serializes Zones: injective, sorted by TemplateRef, tagged records. A
// Template's File and Source are left out: they say where a blob was read
// from and where the Builder wrote the declaration, which is provenance and
// not content, and a directory mounted at another path must not read as
// different content.
func TemplatesCanonicalBytes(r *TemplateRegistry) []byte {
	if r == nil || r.Len() == 0 {
		return nil
	}
	refs := make([]string, 0, len(r.templates))
	for ref := range r.templates {
		refs = append(refs, string(ref))
	}
	sort.Strings(refs)
	var b strings.Builder
	for _, ref := range refs {
		t := r.templates[TemplateRef(ref)]
		chain := make([]string, len(t.Chain))
		for i, c := range t.Chain {
			chain[i] = string(c)
		}
		writeFields(&b, "template", ref, string(t.Kind), strings.Join(chain, ">"))
		writeComponents(&b, "template_component", "template_field", []string{ref}, t.Components)
		for _, p := range t.Provenance {
			writeFields(&b, "template_provenance", ref, string(p.Component), p.Field, string(p.From))
		}
	}
	return []byte(b.String())
}

// EmptyWorld is the topology a World has before its first ContentSwap: no
// Zones, nowhere to stand. Recovery starts here and builds content only from
// the swaps it replays.
func EmptyWorld() *World { return &World{Zones: map[ZoneID]*Zone{}} }

// ContentSource prepares the Topology a ContentSwap produces, given the pack
// versions in effect before it. It is the seam that keeps the core free of
// I/O: live, the Loader has already built the topology off-tick and returns
// it; on replay it resolves the recorded version from the store. The Engine
// checks the digest itself, so a source that builds the wrong World is caught
// rather than trusted.
type ContentSource interface {
	Prepare(inEffect map[string]uint64, swap *logv1.ContentSwap) (Topology, error)
}

// ErrContentDigest: a ContentSwap's recorded world_digest does not match the
// topology its version builds now. The content is not the content that was
// running; halt rather than serve a World built from it, as a State Hash
// mismatch does.
var ErrContentDigest = errors.New("content digest mismatch")

// ErrNoContentSource: the log carries a ContentSwap and this Engine was built
// with no way to prepare one.
var ErrNoContentSource = errors.New("a content swap needs a content source")

// ContentDigestError is ErrContentDigest with what disagreed.
type ContentDigestError struct {
	Tick     Tick
	Pack     string
	Version  uint64
	Recorded []byte
	Built    [32]byte
}

func (e *ContentDigestError) Error() string {
	return fmt.Sprintf("%v at tick %d: %s@%d recorded %x, built %x", ErrContentDigest, e.Tick, e.Pack, e.Version, head(e.Recorded), e.Built[:8])
}

// Unwrap makes errors.Is(err, ErrContentDigest) hold.
func (e *ContentDigestError) Unwrap() error { return ErrContentDigest }

func head(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// ReasonRoomRemoved is EntityRelocated.reason for a Room a new content version
// removed, the only reason in v1.
const ReasonRoomRemoved = "room_removed"

// Relocation is one Entity moved to its Zone's fallback Room by a swap.
type Relocation struct {
	Zone     ZoneID
	Entity   EntityID
	From, To RoomID
	// Dormant: the body was not in the World. It moves so "where you were" is
	// a Room that exists, and no Event names it, since a dormant body is
	// addressed by none.
	Dormant bool
}

// SwapApplied is one ContentSwap as its tick applied it.
type SwapApplied struct {
	Pack        string
	Version     uint64
	Digest      [32]byte
	Relocations []Relocation
	// Stranded are Zones the new content no longer has while Entities stand in
	// them. Their state is kept as it is and their Commands are refused
	// unknown_zone until content brings the Zone back; nothing is deleted by a
	// swap.
	Stranded []ZoneID
}

// preparedSwap is a swap whose topology is built and whose digest is checked,
// waiting for the end of its tick.
type preparedSwap struct {
	rec      Record
	swap     *logv1.ContentSwap
	topo     Topology
	digest   [32]byte
	versions map[string]uint64
}

// prepareSwaps builds every swap of a tick in offset order, each against the
// versions the one before it leaves, and checks each digest, before the tick
// mutates anything: a swap that cannot be applied refuses the whole Step and
// leaves the state untouched.
func (e *Engine) prepareSwaps(tick Tick, swaps []Record) ([]preparedSwap, error) {
	if len(swaps) == 0 {
		return nil, nil
	}
	if e.cfg.Content == nil {
		return nil, fmt.Errorf("tick %d: %w", tick, ErrNoContentSource)
	}
	versions := copyVersions(e.versions)
	out := make([]preparedSwap, 0, len(swaps))
	for _, r := range swaps {
		cs := r.Command.GetContentSwap()
		topo, err := e.cfg.Content.Prepare(copyVersions(versions), cs)
		if err != nil {
			return nil, fmt.Errorf("tick %d: prepare %s@%d: %w", tick, cs.GetPackId(), cs.GetVersion(), err)
		}
		if topo.World == nil {
			topo.World = EmptyWorld()
		}
		digest := ContentDigest(topo)
		if string(digest[:]) != string(cs.GetWorldDigest()) {
			return nil, &ContentDigestError{Tick: tick, Pack: cs.GetPackId(), Version: cs.GetVersion(), Recorded: cs.GetWorldDigest(), Built: digest}
		}
		versions[cs.GetPackId()] = cs.GetVersion()
		out = append(out, preparedSwap{rec: r, swap: cs, topo: topo, digest: digest, versions: copyVersions(versions)})
	}
	return out, nil
}

// applySwap moves the World onto a prepared topology: Zones the content adds
// get empty state, Entities in a Room it removed move to their Zone's fallback
// with an EntityRelocated, and the versions in effect and the digest advance.
// Deterministic: Zones and Entities are visited in ID order.
func (e *Engine) applySwap(p preparedSwap, emit func(ZoneID, string, string, Scope, *gamev1.EventEnvelope)) SwapApplied {
	s := e.state
	out := SwapApplied{Pack: p.swap.GetPackId(), Version: p.swap.GetVersion(), Digest: p.digest}
	w := p.topo.World

	zids := make([]string, 0, len(s.Zones))
	for id := range s.Zones {
		zids = append(zids, string(id))
	}
	sort.Strings(zids)
	for _, zid := range zids {
		zs := s.Zones[ZoneID(zid)]
		nz, kept := w.Zones[zs.ID]
		if !kept {
			if len(zs.Entities) > 0 {
				out.Stranded = append(out.Stranded, zs.ID)
			}
			continue
		}
		eids := make([]string, 0, len(zs.Entities))
		for id := range zs.Entities {
			eids = append(eids, string(id))
		}
		sort.Strings(eids)
		for _, eid := range eids {
			ent := zs.Entities[EntityID(eid)]
			if ent.Room == "" {
				continue
			}
			if _, ok := nz.Rooms[ent.Room]; ok {
				continue
			}
			rel := Relocation{Zone: zs.ID, Entity: ent.ID, From: ent.Room, To: nz.Fallback, Dormant: ent.Dormant}
			ent.Room = nz.Fallback
			out.Relocations = append(out.Relocations, rel)
			if rel.Dormant {
				continue
			}
			emit(zs.ID, "", "", ScopeRoom(zs.ID, nz.Fallback).With(ent.ID), &gamev1.EventEnvelope{
				Payload: &gamev1.EventEnvelope_EntityRelocated{EntityRelocated: &gamev1.EntityRelocated{
					ZoneId: string(zs.ID), EntityName: ent.DisplayName(),
					FromRoomId: string(rel.From), ToRoomId: string(rel.To), Reason: ReasonRoomRemoved,
				}},
			})
		}
	}
	for id := range w.Zones {
		if _, ok := s.Zones[id]; !ok {
			s.Zones[id] = &ZoneState{ID: id, Entities: map[EntityID]*EntityState{}}
		}
	}
	e.world, e.templates = w, p.topo.Templates
	e.versions, e.digest = p.versions, p.digest
	return out
}

// PrepareContent builds the topology a set of versions in effect produces,
// through src: the last pack by name prepared as a swap on top of the rest.
// How a snapshot round's recorded content is rebuilt (RestoreEngine).
func PrepareContent(src ContentSource, versions map[string]uint64) (Topology, error) {
	if len(versions) == 0 {
		return Topology{World: EmptyWorld()}, nil
	}
	if src == nil {
		return Topology{}, ErrNoContentSource
	}
	packs := make([]string, 0, len(versions))
	for p := range versions {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	last := packs[len(packs)-1]
	rest := copyVersions(versions)
	delete(rest, last)
	topo, err := src.Prepare(rest, &logv1.ContentSwap{PackId: last, Version: versions[last]})
	if err != nil {
		return Topology{}, err
	}
	if topo.World == nil {
		topo.World = EmptyWorld()
	}
	return topo, nil
}

// Content reports the pack versions in effect and their digest; an empty map
// and a zero digest before the first swap.
func (e *Engine) Content() (map[string]uint64, [32]byte) {
	return copyVersions(e.versions), e.digest
}

func copyVersions(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
