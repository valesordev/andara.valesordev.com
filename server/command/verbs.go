// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package command

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/sim"
)

// ArgKind is the vocabulary an argument is checked against at parse.
type ArgKind string

// The argument kinds. A kind is closed on purpose: parse rejects a value
// outside it, so the log never carries one.
const (
	// ArgDirection is one of the twelve canonical Directions or a compass
	// alias (n, ne, ... u, d). Resolved to the canonical name.
	ArgDirection ArgKind = "direction"
)

// ArgSpec is one positional argument a verb binds.
type ArgSpec struct {
	Name string  `json:"name"`
	Kind ArgKind `json:"kind"`
}

// Verb is one entry in the verb table: the word a player types, the
// LoggedCommand arm it binds, the role it requires, and how it binds its
// arguments. A verb with Bind and no Args is a shorthand — `north` is
// `move` with direction bound.
type Verb struct {
	Name string          `json:"name"`
	Kind sim.CommandKind `json:"kind"`
	// Role is the role the verb requires, or empty for any authenticated
	// Session. This column is what auth.Authorizer reads.
	Role auth.Role `json:"role,omitempty"`
	Args []ArgSpec `json:"args,omitempty"`
	// Abbrev lets a unique prefix of Name resolve to it.
	Abbrev bool `json:"abbrev,omitempty"`
	// Aliases are exact alternate spellings, resolved before prefixes.
	Aliases []string `json:"aliases,omitempty"`
	// Bind fixes argument values the player does not type.
	Bind map[string]string `json:"bind,omitempty"`
}

// VerbTable is the closed set of verbs a Gateway accepts. It is built in
// and may be replaced from a file (command.verb_table_path); either way it
// is validated once, at construction, so Parse never meets a verb it cannot
// bind.
type VerbTable struct {
	verbs   []Verb
	byName  map[string]*Verb
	byAlias map[string]*Verb
}

// bindableKinds is every arm a verb may bind. sim.KindArrive is not one:
// only a tick produces an Arrive (ADR-0001 rule 4), so no spelling of it
// may reach the log from a player.
var bindableKinds = map[sim.CommandKind][]ArgSpec{
	sim.KindLook: {},
	sim.KindMove: {{Name: "direction", Kind: ArgDirection}},
}

// Builtin is the verb table the server ships with: look, move, and the
// twelve Directions as shorthands with their compass aliases.
func Builtin() *VerbTable {
	verbs := []Verb{
		{Name: "look", Kind: sim.KindLook, Abbrev: true, Aliases: []string{"l"}},
		{Name: "move", Kind: sim.KindMove, Abbrev: true, Args: []ArgSpec{{Name: "direction", Kind: ArgDirection}}, Aliases: []string{"go"}},
	}
	for _, d := range sim.Directions() {
		v := Verb{Name: string(d), Kind: sim.KindMove, Abbrev: true, Bind: map[string]string{"direction": string(d)}}
		if a, ok := directionAlias[d]; ok {
			v.Aliases = []string{a}
		}
		verbs = append(verbs, v)
	}
	t, err := NewVerbTable(verbs)
	if err != nil {
		panic("command: built-in verb table: " + err.Error())
	}
	return t
}

// directionAlias is the compass shorthand for each Direction that has one.
// in and out have none: `i` and `o` are not MUD convention, and a
// single-letter alias for a rarely used Direction costs more mistypes than
// it saves keystrokes.
var directionAlias = map[sim.Direction]string{
	sim.DirNorth: "n", sim.DirNortheast: "ne", sim.DirEast: "e", sim.DirSoutheast: "se",
	sim.DirSouth: "s", sim.DirSouthwest: "sw", sim.DirWest: "w", sim.DirNorthwest: "nw",
	sim.DirUp: "u", sim.DirDown: "d",
}

// NewVerbTable validates verbs and builds the table. Every name and alias
// is unique and lowercase; every kind is bindable; every argument the kind
// needs is either an Arg or a Bind; every role is a known role.
func NewVerbTable(verbs []Verb) (*VerbTable, error) {
	if len(verbs) == 0 {
		return nil, fmt.Errorf("verb table: no verbs")
	}
	t := &VerbTable{byName: map[string]*Verb{}, byAlias: map[string]*Verb{}}
	t.verbs = slices.Clone(verbs)
	for i := range t.verbs {
		v := &t.verbs[i]
		if v.Name == "" || v.Name != strings.ToLower(v.Name) || strings.ContainsFunc(v.Name, isSpace) {
			return nil, fmt.Errorf("verb table: verb %q: name must be lowercase with no whitespace", v.Name)
		}
		if _, dup := t.byName[v.Name]; dup {
			return nil, fmt.Errorf("verb table: verb %q: duplicate name", v.Name)
		}
		if _, dup := t.byAlias[v.Name]; dup {
			return nil, fmt.Errorf("verb table: verb %q: name is another verb's alias", v.Name)
		}
		need, ok := bindableKinds[v.Kind]
		if !ok {
			return nil, fmt.Errorf("verb table: verb %q: kind %q is not one a player may bind (look, move)", v.Name, v.Kind)
		}
		if v.Role != "" && !slices.Contains(auth.AllRoles, v.Role) {
			return nil, fmt.Errorf("verb table: verb %q: unknown role %q", v.Name, v.Role)
		}
		for _, a := range v.Args {
			if !slices.Contains(need, a) {
				return nil, fmt.Errorf("verb table: verb %q: kind %q does not take argument %s:%s", v.Name, v.Kind, a.Name, a.Kind)
			}
		}
		for name, val := range v.Bind {
			idx := slices.IndexFunc(need, func(a ArgSpec) bool { return a.Name == name })
			if idx < 0 {
				return nil, fmt.Errorf("verb table: verb %q: kind %q has no argument %q to bind", v.Name, v.Kind, name)
			}
			if _, err := checkArg(need[idx], val); err != nil {
				return nil, fmt.Errorf("verb table: verb %q: bind %s=%q: %w", v.Name, name, val, err)
			}
		}
		for _, a := range need {
			bound := slices.ContainsFunc(v.Args, func(s ArgSpec) bool { return s.Name == a.Name })
			if _, ok := v.Bind[a.Name]; !ok && !bound {
				return nil, fmt.Errorf("verb table: verb %q: kind %q needs argument %q as an arg or a bind", v.Name, v.Kind, a.Name)
			}
		}
		t.byName[v.Name] = v
		for _, al := range v.Aliases {
			if al == "" || al != strings.ToLower(al) || strings.ContainsFunc(al, isSpace) {
				return nil, fmt.Errorf("verb table: verb %q: alias %q must be lowercase with no whitespace", v.Name, al)
			}
			if _, dup := t.byAlias[al]; dup {
				return nil, fmt.Errorf("verb table: verb %q: alias %q is already taken", v.Name, al)
			}
			if _, dup := t.byName[al]; dup {
				return nil, fmt.Errorf("verb table: verb %q: alias %q is another verb's name", v.Name, al)
			}
			t.byAlias[al] = v
		}
	}
	return t, nil
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

// verbFile is the on-disk form of command.verb_table_path.
type verbFile struct {
	Verbs []Verb `json:"verbs"`
}

// LoadVerbTable reads a verb table from a JSON file:
//
//	{"verbs": [{"name": "look", "kind": "look", "abbrev": true, "aliases": ["l"]},
//	           {"name": "north", "kind": "move", "abbrev": true, "aliases": ["n"],
//	            "bind": {"direction": "north"}}]}
//
// The file replaces the built-in table rather than extending it, so what a
// server accepts is readable in one place.
func LoadVerbTable(path string) (*VerbTable, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("verb table: %w", err)
	}
	var f verbFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("verb table %s: %w", path, err)
	}
	t, err := NewVerbTable(f.Verbs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// Verbs returns the table's entries in name order.
func (t *VerbTable) Verbs() []Verb {
	out := slices.Clone(t.verbs)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Roles is the table's role column, in the form auth.Authorizer reads.
// Verbs with no role are absent: they need only an authenticated Session.
func (t *VerbTable) Roles() auth.VerbRoles {
	out := auth.VerbRoles{}
	for _, v := range t.verbs {
		if v.Role != "" {
			out[v.Name] = v.Role
		}
	}
	return out
}

// Resolve maps a typed token to a verb: an exact name, then an exact alias,
// then the unique abbreviable verb the token prefixes. A prefix two verbs
// claim is unknown_verb naming both, so a player learns to type one more
// letter rather than guessing which one won.
func (t *VerbTable) Resolve(token string) (*Verb, error) {
	if v, ok := t.byName[token]; ok {
		return v, nil
	}
	if v, ok := t.byAlias[token]; ok {
		return v, nil
	}
	var matches []*Verb
	for i := range t.verbs {
		v := &t.verbs[i]
		if v.Abbrev && strings.HasPrefix(v.Name, token) {
			matches = append(matches, v)
		}
	}
	switch len(matches) {
	case 0:
		return nil, &Error{Stage: StageParse, Code: CodeUnknownVerb, Detail: "unknown verb " + quote(token)}
	case 1:
		return matches[0], nil
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	sort.Strings(names)
	return nil, &Error{Stage: StageParse, Code: CodeUnknownVerb, Detail: quote(token) + " could be " + strings.Join(names, " or ")}
}

// checkArg validates and canonicalizes one argument value for its kind.
func checkArg(spec ArgSpec, val string) (string, error) {
	switch spec.Kind {
	case ArgDirection:
		d := sim.Direction(val)
		if d.Valid() {
			return string(d), nil
		}
		for full, alias := range directionAlias {
			if alias == val {
				return string(full), nil
			}
		}
		return "", fmt.Errorf("%s is not a direction; directions are %s", quote(val), sim.DirectionsList())
	}
	return "", fmt.Errorf("argument kind %q is not one the parser knows", spec.Kind)
}
