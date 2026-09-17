// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"fmt"
	"sort"
	"strings"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// TemplateRef names a Template: "<pack>.<Name>". The pack is everything
// before the last dot, so the reference carries its provenance the way a
// ComponentType does (ADR-0010 decision 6).
type TemplateRef string

// Pack is the Content Pack the reference belongs to, or "" for a name with
// no dot.
func (r TemplateRef) Pack() string {
	i := strings.LastIndexByte(string(r), '.')
	if i < 0 {
		return ""
	}
	return string(r[:i])
}

// TemplateKind is the open set of Game Object kinds (ADR-0010 decision 1).
type TemplateKind string

// The kinds named so far. The set is incomplete by Brian's own statement.
const (
	KindEntity   TemplateKind = "entity"
	KindItem     TemplateKind = "item"
	KindBehavior TemplateKind = "behavior"
)

// FieldProvenance records which ancestor set one Component field's value.
type FieldProvenance struct {
	Component ComponentType
	Field     string
	From      TemplateRef
}

// Template is a flattened Game Type as the sim holds it: no resolver, no
// parent pointer — the chain is data, and the Components are the merged
// result the compiler produced (ADR-0010 decision 9).
type Template struct {
	Ref        TemplateRef
	Kind       TemplateKind
	Chain      []TemplateRef     // root first, self last
	Components []Component       // sorted by type; at most one of each
	Provenance []FieldProvenance // sorted by (component, field)
	File       string            // the blob it was loaded from
	Source     string            // "file:line" of the declaration the compiler saw, or ""
}

// Pack is the Template's Content Pack.
func (t *Template) Pack() string { return t.Ref.Pack() }

// Parent is the Template this one extends, or "" for a root.
func (t *Template) Parent() TemplateRef {
	if len(t.Chain) < 2 {
		return ""
	}
	return t.Chain[len(t.Chain)-2]
}

// Component returns the Template's Component of type ct, if it carries one.
func (t *Template) Component(ct ComponentType) (Component, bool) {
	if t == nil {
		return Component{}, false
	}
	return findComponent(t.Components, ct)
}

// MaxChainDepth bounds an inheritance chain, self included (ADR-0010: "depth
// is bounded by a stated constant"). Exported so the compiler (AW-CLI-006)
// rejects at the same depth the loader would.
const MaxChainDepth = 16

// DefaultCorePack is the pack base Templates ship in (ADR-0010 decision 8).
// AW-SRV-013's content.core_pack names it in configuration; until then it
// is a TemplateOptions field with this default.
const DefaultCorePack = "andara.core"

// TemplateFormatVersion is the one format_version the loader accepts.
const TemplateFormatVersion = 1

// TemplateInput is one TemplateDefinition and where it came from.
type TemplateInput struct {
	File string
	Def  *contentv1.TemplateDefinition
}

// TemplateOptions configures BuildTemplates.
type TemplateOptions struct {
	// CorePack is the one pack every other pack may extend from. Empty
	// means DefaultCorePack.
	CorePack string
}

// TemplateRegistry is every Template the World was built with, keyed by
// reference. The sim reads it; nothing writes it after BuildTemplates.
type TemplateRegistry struct {
	corePack  string
	templates map[TemplateRef]*Template
	byPack    map[string][]TemplateRef
	// refused is every name whose definition failed pass one, by file, so a
	// chain finding can say "your parent was refused" rather than "not found".
	refused map[TemplateRef]string
}

// Get returns the Template named by ref.
func (r *TemplateRegistry) Get(ref TemplateRef) (*Template, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.templates[ref]
	return t, ok
}

// Pack returns the references in one pack, sorted.
func (r *TemplateRegistry) Pack(pack string) []TemplateRef {
	if r == nil {
		return nil
	}
	out := append([]TemplateRef(nil), r.byPack[pack]...)
	return out
}

// Packs returns every pack with at least one Template, sorted.
func (r *TemplateRegistry) Packs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byPack))
	for p := range r.byPack {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Len is the number of Templates.
func (r *TemplateRegistry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.templates)
}

// CorePack is the pack ancestors may be resolved from across pack boundaries.
func (r *TemplateRegistry) CorePack() string { return r.corePack }

// BuildTemplates validates flattened TemplateDefinitions and returns the
// registry, or nil with the findings that refused it. Every finding is
// reported, in input order, so a Builder fixes a pack in one pass.
//
// What is checked, in order per Template: the definition is well-formed and
// resolved; its Components are registered and sorted (the AW-SRV-021 rules,
// shared with Rooms and Zones); it is unique in its pack. Then across the
// set: every ancestor in a chain exists in the same pack or the core pack;
// the chain is the parent's chain plus itself, within MaxChainDepth, and of
// one kind; nothing an ancestor carries is missing here (ADR-0010 decision
// 5); and every provenance entry names a real field and a chain member.
func BuildTemplates(inputs []TemplateInput, opts TemplateOptions) (*TemplateRegistry, []ValidationError) {
	core := opts.CorePack
	if core == "" {
		core = DefaultCorePack
	}
	reg := &TemplateRegistry{corePack: core, templates: map[TemplateRef]*Template{}, byPack: map[string][]TemplateRef{}, refused: map[TemplateRef]string{}}
	var errs []ValidationError
	firstFile := map[TemplateRef]string{}

	// Pass one: each definition on its own.
	for _, in := range inputs {
		t, terrs := validateTemplate(in)
		errs = append(errs, terrs...)
		if t == nil {
			if name := TemplateRef(in.Def.GetName()); name != "" {
				reg.refused[name] = in.File
			}
			continue
		}
		if prev, dup := firstFile[t.Ref]; dup {
			errs = append(errs, ValidationError{File: in.File, Template: t.Ref, Code: ErrDuplicateTemplate,
				Detail: fmt.Sprintf("Template %q is declared in %s and again in %s; a name is unique within its pack", t.Ref, prev, in.File)})
			continue
		}
		firstFile[t.Ref] = in.File
		reg.templates[t.Ref] = t
	}

	// Pass two: the chains, against the set. Sorted by reference so two
	// loads report the same sequence.
	refs := make([]TemplateRef, 0, len(reg.templates))
	for ref := range reg.templates {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i] < refs[j] })
	for _, ref := range refs {
		errs = append(errs, reg.validateChain(reg.templates[ref])...)
	}

	fatal := false
	for _, e := range errs {
		if !IsWarning(e, false) {
			fatal = true
			break
		}
	}
	if fatal {
		return nil, errs
	}
	for _, ref := range refs {
		pack := ref.Pack()
		reg.byPack[pack] = append(reg.byPack[pack], ref)
	}
	return reg, errs
}

// sourceOf renders a definition's SourceRef as "file:line", or "".
func sourceOf(def *contentv1.TemplateDefinition) string {
	src := def.GetSource()
	if src == nil || src.GetFile() == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", src.GetFile(), src.GetLine())
}

// templateFinding is one finding on a Template. File is the blob the loader
// read; the declaration the compiler saw is a different file in a different
// language, so it goes in the detail as "declared at file:line" rather than
// in Line, where it would send a Builder to the wrong line of the JSON.
func templateFinding(file string, source string, ref TemplateRef, code ErrCode, detail string) ValidationError {
	if source != "" {
		detail += " (declared at " + source + ")"
	}
	return ValidationError{File: file, Template: ref, Code: code, Detail: detail}
}

// validateTemplate checks one definition in isolation.
func validateTemplate(in TemplateInput) (*Template, []ValidationError) {
	def := in.Def
	if def == nil {
		return nil, []ValidationError{{File: in.File, Code: ErrMalformed, Detail: "template definition is empty"}}
	}
	ref := TemplateRef(def.GetName())
	source := sourceOf(def)
	if ref == "" || ref.Pack() == "" || strings.HasSuffix(string(ref), ".") {
		return nil, []ValidationError{templateFinding(in.File, source, ref, ErrMalformed,
			fmt.Sprintf("template name %q must be <pack>.<Name>", def.GetName()))}
	}
	if v := def.GetFormatVersion(); v != TemplateFormatVersion {
		return nil, []ValidationError{templateFinding(in.File, source, ref, ErrUnsupportedVersion,
			fmt.Sprintf("Template %q has format_version %d; this server supports %d", ref, v, TemplateFormatVersion))}
	}
	if !def.GetResolved() {
		// ADR-0010 decision 9: the sim carries no resolver. An unresolved
		// Template is the compiler's output before its last stage, or a
		// hand-written file that skipped the compiler; either way it is not
		// something the server can make sense of.
		return nil, []ValidationError{templateFinding(in.File, source, ref, ErrUnflattenedTemplate,
			fmt.Sprintf("Template %q is not resolved; the server loads only compiler-flattened Templates (ADR-0010 decision 9)", ref))}
	}
	kind, ok := kindFromProto(def.GetKind())
	if !ok {
		return nil, []ValidationError{templateFinding(in.File, source, ref, ErrMalformed,
			fmt.Sprintf("Template %q has no kind; ENTITY, ITEM, or BEHAVIOR is required", ref))}
	}

	var errs []ValidationError
	chain := make([]TemplateRef, 0, len(def.GetChain()))
	for _, c := range def.GetChain() {
		chain = append(chain, TemplateRef(c))
	}
	switch {
	case len(chain) == 0:
		errs = append(errs, templateFinding(in.File, source, ref, ErrChainMismatch,
			fmt.Sprintf("Template %q has an empty chain; a root's chain is [self]", ref)))
	case chain[len(chain)-1] != ref:
		errs = append(errs, templateFinding(in.File, source, ref, ErrChainMismatch,
			fmt.Sprintf("Template %q has a chain ending in %q; the chain ends in the Template itself", ref, chain[len(chain)-1])))
	case len(chain) > MaxChainDepth:
		errs = append(errs, templateFinding(in.File, source, ref, ErrChainTooDeep,
			fmt.Sprintf("Template %q has an inheritance chain of depth %d; the bound is %d", ref, len(chain), MaxChainDepth)))
	}
	seenInChain := map[TemplateRef]bool{}
	for _, c := range chain {
		if seenInChain[c] {
			errs = append(errs, templateFinding(in.File, source, ref, ErrChainMismatch,
				fmt.Sprintf("Template %q names %q twice in its chain; inheritance is acyclic", ref, c)))
			break
		}
		seenInChain[c] = true
	}

	site := componentSite{file: in.File, what: fmt.Sprintf("Template %s", ref)}
	comps, cerrs := validateComponents(def.GetComponents(), site)
	for i := range cerrs {
		cerrs[i].Template = ref
		if source != "" {
			cerrs[i].Detail += " (declared at " + source + ")"
		}
	}
	errs = append(errs, cerrs...)

	prov := make([]FieldProvenance, 0, len(def.GetProvenance()))
	for _, p := range def.GetProvenance() {
		prov = append(prov, FieldProvenance{Component: ComponentType(p.GetComponent()), Field: p.GetField(), From: TemplateRef(p.GetFrom())})
	}
	sort.Slice(prov, func(i, j int) bool {
		if prov[i].Component != prov[j].Component {
			return prov[i].Component < prov[j].Component
		}
		return prov[i].Field < prov[j].Field
	})
	for _, p := range prov {
		c, ok := findComponent(comps, p.Component)
		if !ok {
			errs = append(errs, templateFinding(in.File, source, ref, ErrInvalidProvenance,
				fmt.Sprintf("Template %q records provenance for component %q, which it does not carry", ref, p.Component)))
			continue
		}
		if _, ok := c.Field(p.Field); !ok {
			errs = append(errs, templateFinding(in.File, source, ref, ErrInvalidProvenance,
				fmt.Sprintf("Template %q records provenance for %s.%s, a field it does not carry", ref, p.Component, p.Field)))
			continue
		}
		if !seenInChain[p.From] {
			errs = append(errs, templateFinding(in.File, source, ref, ErrInvalidProvenance,
				fmt.Sprintf("Template %q records %s.%s as set by %q, which is not in its chain", ref, p.Component, p.Field, p.From)))
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return &Template{Ref: ref, Kind: kind, Chain: chain, Components: comps, Provenance: prov, File: in.File, Source: source}, nil
}

// validateChain checks t against its ancestors, which must all be in the
// registry: in t's pack, or in the core pack.
func (r *TemplateRegistry) validateChain(t *Template) []ValidationError {
	var errs []ValidationError
	finding := func(code ErrCode, detail string) ValidationError {
		return templateFinding(t.File, t.Source, t.Ref, code, detail)
	}
	if len(t.Chain) < 2 {
		return nil
	}
	parentRef := t.Parent()
	if pack := parentRef.Pack(); pack != t.Pack() && pack != r.corePack {
		// The loader resolves across exactly one pack boundary: into core.
		// A Builder pack extending another Builder pack is a dependency the
		// content pipeline does not have (ADR-0010 decision 8).
		return []ValidationError{finding(ErrUnresolvedExtends,
			fmt.Sprintf("Template %q extends %q, which is in pack %q; a Template may extend one in its own pack or in %q, and the loader resolves nothing else",
				t.Ref, parentRef, pack, r.corePack))}
	}
	parent, ok := r.templates[parentRef]
	if !ok {
		if refusedFile, refused := r.refused[parentRef]; refused {
			// The parent is in the pack and was refused above; fixing it is
			// what fixes this, and the Builder should not go hunting for a
			// file that is right there.
			return []ValidationError{finding(ErrUnresolvedExtends,
				fmt.Sprintf("Template %q extends %q, which was refused (see the findings for %s)", t.Ref, parentRef, refusedFile))}
		}
		return []ValidationError{finding(ErrUnresolvedExtends,
			fmt.Sprintf("Template %q extends %q, which is not in pack %q or in %q", t.Ref, parentRef, t.Pack(), r.corePack))}
	}
	// The chain is the parent's chain plus self — the one structural fact
	// that makes every ancestor reachable and the hierarchy a tree.
	want := append(append([]TemplateRef(nil), parent.Chain...), t.Ref)
	if !equalRefs(t.Chain, want) {
		errs = append(errs, finding(ErrChainMismatch,
			fmt.Sprintf("Template %q has chain %v but its parent %q has chain %v; a chain is the parent's chain plus the Template itself",
				t.Ref, t.Chain, parentRef, parent.Chain)))
	}
	if parent.Kind != t.Kind {
		errs = append(errs, finding(ErrChainMismatch,
			fmt.Sprintf("Template %q is a %s but extends %q, a %s; a chain has one kind", t.Ref, t.Kind, parentRef, parent.Kind)))
	}
	// Never remove (ADR-0010 decision 5): everything the parent carries —
	// and so, by induction, everything any ancestor carries — is here.
	for _, pc := range parent.Components {
		c, ok := findComponent(t.Components, pc.Type)
		if !ok {
			errs = append(errs, finding(ErrChainMismatch,
				fmt.Sprintf("Template %q lacks component %q, which its ancestor %q carries; a subtype may override and extend but never remove (ADR-0010 decision 5)",
					t.Ref, pc.Type, parentRef)))
			continue
		}
		for _, pf := range pc.Fields {
			if _, ok := c.Field(pf.Name); !ok {
				errs = append(errs, finding(ErrChainMismatch,
					fmt.Sprintf("Template %q lacks field %s.%s, which its ancestor %q sets; a subtype may override but never remove (ADR-0010 decision 5)",
						t.Ref, pc.Type, pf.Name, parentRef)))
			}
		}
	}
	return errs
}

func equalRefs(a, b []TemplateRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func kindFromProto(k contentv1.TemplateKind) (TemplateKind, bool) {
	switch k {
	case contentv1.TemplateKind_ENTITY:
		return KindEntity, true
	case contentv1.TemplateKind_ITEM:
		return KindItem, true
	case contentv1.TemplateKind_BEHAVIOR:
		return KindBehavior, true
	}
	return "", false
}
