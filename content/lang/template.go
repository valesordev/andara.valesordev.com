// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"path"
	"sort"
	"strings"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// templateDecl pairs a declaration with its file and its qualified name.
type templateDecl struct {
	file string
	ref  string // <pack>.<Name>
	d    *TemplateDecl
}

// resolveTemplates flattens every Template in the pack.
//
// A Template's compiled components are the merged set of everything every
// ancestor declares, nearest ancestor winning field by field, and its
// provenance names which ancestor each surviving value came from. That is the
// answer to "why does this Template have Aggro{threshold: 3}" — a question
// about a value that may appear in none of the files the Builder wrote, which
// is exactly what extends plus field-level merge produces (ADR-0010 decision 9).
func (r *resolver) resolveTemplates() []*contentv1.TemplateDefinition {
	var decls []templateDecl
	for _, f := range r.files {
		for _, d := range f.Decls {
			td, isTemplate := d.(*TemplateDecl)
			if !isTemplate {
				continue
			}
			decls = append(decls, templateDecl{file: f.Path, ref: r.pack + "." + td.Name, d: td})
		}
	}

	byRef := map[string]templateDecl{}
	var kept []templateDecl
	for _, td := range decls {
		if prev, dup := byRef[td.ref]; dup {
			// At the name, not the `template` keyword: the name is what
			// collides.
			r.report(td.file, td.d.NamePos, CodeDuplicateTemplate,
				fmt.Sprintf("Template %q is declared in %s and again in %s; a name is unique within its pack", td.ref, prev.file, td.file),
				td.ref)
			continue
		}
		byRef[td.ref] = td
		kept = append(kept, td)
	}

	// The core pack's Templates, reachable by a dotted reference and by
	// nothing else: a reference to any other pack is unresolved_extends
	// (semantics.md §4).
	core := map[string]*contentv1.TemplateDefinition{}
	if r.core != nil {
		for _, t := range r.core.Templates {
			core[t.GetName()] = t
		}
	}

	// Cycles are found before anything is built, and each is reported once.
	// Walking `extends` per Template would report the same cycle from every
	// member, and a Builder fixing one cycle should not read three findings
	// that describe it from three angles.
	inCycle := r.reportCycles(kept, byRef)

	out := make([]*contentv1.TemplateDefinition, 0, len(kept))
	for _, td := range kept {
		if inCycle[td.ref] {
			continue
		}
		def := r.buildTemplate(td, byRef, core)
		if def != nil {
			out = append(out, def)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// reportCycles finds every `extends` cycle among the pack's own Templates and
// reports each once, naming every Template in it.
//
// The chain is root-first like every other chain: for `A extends C`,
// `C extends B`, `B extends A` it reads p.A, p.B, p.C, p.A — each Template
// followed by the one that extends it, closing back on the start. "A extends B
// extends C extends A" is the only message that makes a cycle fixable
// (semantics.md §4), and it is reported at the lowest-named member so that two
// compiles of the same pack name the same one.
//
// Only local Templates can form a cycle: a core ancestor is already flattened
// in the cached pack and cannot reach back into the pack being compiled.
func (r *resolver) reportCycles(kept []templateDecl, byRef map[string]templateDecl) map[string]bool {
	parent := map[string]string{}
	for _, td := range kept {
		if len(td.d.Heads) != 1 || td.d.Heads[0].Extends == "" {
			continue
		}
		ref := td.d.Heads[0].Extends
		if strings.Contains(ref, ".") {
			continue // another pack; resolved, or reported, elsewhere
		}
		if q := r.pack + "." + ref; byRef[q].d != nil {
			parent[td.ref] = q
		}
	}

	inCycle := map[string]bool{}
	seenCycle := map[string]bool{}
	for _, td := range kept {
		members, ok := cycleFrom(td.ref, parent)
		if !ok {
			continue
		}
		key := cycleKey(members)
		for _, m := range members {
			inCycle[m] = true
		}
		if seenCycle[key] {
			continue
		}
		seenCycle[key] = true

		start := members[0]
		for _, m := range members {
			if m < start {
				start = m
			}
		}
		// The closed walk up from start, reversed: root-first, start to start.
		walk := []string{start}
		for cur := parent[start]; cur != start; cur = parent[cur] {
			walk = append(walk, cur)
		}
		walk = append(walk, start)
		chain := reverse(walk)
		at := byRef[start]
		r.report(at.file, at.d.NamePos, CodeExtendsCycle,
			fmt.Sprintf("Template %q is its own ancestor: %s", start, strings.Join(chain, " extends ")),
			chain...)
	}
	return inCycle
}

// cycleFrom reports whether ref lies on a cycle, and returns its members.
func cycleFrom(ref string, parent map[string]string) ([]string, bool) {
	seen := map[string]int{}
	var walk []string
	for cur := ref; ; {
		if i, ok := seen[cur]; ok {
			members := walk[i:]
			// Only a cycle that contains ref itself is ref's cycle; a Template
			// that merely extends into one is unresolvable for a different
			// reason and is left to the chain walk.
			for _, m := range members {
				if m == ref {
					return members, true
				}
			}
			return nil, false
		}
		seen[cur] = len(walk)
		walk = append(walk, cur)
		next, ok := parent[cur]
		if !ok {
			return nil, false
		}
		cur = next
	}
}

func cycleKey(members []string) string {
	sorted := append([]string{}, members...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

// head resolves the exactly-one-of rule. The grammar admits any number and
// semantics requires one, so that "expected { but found extends" is never what
// a Builder learning the model is told (grammar.ebnf, template_decl).
func (r *resolver) head(td templateDecl) (TemplateHead, bool) {
	switch len(td.d.Heads) {
	case 1:
		return td.d.Heads[0], true
	case 0:
		// At the Template name: there is no head token to point at.
		r.report(td.file, td.d.NamePos, CodeTemplateHead,
			fmt.Sprintf("Template %q states neither `kind` nor `extends`; a root states its kind and a subtype inherits its parent's", td.ref),
			td.ref)
	default:
		// At the second head: the first one is the one that could have stood.
		r.report(td.file, td.d.Heads[1].Pos, CodeTemplateHead,
			fmt.Sprintf("Template %q states both `kind` and `extends`; a root states its kind and a subtype inherits its parent's, because a chain has one kind", td.ref),
			td.ref)
	}
	return TemplateHead{}, false
}

// chainOf walks `extends` to the root, root first and self last. A root's chain
// is [self] (semantics.md §4).
//
// It returns the chain and the declarations behind each link, which the merge
// needs; a link in the core pack has no declaration here and contributes its
// compiled Components instead.
func (r *resolver) chainOf(td templateDecl, byRef map[string]templateDecl, core map[string]*contentv1.TemplateDefinition) ([]string, bool) {
	var chain []string
	seen := map[string]bool{}
	cur := td
	for {
		chain = append(chain, cur.ref)
		seen[cur.ref] = true
		h, ok := r.head(cur)
		if !ok {
			return nil, false
		}
		if h.Kind != "" {
			break // a root
		}
		parent, resolved := r.resolveExtends(cur, h, byRef, core)
		if !resolved {
			return nil, false
		}
		if seen[parent] {
			// Unreachable: reportCycles ran first and every member of a cycle
			// was skipped. Kept as a bound on the walk rather than as a
			// finding, because a walk with no bound is a hang.
			return nil, false
		}
		next, isLocal := byRef[parent]
		if !isLocal {
			// A core ancestor ends the walk: its own chain is already
			// flattened in the cached pack.
			chain = append(chain, reverse(coreChain(core, parent))...)
			break
		}
		cur = next
		if len(chain) > sim.MaxChainDepth+1 {
			break // bounded below, with the finding that names the whole chain
		}
	}
	chain = reverse(chain)
	if len(chain) > sim.MaxChainDepth {
		r.report(td.file, td.d.NamePos, CodeChainTooDeep,
			fmt.Sprintf("Template %q has an inheritance chain of depth %d; the bound is %d", td.ref, len(chain), sim.MaxChainDepth),
			chain...)
		return nil, false
	}
	return chain, true
}

// resolveExtends applies the v1 rule: a bare name resolves within the declaring
// pack, and a dotted one names another pack, which in v1 means andara.core and
// nothing else. A compiler that resolved more widely would emit content the
// server then refuses (semantics.md §4, AW-SRV-022 AC-3).
func (r *resolver) resolveExtends(td templateDecl, h TemplateHead, byRef map[string]templateDecl, core map[string]*contentv1.TemplateDefinition) (string, bool) {
	ref := h.Extends
	if !strings.Contains(ref, ".") {
		qualified := r.pack + "." + ref
		if _, ok := byRef[qualified]; ok {
			return qualified, true
		}
		r.report(td.file, h.RefPos, CodeUnresolvedExtends,
			fmt.Sprintf("Template %q extends %q, which pack %q does not declare", td.ref, ref, r.pack),
			td.ref)
		return "", false
	}
	pack := ref[:strings.LastIndex(ref, ".")]
	if pack != CorePack {
		r.report(td.file, h.RefPos, CodeUnresolvedExtends,
			fmt.Sprintf("Template %q extends %q, and a Template resolves within its own pack or %s and nowhere else", td.ref, ref, CorePack),
			td.ref)
		return "", false
	}
	if _, ok := core[ref]; !ok {
		r.report(td.file, h.RefPos, CodeUnresolvedExtends,
			fmt.Sprintf("Template %q extends %q, which %s does not declare", td.ref, ref, CorePack),
			td.ref)
		return "", false
	}
	return ref, true
}

// coreChain is a cached Template's own chain, self last.
func coreChain(core map[string]*contentv1.TemplateDefinition, ref string) []string {
	t, ok := core[ref]
	if !ok {
		return []string{ref}
	}
	return t.GetChain()
}

func reverse(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[len(in)-1-i] = s
	}
	return out
}

// value is one merged field with the Template that set it.
type mergedField struct {
	field *contentv1.ComponentField
	from  string
}

func (r *resolver) buildTemplate(td templateDecl, byRef map[string]templateDecl, core map[string]*contentv1.TemplateDefinition) *contentv1.TemplateDefinition {
	// A subtype may never remove. Substitutability is the point: anything
	// holding a SWORD keeps working when handed a FIRE_SWORD (ADR-0010
	// decision 5). The form exists only to be rejected, with the rule and the
	// off-switch alternative, because a language with no removal syntax would
	// make the rule unteachable (semantics.md §5).
	chain, ok := r.chainOf(td, byRef, core)
	if !ok {
		return nil
	}
	r.rejectRemoves(td, chain)

	kind := r.kindOf(td, chain, byRef, core)

	// Merge down the chain, root first, so the nearest ancestor is the last
	// writer and wins — field by field, never whole-Component. A subtype
	// states only what changes (ADR-0010 decision 4).
	merged := map[string]map[string]mergedField{}
	markers := map[string]string{}
	for _, ref := range chain {
		switch anc, isLocal := byRef[ref]; {
		case isLocal:
			ancChain := []string{ref}
			for _, c := range r.buildComponents(anc.file, anc.d.Components, ancChain) {
				mergeComponent(merged, markers, c, ref)
			}
		default:
			// A core Template is already flattened and carries provenance
			// naming the ancestor that actually set each field. Attributing
			// its fields to `ref` would credit the immediate core parent for a
			// value it inherited unchanged, which is the question provenance
			// exists to answer (semantics.md §5).
			anc := core[ref]
			from := map[string]string{}
			for _, pv := range anc.GetProvenance() {
				from[pv.GetComponent()+"\x00"+pv.GetField()] = pv.GetFrom()
			}
			for _, c := range anc.GetComponents() {
				mergeComponentFrom(merged, markers, c, ref, from)
			}
		}
	}

	def := &contentv1.TemplateDefinition{
		FormatVersion: FormatVersion,
		Name:          td.ref,
		Kind:          kind,
		Chain:         chain,
		Resolved:      true,
		Source: &contentv1.SourceRef{
			File: path.Join(r.packDirName(), td.file),
			Line: uint32(td.d.Pos.Line),
		},
	}
	def.Components, def.Provenance = flatten(merged, markers)
	return def
}

// mergeComponent folds one ancestor's Component into the merged set.
//
// Declaring a Component with an empty body is not a reset:
// `component andara.core.Behavior {}` on a subtype merges nothing and the
// ancestor's fields survive unchanged. It is how a Builder marks "this type is
// mine to fill in later" (semantics.md §5).
func mergeComponent(merged map[string]map[string]mergedField, markers map[string]string, c *contentv1.ComponentValue, from string) {
	mergeComponentFrom(merged, markers, c, from, nil)
}

// mergeComponentFrom is mergeComponent with a per-field attribution override,
// which a flattened ancestor supplies from its own provenance.
func mergeComponentFrom(merged map[string]map[string]mergedField, markers map[string]string, c *contentv1.ComponentValue, from string, byField map[string]string) {
	typ := c.GetType()
	if _, seen := markers[typ]; !seen {
		markers[typ] = from
	}
	if merged[typ] == nil {
		merged[typ] = map[string]mergedField{}
	}
	for _, f := range c.GetFields() {
		setter := from
		if byField != nil {
			if real, ok := byField[typ+"\x00"+f.GetName()]; ok {
				setter = real
			}
		}
		merged[typ][f.GetName()] = mergedField{field: f, from: setter}
	}
}

// flatten renders the merged set as the compiled Components and provenance,
// both sorted. A marker Component appears in components and in no provenance
// entry, because it sets no field and there is no field for an ancestor to have
// set (semantics.md §5).
func flatten(merged map[string]map[string]mergedField, markers map[string]string) ([]*contentv1.ComponentValue, []*contentv1.FieldProvenance) {
	types := make([]string, 0, len(markers))
	for t := range markers {
		types = append(types, t)
	}
	sort.Strings(types)

	comps := make([]*contentv1.ComponentValue, 0, len(types))
	var prov []*contentv1.FieldProvenance
	for _, t := range types {
		cv := &contentv1.ComponentValue{Type: t}
		names := make([]string, 0, len(merged[t]))
		for n := range merged[t] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			mf := merged[t][n]
			cv.Fields = append(cv.Fields, mf.field)
			prov = append(prov, &contentv1.FieldProvenance{Component: t, Field: n, From: mf.from})
		}
		comps = append(comps, cv)
	}
	sort.Slice(prov, func(i, j int) bool {
		if prov[i].GetComponent() != prov[j].GetComponent() {
			return prov[i].GetComponent() < prov[j].GetComponent()
		}
		return prov[i].GetField() < prov[j].GetField()
	})
	return comps, prov
}

// kindOf reads the kind off the root of the chain. A chain has one kind, so a
// subtype inherits its parent's rather than restating it (semantics.md §4).
func (r *resolver) kindOf(td templateDecl, chain []string, byRef map[string]templateDecl, core map[string]*contentv1.TemplateDefinition) contentv1.TemplateKind {
	root := chain[0]
	if local, isLocal := byRef[root]; isLocal {
		if h, ok := r.head(local); ok {
			return kindFromKeyword(h.Kind)
		}
		return contentv1.TemplateKind_TEMPLATE_KIND_UNSPECIFIED
	}
	if t, ok := core[root]; ok {
		return t.GetKind()
	}
	return contentv1.TemplateKind_TEMPLATE_KIND_UNSPECIFIED
}

func kindFromKeyword(k string) contentv1.TemplateKind {
	switch k {
	case "entity":
		return contentv1.TemplateKind_ENTITY
	case "item":
		return contentv1.TemplateKind_ITEM
	case "behavior":
		return contentv1.TemplateKind_BEHAVIOR
	}
	return contentv1.TemplateKind_TEMPLATE_KIND_UNSPECIFIED
}

// rejectRemoves reports every `remove` form, naming the ancestor that defined
// the Component or field, the substitutability rule, and the `enabled: false`
// alternative (semantics.md §5, errors.md removed_by_subtype).
func (r *resolver) rejectRemoves(td templateDecl, chain []string) {
	for _, rm := range td.d.Removes {
		what := fmt.Sprintf("Component %q", rm.Component)
		if rm.Field != "" {
			what = fmt.Sprintf("field %q of Component %q", rm.Field, rm.Component)
		}
		r.report(td.file, rm.Pos, CodeRemovedBySubtype,
			fmt.Sprintf("cannot remove %s; a subtype may override and extend but never remove, so that anything holding the parent keeps working — an off switch on the Component, such as `enabled: false`, is the way to disable it", what),
			chain...)
	}
}

// packDirName is the pack directory's own name, which SourceRef.file is
// prefixed with (semantics.md §6). It is provenance a finding quotes, not a
// path the loader opens.
func (r *resolver) packDirName() string { return dirBase(r.dir) }
