// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"strings"

	"github.com/valesordev/andara/server/sim"
)

// SourceMap places a finding about compiled content back on the source that
// produced it (AW-CLI-002).
//
// The loader validates compiled Zones and Templates, which carry no source
// position: a Zone blob is a ZoneDefinition, and the `.aw` line its Room came
// from is gone by then. What a loader finding does carry is its declaration
// chain — Zone, Room, Exit direction, or the Template — and that chain is the
// one the compiler reports its own findings under (errors.md §1). So the map
// is keyed by chain, and it holds the position the compiler would have
// reported at, which is what makes `content validate`'s findings, the
// compiler's, and the publish gate's the same diagnostic rather than three
// descriptions of it (AC-4).
type SourceMap struct {
	zones     map[string]origin // key: zone
	fallbacks map[string]origin // key: zone; the first `fallback`'s Room reference
	rooms     map[string]origin // key: zone \x00 room
	exits     map[string]origin // key: zone \x00 room \x00 direction; the `exit` keyword
	exitRefs  map[string]origin // same key; the target reference
	templates map[string]origin // key: <pack>.<Name>
}

// origin is where a declaration is, and the chain the compiler reports it
// under.
type origin struct {
	file      string
	line, col int
	chain     []string
}

func newSourceMap() *SourceMap {
	return &SourceMap{
		zones: map[string]origin{}, fallbacks: map[string]origin{}, rooms: map[string]origin{},
		exits: map[string]origin{}, exitRefs: map[string]origin{}, templates: map[string]origin{},
	}
}

func at(file string, p Pos, chain ...string) origin {
	return origin{file: file, line: p.Line, col: p.Col, chain: chain}
}

func key(parts ...string) string { return strings.Join(parts, "\x00") }

// Place returns the finding as the compiler would have reported it: at the
// declaration its chain names, with the compiler's chain. A chain the map has
// no declaration for — a finding about a pack as a whole, or one naming a
// declaration this compile didn't produce — is not placed, and the caller
// keeps the position it has.
//
// The position within a declaration follows the code, as the compiler's does:
// an unresolved Exit target is reported at the reference, every other Exit
// finding at the `exit` keyword, and fallback_missing at the `fallback`
// reference when the Zone has one (errors.md §3).
func (m *SourceMap) Place(code, message string, chain []string, sev Severity) (Diagnostic, bool) {
	if m == nil {
		return Diagnostic{}, false
	}
	o, ok := m.lookup(code, chain)
	if !ok {
		return Diagnostic{}, false
	}
	return Diagnostic{
		File: o.file, Line: o.line, Col: o.col,
		Code: code, Message: message, Chain: append([]string(nil), o.chain...), Severity: sev,
	}, true
}

func (m *SourceMap) lookup(code string, chain []string) (origin, bool) {
	if len(chain) == 1 {
		if o, ok := m.templates[chain[0]]; ok {
			return o, true
		}
	}
	if code == string(sim.ErrFallbackMissing) && len(chain) >= 1 {
		// The loader names the missing Room in the chain; the compiler's
		// finding is about the Zone, at its `fallback` if it has one.
		if o, ok := m.fallbacks[chain[0]]; ok {
			return o, true
		}
		o, ok := m.zones[chain[0]]
		return o, ok
	}
	switch len(chain) {
	case 1:
		o, ok := m.zones[chain[0]]
		return o, ok
	case 2:
		o, ok := m.rooms[key(chain...)]
		return o, ok
	case 3:
		if code == string(sim.ErrUnknownRoom) || code == string(sim.ErrUnknownZone) {
			if o, ok := m.exitRefs[key(chain...)]; ok {
				return o, true
			}
		}
		o, ok := m.exits[key(chain...)]
		return o, ok
	}
	return origin{}, false
}
