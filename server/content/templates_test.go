// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/valesordev/andara/server/sim"
)

// tfixture is a testdata path outside testdata/content/, which fixture()
// is rooted in.
func tfixture(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The shared fixture loads as a whole: seven Templates in two packs, with
// the town pack extending the core (AC-1 end to end through the dir loader).
func TestLoadTemplatesDir_Fixture(t *testing.T) {
	inputs, errs := LoadTemplatesDir(tfixture(t, "templates"))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(inputs) != 7 {
		t.Fatalf("%d inputs, want 7", len(inputs))
	}
	// Sorted by file name, so two loads report findings in one order.
	for i := 1; i < len(inputs); i++ {
		if inputs[i-1].File > inputs[i].File {
			t.Fatalf("inputs not sorted: %s after %s", inputs[i].File, inputs[i-1].File)
		}
	}
	reg, errs := sim.BuildTemplates(inputs, sim.TemplateOptions{})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if reg.Len() != 7 || !slices.Equal(reg.Packs(), []string{"andara.core", "town"}) {
		t.Fatalf("len=%d packs=%v", reg.Len(), reg.Packs())
	}
	if got := reg.Pack("town"); !slices.Equal(got, []sim.TemplateRef{"town.Guard", "town.Lantern", "town.Merchant"}) {
		t.Errorf("town = %v", got)
	}
	m, _ := reg.Get("town.Merchant")
	if !slices.Equal(m.Chain, []sim.TemplateRef{"andara.core.Entity", "andara.core.Npc", "town.Merchant"}) {
		t.Errorf("chain %v", m.Chain)
	}
	if m.File != filepath.Join(tfixture(t, "templates"), "templates", "town.Merchant.json") {
		t.Errorf("file %s", m.File)
	}
	lantern, _ := reg.Get("town.Lantern")
	if lantern.Kind != sim.KindItem || lantern.Parent() != "andara.core.Item" {
		t.Errorf("lantern %+v", lantern)
	}
}

// A content directory without templates/ has no Templates, which is legal.
func TestLoadTemplatesDir_Absent(t *testing.T) {
	inputs, errs := LoadTemplatesDir(fixture(t, "valid"))
	if inputs != nil || errs != nil {
		t.Fatalf("inputs=%v errs=%v", inputs, errs)
	}
}

func TestLoadTemplatesDir_Findings(t *testing.T) {
	cases := map[string]struct {
		code sim.ErrCode
		line int
	}{
		"malformed":          {sim.ErrMalformed, 2},
		"unflattened":        {sim.ErrUnflattenedTemplate, 0},
		"duplicate":          {sim.ErrDuplicateTemplate, 0},
		"unknown-component":  {sim.ErrUnknownComponent, 0},
		"unresolved-extends": {sim.ErrUnresolvedExtends, 0},
		"chain-mismatch":     {sim.ErrChainMismatch, 0},
		"chain-too-deep":     {sim.ErrChainTooDeep, 0},
		"invalid-provenance": {sim.ErrInvalidProvenance, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			inputs, errs := LoadTemplatesDir(tfixture(t, "templates-invalid", name))
			if len(errs) == 0 {
				_, errs = sim.BuildTemplates(inputs, sim.TemplateOptions{})
			}
			if len(errs) != 1 || errs[0].Code != c.code {
				t.Fatalf("errs = %v, want one %s", errs, c.code)
			}
			if errs[0].Line != c.line || errs[0].File == "" {
				t.Errorf("finding %+v", errs[0])
			}
		})
	}
}

func TestLoadTemplates_Sources(t *testing.T) {
	if _, errs := LoadTemplates(SourceKafka, ""); len(errs) != 1 || errs[0].Code != sim.ErrEmptyContent {
		t.Errorf("kafka: %v", errs)
	}
	if _, errs := LoadTemplates("s3", ""); len(errs) != 1 || errs[0].Code != sim.ErrMalformed {
		t.Errorf("s3: %v", errs)
	}
	if inputs, errs := LoadTemplates(SourceDir, tfixture(t, "templates")); len(errs) != 0 || len(inputs) != 7 {
		t.Errorf("dir: %d inputs, %v", len(inputs), errs)
	}
}

// The fixture's copy of the andara.core seed must be byte-identical to
// content/core/templates/, which is what `make deploy` publishes
// (AW-INF-007). A drift here would mean tests pass against a core the server
// never ships.
func TestCoreSeedMatchesFixture(t *testing.T) {
	seed, err := filepath.Abs(filepath.Join("..", "..", "content", "core", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(seed)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		n++
		want, err := os.ReadFile(filepath.Join(seed, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(tfixture(t, "templates"), "templates", e.Name()))
		if err != nil {
			t.Fatalf("fixture lacks %s: %v", e.Name(), err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("testdata/templates/templates/%s differs from content/core/templates/%s", e.Name(), e.Name())
		}
	}
	if n != 4 {
		t.Errorf("core seed has %d templates, want 4 (Entity, Character, Npc, Item)", n)
	}
	// And the seed loads on its own as a complete pack.
	inputs, errs := LoadTemplatesDir(filepath.Dir(seed))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	reg, errs := sim.BuildTemplates(inputs, sim.TemplateOptions{})
	if len(errs) != 0 || reg.Len() != 4 {
		t.Fatalf("seed: len=%d errs=%v", reg.Len(), errs)
	}
}
