// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"sort"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// EntityState is one Entity as the simulation holds it: the Template it was
// made from, the content version that Template came from, and its
// Components. The Template reference is what lets an Entity always name its
// pack — AW-SRV-009 scopes an agent to the pack of the Entities it may drive
// — and the content version is provenance for AW-SRV-019's records.
//
// This is the Go form. The snapshot body that carries it on the wire is
// AW-SRV-006's to define; nothing here assigns a field number.
type EntityState struct {
	ID             EntityID
	Template       TemplateRef
	ContentVersion string
	Components     []Component // sorted by type; at most one of each
	// Room is where the Entity is. The Zone is implicit — an Entity lives in
	// the ZoneState whose Partition owns it (ADR-0001 rule 3) — so a RoomID
	// is the whole position. Empty for an Entity that is nowhere, which is
	// legal only in a test; every Entity a Room can list has one
	// (AW-SRV-003 Data / state impact).
	Room RoomID
	// Name is the display name when the Entity has one apart from its ID:
	// a Character's, immutable (docs/glossary.md), carried by the
	// BindCharacter that made the body (AW-SRV-014). Empty for anything
	// named by its ID.
	Name string
	// Dormant marks a Character whose Session left the World (AW-SRV-014):
	// the body keeps its Room so "where you were" survives a restart, but
	// it is in no Room's occupant list, invisible to look, and addressed by
	// no Event, until the next BindCharacter clears it. DormantSince is the
	// Tick it went dormant, for AW-SRV-032's retention.
	Dormant      bool
	DormantSince Tick
}

// DisplayName is how a Room lists the Entity and how CharacterArrived and
// CharacterLeft name it: Name when set, else the EntityID.
func (e *EntityState) DisplayName() string {
	if e.Name != "" {
		return e.Name
	}
	return string(e.ID)
}

// Present reports whether the Entity is in the World: in a Room and not
// dormant. Only a present Entity is listed, addressed, or acted for.
func (e *EntityState) Present() bool { return e != nil && e.Room != "" && !e.Dormant }

// Component returns the Entity's Component of type ct, if it carries one.
func (e *EntityState) Component(ct ComponentType) (Component, bool) {
	if e == nil {
		return Component{}, false
	}
	return findComponent(e.Components, ct)
}

// Instantiate makes an Entity from a Template: the flattened Components are
// copied onto it, sorted by type, and the Template and content version are
// recorded. Deterministic — the same inputs produce the same EntityState
// (AC-5) — and it reads nothing but its arguments, so the tick may call it.
func Instantiate(t *Template, id EntityID, contentVersion string) EntityState {
	e := EntityState{ID: id, Template: t.Ref, ContentVersion: contentVersion}
	if len(t.Components) > 0 {
		e.Components = make([]Component, len(t.Components))
		for i, c := range t.Components {
			e.Components[i] = Component{Type: c.Type, Fields: append([]ComponentField(nil), c.Fields...)}
		}
		sortComponents(e.Components)
	}
	return e
}

// Proto renders the Entity for transit across a Zone boundary
// (logv1.Arrive). Position is not carried: the Arrive names the target Room.
func (e *EntityState) Proto() *logv1.Entity {
	out := &logv1.Entity{Id: string(e.ID), Template: string(e.Template), ContentVersion: e.ContentVersion, Name: e.Name}
	for _, c := range e.Components {
		cv := &contentv1.ComponentValue{Type: string(c.Type)}
		for _, f := range c.Fields {
			fd := &contentv1.ComponentField{Name: f.Name}
			switch f.Kind {
			case FieldString:
				fd.Value = &contentv1.ComponentField_StringValue{StringValue: f.Str}
			case FieldInt:
				fd.Value = &contentv1.ComponentField_IntValue{IntValue: f.Int}
			case FieldBool:
				fd.Value = &contentv1.ComponentField_BoolValue{BoolValue: f.Bool}
			}
			cv.Fields = append(cv.Fields, fd)
		}
		out.Components = append(out.Components, cv)
	}
	return out
}

// EntityFromProto is the inverse of Proto, placed in room. Components are
// taken as carried — the source Zone validated them when it loaded the
// Template — and sorted, so a hand-built record cannot break the invariant.
func EntityFromProto(p *logv1.Entity, room RoomID) EntityState {
	e := EntityState{ID: EntityID(p.GetId()), Template: TemplateRef(p.GetTemplate()), ContentVersion: p.GetContentVersion(), Room: room, Name: p.GetName()}
	for _, cv := range p.GetComponents() {
		c := Component{Type: ComponentType(cv.GetType())}
		for _, fd := range cv.GetFields() {
			f, _ := fieldValue(fd)
			c.Fields = append(c.Fields, f)
		}
		e.Components = append(e.Components, c)
	}
	sortComponents(e.Components)
	return e
}

// sortComponents orders a Component set by type, and each set's fields by
// name — the invariant every reader of a Component set may assume.
func sortComponents(set []Component) {
	sort.Slice(set, func(i, j int) bool { return set[i].Type < set[j].Type })
	for _, c := range set {
		sort.Slice(c.Fields, func(i, j int) bool { return c.Fields[i].Name < c.Fields[j].Name })
	}
}
