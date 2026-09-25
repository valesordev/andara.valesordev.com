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
}

// Why a ContentSwap was refused: a deterministic no-op that consumes its
// record and changes nothing, live and on replay alike (log.proto, review of
// #88). The Loader is told, and re-evaluates.
const (
	// SwapStaleBase: base_digest is not the content in effect. The swap was
	// built on a World the log has since moved past.
	SwapStaleBase = "stale_base"
	// SwapZoneRemoved: the swap's World lacks a Zone the content in effect
	// has. The Loader refuses such a version; this is the Engine's backstop.
	SwapZoneRemoved = "zone_removed"
	// SwapFallbackMissing: a Zone of the swap's World names no fallback Room
	// of its own. BuildWorld refuses such content; the Engine does not trust
	// a source to have used it.
	SwapFallbackMissing = "fallback_missing"
	// SwapMisrouted: a ContentSwap not on WorldPartition, or with a zone_id.
	SwapMisrouted = "misrouted"
)

// SwapRefused is one ContentSwap its tick refused.
type SwapRefused struct {
	Pack    string
	Version uint64
	Reason  string
	// Detail names what disagreed, for the log.
	Detail string
}

// preparedSwap is a swap whose topology is built and whose digest is checked,
// waiting for the end of its tick — or, with refused set, a swap its tick
// consumes and refuses.
type preparedSwap struct {
	rec      Record
	swap     *logv1.ContentSwap
	topo     Topology
	digest   [32]byte
	versions map[string]uint64
	refused  *SwapRefused
}

// prepareSwaps decides every swap of a tick in (Partition, offset) order, each
// against the content the one before it leaves, before the tick mutates
// anything.
//
// A swap is refused — a deterministic no-op, the same live and on replay —
// when it is misrouted, when its base_digest is not the content in effect
// (stale: built on a World the log has moved past), when its World removes a
// Zone the content in effect has, or when a Zone of it has no fallback of its
// own. Only a swap on the right base whose world_digest its version no longer
// builds is an error, and that refuses the whole Step, as a State Hash
// mismatch does: the content itself is not the content that was running.
func (e *Engine) prepareSwaps(tick Tick, swaps []Record) ([]preparedSwap, error) {
	if len(swaps) == 0 {
		return nil, nil
	}
	versions, digest, world := copyVersions(e.versions), e.digest, e.world
	out := make([]preparedSwap, 0, len(swaps))
	for _, r := range swaps {
		cs := r.Command.GetContentSwap()
		refuse := func(reason, detail string) {
			out = append(out, preparedSwap{rec: r, swap: cs, refused: &SwapRefused{Pack: cs.GetPackId(), Version: cs.GetVersion(), Reason: reason, Detail: detail}})
		}
		if r.Partition != WorldPartition || r.Command.GetZoneId() != "" {
			refuse(SwapMisrouted, fmt.Sprintf("on partition %d with zone_id %q", r.Partition, r.Command.GetZoneId()))
			continue
		}
		base := cs.GetBaseDigest()
		if (len(versions) == 0 && len(base) != 0) || (len(versions) > 0 && string(base) != string(digest[:])) {
			refuse(SwapStaleBase, fmt.Sprintf("built on %x, in effect %x", head(base), digest[:8]))
			continue
		}
		if e.cfg.Content == nil {
			return nil, fmt.Errorf("tick %d: %w", tick, ErrNoContentSource)
		}
		topo, err := e.cfg.Content.Prepare(copyVersions(versions), cs)
		if err != nil {
			return nil, fmt.Errorf("tick %d: prepare %s@%d: %w", tick, cs.GetPackId(), cs.GetVersion(), err)
		}
		if topo.World == nil {
			topo.World = EmptyWorld()
		}
		built := ContentDigest(topo)
		if string(built[:]) != string(cs.GetWorldDigest()) {
			return nil, &ContentDigestError{Tick: tick, Pack: cs.GetPackId(), Version: cs.GetVersion(), Recorded: cs.GetWorldDigest(), Built: built}
		}
		if z := removedZone(world, topo.World); z != "" {
			refuse(SwapZoneRemoved, fmt.Sprintf("zone %s is in effect and not in %s@%d", z, cs.GetPackId(), cs.GetVersion()))
			continue
		}
		if z := fallbackless(topo.World); z != "" {
			refuse(SwapFallbackMissing, fmt.Sprintf("zone %s has no fallback Room of its own", z))
			continue
		}
		versions[cs.GetPackId()] = cs.GetVersion()
		digest, world = built, topo.World
		out = append(out, preparedSwap{rec: r, swap: cs, topo: topo, digest: built, versions: copyVersions(versions)})
	}
	return out, nil
}

// removedZone is the first Zone, by ID, that from has and to does not.
func removedZone(from, to *World) ZoneID {
	if from == nil {
		return ""
	}
	ids := make([]string, 0, len(from.Zones))
	for id := range from.Zones {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := to.Zones[ZoneID(id)]; !ok {
			return ZoneID(id)
		}
	}
	return ""
}

// fallbackless is the first Zone, by ID, whose fallback is not one of its Rooms.
func fallbackless(w *World) ZoneID {
	ids := make([]string, 0, len(w.Zones))
	for id := range w.Zones {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, id := range ids {
		z := w.Zones[ZoneID(id)]
		if _, ok := z.Rooms[z.Fallback]; !ok {
			return z.ID
		}
	}
	return ""
}

// applySwap moves the World onto a prepared topology: Zones the content adds
// get empty state, Entities in a Room it removed move to their Zone's fallback
// with an EntityRelocated, and the versions in effect and the digest advance.
// Deterministic: Zones and Entities are visited in ID order. No Zone is
// removed: prepareSwaps refused any swap that would.
//
// Two swaps in one tick apply one after the other, so an Entity can be
// relocated twice in a tick — to the first swap's fallback, and on again if
// the second removes that Room too — and emits an EntityRelocated for each.
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
			continue // unreachable: a swap that removes a Zone is refused
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

// ContentVersionOf is the pack@version an Entity instantiated from t records:
// t's pack as it is in effect, or — when a single-pack source such as
// content.source=dir supplies every Template — that one pack. Empty when
// neither holds, which is before any content is in effect.
func (e *Engine) ContentVersionOf(t *Template) string {
	if t == nil {
		return ""
	}
	if v, ok := e.versions[t.Pack()]; ok {
		return fmt.Sprintf("%s@%d", t.Pack(), v)
	}
	if len(e.versions) == 1 {
		for p, v := range e.versions {
			return fmt.Sprintf("%s@%d", p, v)
		}
	}
	return ""
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
