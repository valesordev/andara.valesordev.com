// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// tdef builds a resolved definition in the compiler's output form.
func tdef(name string, kind contentv1.TemplateKind, chain []string, comps ...*contentv1.ComponentValue) *contentv1.TemplateDefinition {
	return &contentv1.TemplateDefinition{
		FormatVersion: TemplateFormatVersion,
		Name:          name,
		Kind:          kind,
		Chain:         chain,
		Components:    comps,
		Resolved:      true,
		Source:        &contentv1.SourceRef{File: name + ".aw", Line: 1},
	}
}

func strField(name, v string) *contentv1.ComponentField {
	return &contentv1.ComponentField{Name: name, Value: &contentv1.ComponentField_StringValue{StringValue: v}}
}

const (
	entity = contentv1.TemplateKind_ENTITY
	item   = contentv1.TemplateKind_ITEM
)

// core is the andara.core seed as inputs.
func core() []TemplateInput {
	return []TemplateInput{
		{File: "core/Entity.json", Def: tdef("andara.core.Entity", entity, []string{"andara.core.Entity"})},
		{File: "core/Npc.json", Def: tdef("andara.core.Npc", entity, []string{"andara.core.Entity", "andara.core.Npc"}, comp("andara.core.Memory"))},
		{File: "core/Item.json", Def: tdef("andara.core.Item", item, []string{"andara.core.Item"})},
	}
}

func merchant() TemplateInput {
	d := tdef("town.Merchant", entity, []string{"andara.core.Entity", "andara.core.Npc", "town.Merchant"},
		comp("andara.core.Memory"), comp("andara.core.Behavior", strField("name", "town.merchant")))
	d.Provenance = []*contentv1.FieldProvenance{{Component: "andara.core.Behavior", Field: "name", From: "town.Merchant"}}
	return TemplateInput{File: "town/Merchant.json", Def: d}
}

func errCodes(errs []ValidationError) []ErrCode {
	out := make([]ErrCode, len(errs))
	for i, e := range errs {
		out[i] = e.Code
	}
	return out
}

// AC-1: a compiled subtype loads with its flattened set and its chain.
func TestBuildTemplates_Merchant(t *testing.T) {
	reg, errs := BuildTemplates(append(core(), merchant()), TemplateOptions{})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	m, ok := reg.Get("town.Merchant")
	if !ok {
		t.Fatal("town.Merchant not in registry")
	}
	if !slices.Equal(m.Chain, []TemplateRef{"andara.core.Entity", "andara.core.Npc", "town.Merchant"}) {
		t.Errorf("chain %v", m.Chain)
	}
	if m.Kind != KindEntity || m.Pack() != "town" || m.Parent() != "andara.core.Npc" {
		t.Errorf("kind=%s pack=%s parent=%s", m.Kind, m.Pack(), m.Parent())
	}
	// Sorted by type: Behavior before Memory, whatever order the file had.
	if len(m.Components) != 2 || m.Components[0].Type != "andara.core.Behavior" || m.Components[1].Type != "andara.core.Memory" {
		t.Errorf("components %+v", m.Components)
	}
	// AC-6: the Behavior name is recorded, not validated.
	b, _ := m.Component("andara.core.Behavior")
	if f, _ := b.Field("name"); f.Str != "town.merchant" {
		t.Errorf("behavior name %q", f.Str)
	}
	if len(m.Provenance) != 1 || m.Provenance[0].From != "town.Merchant" {
		t.Errorf("provenance %+v", m.Provenance)
	}
	if got := reg.Pack("town"); !slices.Equal(got, []TemplateRef{"town.Merchant"}) {
		t.Errorf("Pack(town) = %v", got)
	}
	if got := reg.Packs(); !slices.Equal(got, []string{"andara.core", "town"}) {
		t.Errorf("Packs() = %v", got)
	}
	if reg.Len() != 4 || reg.CorePack() != DefaultCorePack {
		t.Errorf("len=%d core=%s", reg.Len(), reg.CorePack())
	}
}

// AC-2: an unregistered Component type names pack, Template, type, and the
// fact that types are server-defined.
func TestBuildTemplates_UnknownComponent(t *testing.T) {
	in := TemplateInput{File: "town/Bad.json", Def: tdef("town.Bad", entity, []string{"andara.core.Entity", "town.Bad"}, comp("town.Aggro"))}
	reg, errs := BuildTemplates(append(core(), in), TemplateOptions{})
	if reg != nil || !slices.Equal(errCodes(errs), []ErrCode{ErrUnknownComponent}) {
		t.Fatalf("reg=%v errs=%v", reg, errs)
	}
	e := errs[0]
	if e.Template != "town.Bad" || e.File != "town/Bad.json" || e.Line != 1 {
		t.Errorf("finding %+v", e)
	}
	for _, want := range []string{"town.Aggro", "Template town.Bad", "defined on the server"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, e.Detail)
		}
	}
}

// AC-3: an ancestor absent from the pack and the core, and one in another
// Builder pack, are both unresolved — the loader never resolves across a
// pack boundary other than into core.
func TestBuildTemplates_UnresolvedExtends(t *testing.T) {
	missing := TemplateInput{File: "town/Orphan.json", Def: tdef("town.Orphan", entity, []string{"andara.core.Entity", "andara.core.Dragon", "town.Orphan"})}
	reg, errs := BuildTemplates(append(core(), missing), TemplateOptions{})
	if reg != nil || !slices.Equal(errCodes(errs), []ErrCode{ErrUnresolvedExtends}) {
		t.Fatalf("errs=%v", errs)
	}
	for _, want := range []string{`"town.Orphan"`, `"andara.core.Dragon"`, `"town"`, `"andara.core"`} {
		if !strings.Contains(errs[0].Detail, want) {
			t.Errorf("detail lacks %s: %s", want, errs[0].Detail)
		}
	}

	// A Template in pack "docks" extends one in pack "town": both exist, and
	// it is still unresolved.
	base := TemplateInput{File: "town/Base.json", Def: tdef("town.Base", entity, []string{"andara.core.Entity", "town.Base"})}
	cross := TemplateInput{File: "docks/Sub.json", Def: tdef("docks.Sub", entity, []string{"andara.core.Entity", "town.Base", "docks.Sub"})}
	reg, errs = BuildTemplates(append(core(), base, cross), TemplateOptions{})
	if reg != nil || !slices.Equal(errCodes(errs), []ErrCode{ErrUnresolvedExtends}) || errs[0].Template != "docks.Sub" {
		t.Fatalf("cross-pack: errs=%v", errs)
	}
	if !strings.Contains(errs[0].Detail, "resolves nothing else") {
		t.Errorf("cross-pack detail: %s", errs[0].Detail)
	}

	// A different core pack name is honored.
	alt := TemplateInput{File: "alt/Root.json", Def: tdef("alt.Root", entity, []string{"alt.Root"})}
	sub := TemplateInput{File: "town/Sub.json", Def: tdef("town.Sub", entity, []string{"alt.Root", "town.Sub"})}
	if reg, errs := BuildTemplates([]TemplateInput{alt, sub}, TemplateOptions{CorePack: "alt"}); reg == nil || len(errs) != 0 {
		t.Fatalf("alt core: errs=%v", errs)
	}
}

// AC-4: the sim carries no resolver.
func TestBuildTemplates_Unflattened(t *testing.T) {
	d := tdef("town.Raw", entity, []string{"andara.core.Entity", "town.Raw"})
	d.Resolved = false
	reg, errs := BuildTemplates(append(core(), TemplateInput{File: "town/Raw.json", Def: d}), TemplateOptions{})
	if reg != nil || !slices.Equal(errCodes(errs), []ErrCode{ErrUnflattenedTemplate}) {
		t.Fatalf("errs=%v", errs)
	}
	if !strings.Contains(errs[0].Detail, "ADR-0010 decision 9") || !strings.Contains(errs[0].Detail, "declared at town.Raw.aw:1") {
		t.Errorf("detail: %s", errs[0].Detail)
	}
}

// AC-7: a name declared twice names both files.
func TestBuildTemplates_Duplicate(t *testing.T) {
	a := TemplateInput{File: "town/a.json", Def: tdef("town.Twin", entity, []string{"andara.core.Entity", "town.Twin"})}
	b := TemplateInput{File: "town/b.json", Def: tdef("town.Twin", entity, []string{"andara.core.Entity", "town.Twin"})}
	reg, errs := BuildTemplates(append(core(), a, b), TemplateOptions{})
	if reg != nil || !slices.Equal(errCodes(errs), []ErrCode{ErrDuplicateTemplate}) {
		t.Fatalf("errs=%v", errs)
	}
	if e := errs[0]; e.File != "town/b.json" || !strings.Contains(e.Detail, "town/a.json") || !strings.Contains(e.Detail, "town/b.json") {
		t.Errorf("finding %+v", e)
	}
}

// chain_mismatch and its relatives: every way a compiler-emitted chain can
// disagree with the set it was emitted into.
func TestBuildTemplates_ChainFindings(t *testing.T) {
	cases := map[string]struct {
		in   TemplateInput
		code ErrCode
		want string
	}{
		"missing ancestor component": {
			// Npc carries Memory; a subtype without it removed something.
			TemplateInput{File: "f", Def: tdef("town.Forgetful", entity, []string{"andara.core.Entity", "andara.core.Npc", "town.Forgetful"})},
			ErrChainMismatch, "never remove",
		},
		"chain not parent's chain plus self": {
			TemplateInput{File: "f", Def: tdef("town.Skip", entity, []string{"andara.core.Npc", "town.Skip"}, comp("andara.core.Memory"))},
			ErrChainMismatch, "parent's chain plus",
		},
		"kind differs from parent": {
			TemplateInput{File: "f", Def: tdef("town.Odd", item, []string{"andara.core.Entity", "town.Odd"})},
			ErrChainMismatch, "one kind",
		},
		"chain does not end in self": {
			TemplateInput{File: "f", Def: tdef("town.Off", entity, []string{"andara.core.Entity", "town.Other"})},
			ErrChainMismatch, "ends in the Template itself",
		},
		"empty chain": {
			TemplateInput{File: "f", Def: tdef("town.Empty", entity, nil)},
			ErrChainMismatch, "empty chain",
		},
		"repeated name": {
			TemplateInput{File: "f", Def: tdef("town.Loop", entity, []string{"town.Loop", "andara.core.Entity", "town.Loop"})},
			ErrChainMismatch, "acyclic",
		},
		"provenance for absent component": {
			func() TemplateInput {
				d := tdef("town.P1", entity, []string{"andara.core.Entity", "town.P1"})
				d.Provenance = []*contentv1.FieldProvenance{{Component: "andara.core.Behavior", Field: "name", From: "town.P1"}}
				return TemplateInput{File: "f", Def: d}
			}(),
			ErrInvalidProvenance, "does not carry",
		},
		"provenance from outside the chain": {
			func() TemplateInput {
				d := tdef("town.P2", entity, []string{"andara.core.Entity", "town.P2"}, comp("andara.core.Behavior", strField("name", "x")))
				d.Provenance = []*contentv1.FieldProvenance{{Component: "andara.core.Behavior", Field: "name", From: "town.Elsewhere"}}
				return TemplateInput{File: "f", Def: d}
			}(),
			ErrInvalidProvenance, "not in its chain",
		},
		"unsupported format version": {
			func() TemplateInput {
				d := tdef("town.Old", entity, []string{"andara.core.Entity", "town.Old"})
				d.FormatVersion = 2
				return TemplateInput{File: "f", Def: d}
			}(),
			ErrUnsupportedVersion, "format_version 2",
		},
		"no kind": {
			TemplateInput{File: "f", Def: tdef("town.Kindless", contentv1.TemplateKind_TEMPLATE_KIND_UNSPECIFIED, []string{"andara.core.Entity", "town.Kindless"})},
			ErrMalformed, "no kind",
		},
		"name without pack": {
			TemplateInput{File: "f", Def: tdef("Merchant", entity, []string{"Merchant"})},
			ErrMalformed, "<pack>.<Name>",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			reg, errs := BuildTemplates(append(core(), c.in), TemplateOptions{})
			if reg != nil {
				t.Fatal("registry built")
			}
			if len(errs) == 0 || errs[0].Code != c.code || !strings.Contains(errs[0].Detail, c.want) {
				t.Fatalf("want %s containing %q, got %v", c.code, c.want, errs)
			}
		})
	}

	// Depth: a chain of MaxChainDepth loads; one more does not.
	deep := core()
	chain := []string{"andara.core.Entity"}
	for i := 1; i < MaxChainDepth; i++ {
		name := "town.D" + strings.Repeat("x", i)
		chain = append(chain, name)
		deep = append(deep, TemplateInput{File: name, Def: tdef(name, entity, slices.Clone(chain))})
	}
	if reg, errs := BuildTemplates(deep, TemplateOptions{}); reg == nil || len(errs) != 0 {
		t.Fatalf("depth %d refused: %v", MaxChainDepth, errs)
	}
	chain = append(chain, "town.Deepest")
	deep = append(deep, TemplateInput{File: "deepest", Def: tdef("town.Deepest", entity, chain)})
	if _, errs := BuildTemplates(deep, TemplateOptions{}); len(errs) == 0 || errs[0].Code != ErrChainTooDeep {
		t.Fatalf("depth %d accepted: %v", MaxChainDepth+1, errs)
	}
}

// Every finding is reported, in a stable order, so a Builder fixes a pack
// in one pass.
func TestBuildTemplates_AllFindingsInOrder(t *testing.T) {
	inputs := append(core(),
		TemplateInput{File: "z.json", Def: tdef("town.Z", entity, []string{"andara.core.Entity", "town.Missing", "town.Z"})},
		TemplateInput{File: "a.json", Def: tdef("town.A", entity, []string{"andara.core.Entity", "town.A"}, comp("nope.Nope"))},
	)
	_, errs := BuildTemplates(inputs, TemplateOptions{})
	// Per-definition findings first in input order (a.json's unknown
	// component), then chain findings in reference order.
	if !slices.Equal(errCodes(errs), []ErrCode{ErrUnknownComponent, ErrUnresolvedExtends}) {
		t.Fatalf("codes %v", errCodes(errs))
	}
}

// AC-5: Instantiate is deterministic and its Components are sorted.
func TestInstantiate_Deterministic(t *testing.T) {
	reg, errs := BuildTemplates(append(core(), merchant()), TemplateOptions{})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	m, _ := reg.Get("town.Merchant")
	a := Instantiate(m, "npc-1", "town@7")
	b := Instantiate(m, "npc-1", "town@7")
	if !bytes.Equal(EntityCanonicalBytes(a), EntityCanonicalBytes(b)) {
		t.Fatal("two instantiations differ")
	}
	// AC-8: the Entity names its Template — and so its pack — and the
	// content version it came from.
	if a.Template != "town.Merchant" || a.Template.Pack() != "town" || a.ContentVersion != "town@7" || a.ID != "npc-1" {
		t.Errorf("entity %+v", a)
	}
	if !slices.IsSortedFunc(a.Components, func(x, y Component) int { return strings.Compare(string(x.Type), string(y.Type)) }) {
		t.Errorf("components not sorted: %+v", a.Components)
	}
	// The Entity owns its Components: mutating one does not reach the
	// Template or the sibling.
	a.Components[0].Fields[0].Str = "changed"
	if f, _ := b.Components[0].Field("name"); f.Str != "town.merchant" {
		t.Error("instantiations share field storage")
	}
	if f, _ := m.Components[0].Field("name"); f.Str != "town.merchant" {
		t.Error("instantiation shares storage with the Template")
	}
	// Different inputs, different bytes.
	c := Instantiate(m, "npc-2", "town@7")
	d := Instantiate(m, "npc-1", "town@8")
	if bytes.Equal(EntityCanonicalBytes(b), EntityCanonicalBytes(c)) || bytes.Equal(EntityCanonicalBytes(b), EntityCanonicalBytes(d)) {
		t.Error("distinct entities encode identically")
	}
	if got, ok := a.Component("andara.core.Memory"); !ok || got.Type != "andara.core.Memory" {
		t.Error("Memory not carried onto the Entity")
	}
	// A root with no Components instantiates to no Components.
	e, _ := reg.Get("andara.core.Entity")
	if ent := Instantiate(e, "x", "v"); ent.Components != nil {
		t.Errorf("empty template gave components %+v", ent.Components)
	}
}

func TestTemplateRef_Pack(t *testing.T) {
	for in, want := range map[TemplateRef]string{"andara.core.Npc": "andara.core", "town.Merchant": "town", "Merchant": "", "": ""} {
		if got := in.Pack(); got != want {
			t.Errorf("%q.Pack() = %q, want %q", in, got, want)
		}
	}
	var nilReg *TemplateRegistry
	if _, ok := nilReg.Get("x"); ok || nilReg.Len() != 0 || nilReg.Pack("x") != nil || nilReg.Packs() != nil {
		t.Error("nil registry is not empty")
	}
}
