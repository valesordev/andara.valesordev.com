// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang_test

import (
	"path/filepath"
	"testing"

	"github.com/valesordev/andara/content/lang"
	"github.com/valesordev/andara/server/sim"
)

const corpus = "../../docs/specs/content-language/v1/corpus"

func compileCore(t *testing.T) *lang.Pack {
	t.Helper()
	out, ds := lang.Compile(filepath.Join(corpus, "valid", "core"), nil, nil)
	if out == nil {
		t.Fatalf("corpus/valid/core does not compile: %v", ds)
	}
	return &lang.Pack{Name: lang.CorePack, Version: 1, Templates: out.Templates}
}

// assertLoads compiles a pack and puts the result through the loader, which is
// the server's own validation of Zones and flattened Templates.
func assertLoads(t *testing.T, caseName string, core *lang.Pack) {
	t.Helper()
	dir := filepath.Join(corpus, caseName)
	out, ds := lang.Compile(dir, core, nil)
	if out == nil {
		t.Fatalf("%s does not compile: %v", caseName, ds)
	}

	var tin []sim.TemplateInput
	for _, tmpl := range out.Templates {
		tin = append(tin, sim.TemplateInput{File: tmpl.GetName() + ".json", Def: tmpl})
	}
	if core != nil {
		for _, tmpl := range core.Templates {
			tin = append(tin, sim.TemplateInput{File: tmpl.GetName() + ".json", Def: tmpl})
		}
	}
	if _, errs := sim.BuildTemplates(tin, sim.TemplateOptions{}); len(errs) > 0 {
		for _, e := range errs {
			if !sim.IsWarning(e, false) {
				t.Errorf("%s: the loader refused a compiled Template: %s", caseName, e.Error())
			}
		}
	}

	if len(out.Zones) == 0 {
		return
	}
	var zin []sim.Input
	for _, z := range out.Zones {
		zin = append(zin, sim.Input{File: z.GetId() + ".json", Def: z})
	}
	if _, errs := sim.BuildWorld(zin, sim.Options{Source: caseName}); len(errs) > 0 {
		for _, e := range errs {
			if !sim.IsWarning(e, false) {
				t.Errorf("%s: the loader refused a compiled Zone: %s", caseName, e.Error())
			}
		}
	}
}
