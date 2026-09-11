// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import "strings"

// canonicalDirections is the closed Direction set, decided 2026-09-10 and
// written down in docs/glossary.md. Listed in compass order and then the
// vertical and containment pairs, because this slice is also what a Builder
// reads back in the rejection message and a sorted-alphabetically list of
// directions is a worse thing to read than a compass.
//
// Growing the set is a glossary edit plus this slice plus a content
// revalidation. It is deliberately not a protobuf enum: an unknown enum member
// is dropped silently on the wire, where an unknown string is rejected loudly
// with a file and a line (zone.proto, ExitDefinition.direction).
var canonicalDirections = []Direction{
	North, Northeast, East, Southeast,
	South, Southwest, West, Northwest,
	Up, Down, In, Out,
}

// The twelve canonical Directions.
const (
	North     Direction = "north"
	Northeast Direction = "northeast"
	East      Direction = "east"
	Southeast Direction = "southeast"
	South     Direction = "south"
	Southwest Direction = "southwest"
	West      Direction = "west"
	Northwest Direction = "northwest"
	Up        Direction = "up"
	Down      Direction = "down"
	In        Direction = "in"
	Out       Direction = "out"
)

// directionReverse pairs every Direction with its reverse. Every canonical
// Direction has one, which is what lets the loader warn about a one-way Exit
// that was almost certainly meant to be two.
var directionReverse = map[Direction]Direction{
	North:     South,
	South:     North,
	East:      West,
	West:      East,
	Northeast: Southwest,
	Southwest: Northeast,
	Northwest: Southeast,
	Southeast: Northwest,
	Up:        Down,
	Down:      Up,
	In:        Out,
	Out:       In,
}

// Valid reports whether d is one of the twelve canonical Directions.
// The comparison is exact: "North" is not "north". Case folding here would
// make content that round-trips through a case-insensitive tool hash
// differently from the file a Builder wrote.
func (d Direction) Valid() bool {
	_, ok := directionReverse[d]
	return ok
}

// Reverse returns the Direction that leads back, and false for a Direction
// outside the canonical set.
func (d Direction) Reverse() (Direction, bool) {
	r, ok := directionReverse[d]
	return r, ok
}

// Directions returns the canonical set in compass order. The slice is copied
// so a caller cannot reorder the vocabulary the error messages quote.
func Directions() []Direction {
	out := make([]Direction, len(canonicalDirections))
	copy(out, canonicalDirections)
	return out
}

// DirectionsList renders the canonical set for an error message.
func DirectionsList() string {
	parts := make([]string, len(canonicalDirections))
	for i, d := range canonicalDirections {
		parts[i] = string(d)
	}
	return strings.Join(parts, ", ")
}
