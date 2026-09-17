// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import "sort"

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
}

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

// sortComponents orders a Component set by type, and each set's fields by
// name — the invariant every reader of a Component set may assume.
func sortComponents(set []Component) {
	sort.Slice(set, func(i, j int) bool { return set[i].Type < set[j].Type })
	for _, c := range set {
		sort.Slice(c.Fields, func(i, j int) bool { return c.Fields[i].Name < c.Fields[j].Name })
	}
}
