// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package contentequiv

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/valesordev/andara/content/lang"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// Blob-level twins (AW-CLI-002 AC-4's error-level half, from the §8 review).
//
// The compiler refuses an invalid pack before it has blobs, so the publish
// gate never sees the corpus's invalid/semantic cases. But the loader raises
// every code in errors.md §3.2 too, because the compiler isn't a gate the
// server trusts. A twin is the compiled form such a case would have if
// something other than the compiler wrote it, with the same defect. The gate
// is held to the case's sidecar on code and chain. Position is left out: a
// blob has no source line to place a finding on.

// Twin is one invalid/semantic case as blobs, by published path.
type Twin struct {
	// Case is the corpus case it twins, as Cases names it.
	Case  string
	Pack  string
	Blobs map[string][]byte
	// Want is the case's sidecar in CodeChain form, sorted.
	Want []string
}

// CodeChain is a finding without its position: `code`, the chain indented
// beneath.
func CodeChain(code string, chain []string) string {
	s := code
	for _, c := range chain {
		s += "\n  " + c
	}
	return s
}

// Twins builds every twin, with its expected findings read from the corpus
// under root.
func Twins(root string) ([]Twin, error) {
	var out []Twin
	for name, blobs := range twinBlobs() {
		want, err := readSidecar(filepath.Join(root, Corpus, "invalid", "semantic", name, "expected.errors"))
		if err != nil {
			return nil, err
		}
		// Strip the position from each sidecar finding.
		for i, w := range want {
			_, rest, _ := strings.Cut(w, ": ")
			want[i] = rest
		}
		sort.Strings(want)
		out = append(out, Twin{Case: "invalid/semantic/" + name, Pack: "p", Blobs: blobs, Want: want})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Case < out[j].Case })
	return out, nil
}

// --- building blobs -----------------------------------------------------------

type blobs map[string][]byte

func zone(id, fallback string, rooms ...*contentv1.RoomDefinition) *contentv1.ZoneDefinition {
	return &contentv1.ZoneDefinition{FormatVersion: lang.FormatVersion, Id: id, Name: id, FallbackRoom: fallback, Rooms: rooms}
}

func room(id string, exits ...*contentv1.ExitDefinition) *contentv1.RoomDefinition {
	return &contentv1.RoomDefinition{Id: id, Title: id, Exits: exits}
}

func exit(dir, toZone, toRoom string) *contentv1.ExitDefinition {
	return &contentv1.ExitDefinition{Direction: dir, ToZone: toZone, ToRoom: toRoom}
}

func comp(typ string, fields ...*contentv1.ComponentField) *contentv1.ComponentValue {
	return &contentv1.ComponentValue{Type: typ, Fields: fields}
}

func tmpl(name string, kind contentv1.TemplateKind, chain []string, comps ...*contentv1.ComponentValue) *contentv1.TemplateDefinition {
	t := &contentv1.TemplateDefinition{FormatVersion: lang.FormatVersion, Name: name, Kind: kind, Chain: chain, Components: comps, Resolved: true}
	for _, c := range comps {
		for _, f := range c.GetFields() {
			t.Provenance = append(t.Provenance, &contentv1.FieldProvenance{Component: c.GetType(), Field: f.GetName(), From: name})
		}
	}
	return t
}

func (b blobs) zone(path string, z *contentv1.ZoneDefinition) blobs {
	b[path] = lang.CanonicalJSON(z)
	return b
}

func (b blobs) tmpl(path string, t *contentv1.TemplateDefinition) blobs {
	b[path] = lang.CanonicalJSON(t)
	return b
}

func str(name, v string) *contentv1.ComponentField {
	return &contentv1.ComponentField{Name: name, Value: &contentv1.ComponentField_StringValue{StringValue: v}}
}

func boolean(name string, v bool) *contentv1.ComponentField {
	return &contentv1.ComponentField{Name: name, Value: &contentv1.ComponentField_BoolValue{BoolValue: v}}
}

const entity, item = contentv1.TemplateKind_ENTITY, contentv1.TemplateKind_ITEM

// twinBlobs is each eligible case's twin: the cases whose codes errors.md
// §3.2 says the loader raises too. Each mirrors its case's source.
func twinBlobs() map[string]blobs {
	deep := blobs{}
	var chain []string
	for i := 1; i <= 17; i++ {
		name := fmt.Sprintf("p.D%02d", i)
		chain = append(append([]string(nil), chain...), name)
		deep.tmpl("templates/"+name+".json", tmpl(name, entity, chain))
	}
	return map[string]blobs{
		"chain-too-deep":                deep,
		"cross-pack-exit":               blobs{}.zone("z.json", zone("z", "r", room("r", exit("east", "docks", "pier")))),
		"duplicate-component-type-room": blobs{}.zone("z.json", zone("z", "r", &contentv1.RoomDefinition{Id: "r", Title: "R", Components: []*contentv1.ComponentValue{comp("andara.core.Dark"), comp("andara.core.Dark")}})),
		"duplicate-component-type-template": blobs{}.tmpl("templates/p.T.json", tmpl("p.T", entity, []string{"p.T"},
			comp("andara.core.Behavior", str("name", "a")), comp("andara.core.Behavior", str("name", "b")))),
		"duplicate-direction": blobs{}.zone("z.json", zone("z", "r",
			room("r", exit("north", "", "a"), exit("north", "", "b")), room("a", exit("south", "", "r")), room("b", exit("south", "", "r")))),
		"duplicate-room": blobs{}.zone("z.json", zone("z", "r", room("r"), room("r"))),
		"duplicate-template": blobs{}.
			tmpl("templates/p.Twin.json", tmpl("p.Twin", entity, []string{"p.Twin"})).
			tmpl("templates/p.Twin-again.json", tmpl("p.Twin", item, []string{"p.Twin"})),
		"duplicate-zone": blobs{}.
			zone("a.json", zone("z", "r", room("r"))).
			zone("b.json", zone("z", "q", room("q"))),
		"fallback-missing": blobs{}.zone("market.json", zone("market", "nowhere", room("square"))),
		"invalid-component-field-kind": blobs{}.tmpl("templates/p.T.json", tmpl("p.T", entity, []string{"p.T"},
			comp("andara.core.Behavior", boolean("name", true)))),
		"invalid-component-field-name": blobs{}.zone("z.json", zone("z", "r",
			&contentv1.RoomDefinition{Id: "r", Title: "R", Components: []*contentv1.ComponentValue{comp("andara.core.Dark", boolean("enabled", false))}})),
		"multiple-findings": blobs{}.
			zone("z.json", zone("z", "r", room("r", exit("norht", "", "h"), exit("north", "", "nowhere")), room("h", exit("south", "", "r")))).
			tmpl("templates/p.T.json", tmpl("p.T", entity, []string{"p.T"}, comp("andara.core.Drak"))),
		"unknown-component-type": blobs{}.zone("z.json", zone("z", "r",
			&contentv1.RoomDefinition{Id: "r", Title: "R", Components: []*contentv1.ComponentValue{comp("andara.core.Drak")}})),
		"unknown-direction":             blobs{}.zone("z.json", zone("z", "r", room("r", exit("norht", "", "h")), room("h", exit("south", "", "r")))),
		"unknown-room":                  blobs{}.zone("z.json", zone("z", "r", room("r", exit("north", "", "nowhere")))),
		"unknown-room-cross-zone":       blobs{}.zone("z.json", zone("z", "r", room("r", exit("north", "other", "nowhere")))).zone("other.json", zone("other", "q", room("q"))),
		"unknown-zone":                  blobs{}.zone("z.json", zone("z", "r", room("r", exit("north", "elsewhere", "hall")))),
		"unresolved-extends":            blobs{}.tmpl("templates/p.Orphan.json", tmpl("p.Orphan", entity, []string{"p.Nowhere", "p.Orphan"})),
		"unresolved-extends-other-pack": blobs{}.tmpl("templates/p.Reaching.json", tmpl("p.Reaching", entity, []string{"docks.Crate", "p.Reaching"})),
	}
}
