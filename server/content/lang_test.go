// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/valesordev/andara/content/lang"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
)

// TestPublishGateCanImportTheCompiler is the premise of AW-CLI-006 AC-8, from
// the side that does the importing.
//
// AW-SRV-013's publish gate compiles a submitted pack and compares its
// diagnostics with the CLI's, so `server/content` has to be able to import
// `content/lang` — which means the compiler may not import `server/content`
// back, and may not drag a Kafka client or an object store in behind it. The
// depguard rule and TestImportsNothingFromServerButSim hold that from the other
// side; this file is the import itself, so that the day AW-SRV-013 lands it is
// wiring a package that is already reachable rather than discovering it is not.
//
// It also checks the thing the gate exists to check: that what the compiler
// emits, the loader loads. The loader's `unflattened_template`,
// `chain_mismatch`, and `invalid_provenance` are the server checking the
// compiler (errors.md §3.5), and a compiler tripping one would be emitting
// content nothing can load.
func writeUnder(t *testing.T, root, rel string, body []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPublishGateCanImportTheCompiler(t *testing.T) {
	const corpus = "../../docs/specs/content-language/v1/corpus"

	core, ds := lang.Compile(filepath.Join(corpus, "valid", "core"), nil, nil)
	if core == nil {
		t.Fatalf("the core pack does not compile: %v", ds)
	}
	out, ds := lang.Compile(filepath.Join(corpus, "valid", "town"),
		&lang.Pack{Name: lang.CorePack, Version: 1, Templates: core.Templates}, nil)
	if out == nil {
		t.Fatalf("the town pack does not compile: %v", ds)
	}

	// The compiled blobs go through the loader's own parser, not through a
	// shortcut: the gate reads what a publish would store.
	dir := t.TempDir()
	for _, b := range out.Blobs {
		if b.MediaType != lang.BlobMediaType {
			continue
		}
		writeUnder(t, dir, b.Path, b.Bytes)
	}
	for _, t2 := range core.Templates {
		writeUnder(t, dir, filepath.Join("templates", t2.GetName()+".json"), lang.CanonicalJSON(t2))
	}

	templates, errs := content.LoadTemplatesDir(filepath.Join(dir, "templates"))
	if len(errs) > 0 {
		t.Fatalf("the loader could not read the compiled Templates: %v", errs)
	}
	if _, errs := sim.BuildTemplates(templates, sim.TemplateOptions{}); len(errs) > 0 {
		for _, e := range errs {
			if !sim.IsWarning(e, false) {
				t.Errorf("the loader refused a compiled Template: %s", e.Error())
			}
		}
	}

	zones, errs := content.LoadDir(dir)
	if len(errs) > 0 {
		t.Fatalf("the loader could not read the compiled Zones: %v", errs)
	}
	world, errs := sim.BuildWorld(zones, sim.Options{Source: "dir:" + dir})
	for _, e := range errs {
		if !sim.IsWarning(e, false) {
			t.Errorf("the loader refused a compiled Zone: %s", e.Error())
		}
	}
	if world == nil {
		t.Fatal("the compiled World did not build")
	}
	if _, ok := world.Resolve(sim.RoomRef{Zone: "town", Room: "plaza"}); !ok {
		t.Error("town/plaza is not resolvable in the World the compiler produced")
	}
}
