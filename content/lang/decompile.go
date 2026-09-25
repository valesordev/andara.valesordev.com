// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"path"
	"sort"
	"strings"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// Decompile turns compiled output back into .aw source, keyed by path.
//
// The contract it satisfies is the one that matters operationally
// (semantics.md §8): **compiled → source → compiled is identity**. It is how a
// Builder who has lost their working copy gets it back. The other direction
// holds only for canonical source, because decompile output carries no
// comments — what preserves those is the source blob published beside the
// compiled output, not this function.
//
// Because ZoneDefinition has no SourceRef, decompile cannot know which file a
// Zone came from, so it emits the canonical file layout (semantics.md §8):
// pack.aw alone, one file per Zone, Templates grouped by source.file's
// basename or templates.aw when absent.
func Decompile(out *Output) (map[string][]byte, error) {
	return DecompileWith(out, nil)
}

// DecompileWith is Decompile against the core pack the output was compiled
// against.
//
// It matters for un-flattening. A Template's compiled Components are the merged
// set of everything its ancestors declare, and what the Template itself wrote is
// recovered by subtracting the parent's set — so the parent has to be in hand.
// A parent inside the pack is in out.Templates; a parent in andara.core is not,
// and without it every inherited Component looks like the subtype's own.
//
// The round trip holds either way, because declaring a Component with an empty
// body is a no-op rather than a reset (semantics.md §5), so the redundant line
// recompiles to the same bytes. What it costs is the reading: every Builder
// pack extends andara.core, so without the core pack `decompile` hands back
// source carrying a restatement of every marker Component the chain inherits —
// the ergonomic failure the whole model exists to avoid (ADR-0010 decision 4).
func DecompileWith(out *Output, core *Pack) (map[string][]byte, error) {
	if out == nil {
		return nil, fmt.Errorf("no output to decompile")
	}
	// Declarations are collected per file rather than written per file,
	// because the canonical layout can put two of them in the same place: the
	// language has no reserved words, so `zone pack "Pack"` is legal and lands
	// on pack.aw, and a Template group's basename can equal a Zone's. A file is
	// a flat sequence of declarations (grammar.ebnf), so sharing one is legal
	// source — where overwriting the map entry silently dropped whichever came
	// first, and for `pack.aw` that was the pack declaration itself, leaving
	// source that cannot be recompiled.
	decls := map[string][]Decl{}
	add := func(name string, d Decl) { decls[name] = append(decls[name], d) }

	pack := &PackDecl{Name: out.Pack}
	if out.Requires.Pack != "" {
		pack.Requires = &RequiresClause{Pack: out.Requires.Pack, Version: out.Requires.Version}
	}
	add("pack.aw", pack)

	for _, z := range out.Zones {
		d, err := zoneToAST(z)
		if err != nil {
			return nil, err
		}
		add(z.GetId()+".aw", d)
	}

	// Templates grouped by the basename of their SourceRef, so a pack authored
	// in the canonical layout round-trips file for file.
	groups := map[string][]*contentv1.TemplateDefinition{}
	for _, t := range out.Templates {
		groups[templateFile(t)] = append(groups[templateFile(t)], t)
	}
	byName := map[string]*contentv1.TemplateDefinition{}
	for _, t := range out.Templates {
		byName[t.GetName()] = t
	}
	if core != nil {
		for _, t := range core.Templates {
			byName[t.GetName()] = t
		}
	}
	for name, ts := range groups {
		sort.Slice(ts, func(i, j int) bool { return sourceLine(ts[i]) < sourceLine(ts[j]) })
		for _, t := range ts {
			d, err := templateToAST(t, byName, out.Pack)
			if err != nil {
				return nil, err
			}
			add(name, d)
		}
	}

	files := map[string][]byte{}
	for name, ds := range decls {
		files[name] = canonicalPrint(&File{Decls: ds})
	}
	return files, nil
}

// canonicalPrint renders in canonical form: fmt-clean, comment-free, and in
// canonical order (formatting.md §6).
func canonicalPrint(f *File) []byte {
	p := newPrinter(f)
	p.canon = true
	return []byte(p.file(f))
}

// lit builds a literal from a compiled value, for the decompiler, which has no
// authored spelling to reproduce.
func lit(v string) StringLit { return StringLit{Raw: quote(v), Value: v} }

func templateFile(t *contentv1.TemplateDefinition) string {
	src := t.GetSource().GetFile()
	if src == "" {
		return "templates.aw"
	}
	return path.Base(src)
}

func sourceLine(t *contentv1.TemplateDefinition) uint32 { return t.GetSource().GetLine() }

func zoneToAST(z *contentv1.ZoneDefinition) (*ZoneDecl, error) {
	d := &ZoneDecl{ID: z.GetId(), Name: lit(z.GetName())}
	if fr := z.GetFallbackRoom(); fr != "" {
		d.Fallbacks = append(d.Fallbacks, &FallbackDecl{Room: fr})
	}
	for _, c := range z.GetComponents() {
		d.Components = append(d.Components, componentToAST(c))
	}
	for _, r := range z.GetRooms() {
		room := &RoomDecl{ID: r.GetId(), Title: lit(r.GetTitle())}
		if d := r.GetDescription(); d != "" {
			// One literal, which canonical printing then wraps at the budget:
			// decompile has no record of where the Builder broke the prose, so
			// it produces the one deterministic wrap (formatting.md §6).
			room.Descs = append(room.Descs, &DescDecl{
				Value: d,
				Parts: []StringLit{{Raw: quote(d), Value: d}},
			})
		}
		for _, e := range r.GetExits() {
			room.Exits = append(room.Exits, &ExitDecl{
				Direction: e.GetDirection(), ToZone: e.GetToZone(), ToRoom: e.GetToRoom(),
			})
		}
		for _, c := range r.GetComponents() {
			room.Components = append(room.Components, componentToAST(c))
		}
		d.Rooms = append(d.Rooms, room)
	}
	return d, nil
}

func componentToAST(c *contentv1.ComponentValue) *ComponentDecl {
	out := &ComponentDecl{Type: c.GetType()}
	for _, f := range c.GetFields() {
		out.Fields = append(out.Fields, &FieldAssign{Name: f.GetName(), Value: fieldToValue(f)})
	}
	return out
}

func fieldToValue(f *contentv1.ComponentField) Value {
	switch v := f.GetValue().(type) {
	case *contentv1.ComponentField_StringValue:
		return Value{Kind: String, Str: v.StringValue, Raw: quote(v.StringValue)}
	case *contentv1.ComponentField_IntValue:
		return Value{Kind: Int, Int: v.IntValue, Raw: fmt.Sprintf("%d", v.IntValue)}
	case *contentv1.ComponentField_BoolValue:
		raw := "false"
		if v.BoolValue {
			raw = "true"
		}
		return Value{Kind: LowerID, Bool: v.BoolValue, Raw: raw}
	}
	return Value{Kind: String, Raw: `""`}
}

// templateToAST un-flattens one Template back to what its own file declared.
//
// A compiled Template carries the merged set of everything its ancestors
// declare, so emitting that set verbatim would produce source that recompiles
// to the same bytes but reads as though the Builder restated every inherited
// value — the ergonomic failure the whole model exists to avoid (ADR-0010
// decision 4). What a Template declared itself is recoverable exactly:
// provenance names the Template that set each field, so a field whose `from` is
// this Template is one this Template wrote.
//
// Marker Components carry no fields and so appear in no provenance entry. One
// is this Template's own when the parent does not carry it.
func templateToAST(t *contentv1.TemplateDefinition, byName map[string]*contentv1.TemplateDefinition, pack string) (*TemplateDecl, error) {
	d := &TemplateDecl{Name: localName(t.GetName())}
	chain := t.GetChain()
	switch {
	case len(chain) == 0:
		return nil, fmt.Errorf("template %q has an empty chain", t.GetName())
	case len(chain) == 1:
		d.Heads = []TemplateHead{{Kind: kindKeyword(t.GetKind())}}
	default:
		d.Heads = []TemplateHead{{Extends: shorten(chain[len(chain)-2], pack)}}
	}

	// The parent's flattened set, which is what this Template inherited.
	inherited := map[string]map[string]bool{}
	if len(chain) > 1 {
		if parent, ok := byName[chain[len(chain)-2]]; ok {
			for _, c := range parent.GetComponents() {
				inherited[c.GetType()] = map[string]bool{}
				for _, f := range c.GetFields() {
					inherited[c.GetType()][f.GetName()] = true
				}
			}
		}
	}

	own := map[string]bool{}
	for _, p := range t.GetProvenance() {
		if p.GetFrom() == t.GetName() {
			own[p.GetComponent()+"\x00"+p.GetField()] = true
		}
	}

	for _, c := range t.GetComponents() {
		decl := &ComponentDecl{Type: c.GetType()}
		for _, f := range c.GetFields() {
			if own[c.GetType()+"\x00"+f.GetName()] {
				decl.Fields = append(decl.Fields, &FieldAssign{Name: f.GetName(), Value: fieldToValue(f)})
			}
		}
		_, wasInherited := inherited[c.GetType()]
		if len(decl.Fields) == 0 && wasInherited {
			continue // wholly the parent's; this Template said nothing about it
		}
		d.Components = append(d.Components, decl)
	}
	return d, nil
}

// shorten writes an ancestor the way a Builder would: bare within the
// declaring pack, dotted to reach andara.core (semantics.md §4).
func shorten(ref, pack string) string {
	if strings.HasPrefix(ref, pack+".") {
		return strings.TrimPrefix(ref, pack+".")
	}
	return ref
}

func localName(ref string) string {
	if i := strings.LastIndex(ref, "."); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func kindKeyword(k contentv1.TemplateKind) string {
	switch k {
	case contentv1.TemplateKind_ITEM:
		return "item"
	case contentv1.TemplateKind_BEHAVIOR:
		return "behavior"
	default:
		return "entity"
	}
}

// Canonical order (formatting.md §6). It is a separate, stronger property than
// fmt-clean: fmt reorders nothing, and only decompile output is in this order.

func canonicalZoneItems(d *ZoneDecl) []ZoneItem {
	var out []ZoneItem
	for _, f := range d.Fallbacks {
		out = append(out, f)
	}
	comps := append([]*ComponentDecl{}, d.Components...)
	sort.Slice(comps, func(i, j int) bool { return comps[i].Type < comps[j].Type })
	for _, c := range comps {
		out = append(out, c)
	}
	rooms := append([]*RoomDecl{}, d.Rooms...)
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })
	for _, r := range rooms {
		out = append(out, r)
	}
	return out
}

func canonicalRoomItems(d *RoomDecl) []RoomItem {
	var out []RoomItem
	for _, desc := range d.Descs {
		out = append(out, desc)
	}
	exits := append([]*ExitDecl{}, d.Exits...)
	sort.Slice(exits, func(i, j int) bool { return exits[i].Direction < exits[j].Direction })
	for _, e := range exits {
		out = append(out, e)
	}
	comps := append([]*ComponentDecl{}, d.Components...)
	sort.Slice(comps, func(i, j int) bool { return comps[i].Type < comps[j].Type })
	for _, c := range comps {
		out = append(out, c)
	}
	return out
}

func canonicalTemplateItems(d *TemplateDecl) []TemplateItem {
	comps := append([]*ComponentDecl{}, d.Components...)
	sort.Slice(comps, func(i, j int) bool { return comps[i].Type < comps[j].Type })
	out := make([]TemplateItem, 0, len(comps))
	for _, c := range comps {
		out = append(out, c)
	}
	return out
}
