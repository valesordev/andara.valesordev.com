// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"sort"
	"strconv"
)

// ComponentType is the namespaced type of a Component — "andara.core.Dark"
// (ADR-0010 decision 6). The namespace carries provenance: a core Component and
// a Builder pack's Component cannot collide, and which is which is readable
// without a lookup.
type ComponentType string

// FieldKind selects which value a ComponentField carries. It mirrors the
// `oneof` in andara/content/v1/zone.proto, which has no float member and may
// never grow one: a float in the State Hash makes the hash
// platform-dependent (ADR-0007 rule 3).
type FieldKind uint8

// The kinds a Component field value may take.
const (
	// FieldUnset is a field with no value set. Rejected at load — a named
	// field with no value is a Builder mistake, not an empty string.
	FieldUnset FieldKind = iota
	FieldString
	FieldInt
	FieldBool
)

// String names the kind as it appears in error messages and in the canonical
// serialization, where it is the tag that keeps the encoding injective.
func (k FieldKind) String() string {
	switch k {
	case FieldString:
		return "string"
	case FieldInt:
		return "int"
	case FieldBool:
		return "bool"
	default:
		return "unset"
	}
}

// ComponentField is one named value inside a Component. Kind selects which
// member is meaningful; the others are zero.
type ComponentField struct {
	Name string
	Kind FieldKind
	Str  string
	Int  int64
	Bool bool
}

// Value renders the field's value for the canonical serialization. Paired with
// the Kind tag it is reversible, which is what makes the encoding injective.
func (f ComponentField) Value() string {
	switch f.Kind {
	case FieldString:
		return f.Str
	case FieldInt:
		return strconv.FormatInt(f.Int, 10)
	case FieldBool:
		return strconv.FormatBool(f.Bool)
	default:
		return ""
	}
}

// Component is data attached to a Room or a Zone (ADR-0010 decision 8).
// Components hold no logic: systems read them, and a system is Go written by a
// developer, never anything a Builder authored.
type Component struct {
	Type   ComponentType
	Fields []ComponentField // stable, sorted by Name
}

// Field returns the named field, if the Component carries it.
func (c Component) Field(name string) (ComponentField, bool) {
	for _, f := range c.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return ComponentField{}, false
}

// componentSpec declares one Component type the server understands: its name
// and the fields it may carry, by kind. A marker Component declares no fields.
type componentSpec struct {
	fields map[string]FieldKind
}

// componentRegistry is the closed vocabulary of Component types.
//
// ADR-0010 decision 7: Component types are defined on the server, and Builders
// compose them rather than creating them. So this is a table in the binary, not
// configuration — adding a type is a code change and a release, which is
// exactly what the rejection message in AC-2 has to tell a Builder so they stop
// re-checking their spelling and file an issue instead.
//
// The four seeded here are markers: enough to prove the mechanism, and the ones
// that recur across every MUD. Which Room and Zone properties Andara actually
// wants is game design (CLAUDE.md §11) and is expected to grow from real
// content rather than from guessing now.
var componentRegistry = map[ComponentType]componentSpec{
	// The Room is unlit. Data only: the system that reads it and suppresses a
	// Room description belongs to the story that adds looking in the dark.
	"andara.core.Dark": {},
	// Magic does not function here.
	"andara.core.NoMagic": {},
	// The Room is enclosed — weather and sky do not reach it.
	"andara.core.Indoors": {},
	// Recall and other self-teleport effects do not leave from here.
	"andara.core.NoRecall": {},

	// AW-SRV-022: the two Components the andara.core seed Templates carry.
	// One vocabulary for every carrier (ADR-0010 decision 8), so these are
	// legal on a Room or a Zone too, where they mean nothing until a system
	// reads them.

	// Binds a Behavior to a Template: the one seam between the Template
	// hierarchy and the Python class hierarchy in a Behavior Agent
	// (ADR-0010). The name is recorded here and not validated: AW-CLI-006
	// validates it at compile, AW-SRV-009 at claim.
	"andara.core.Behavior": {fields: map[string]FieldKind{"name": FieldString}},
	// NPC memory as World state (AW-SRV-009). The slots — {slot, bytes} —
	// are runtime state a Behavior sets by Command, not Template data, and
	// the ComponentField oneof could not carry them anyway; as a Template
	// Component this is a marker that says "this Entity remembers".
	"andara.core.Memory": {},
}

// KnownComponentType reports whether the server has a Component type
// registered.
func KnownComponentType(t ComponentType) bool {
	_, ok := componentRegistry[t]
	return ok
}

// ComponentTypes returns the registered Component types in sorted order.
// Exported so the CLI's content validation quotes the same vocabulary the
// loader enforces rather than a copy of it (ADR-0004: one validator).
func ComponentTypes() []ComponentType {
	out := make([]ComponentType, 0, len(componentRegistry))
	for t := range componentRegistry {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
