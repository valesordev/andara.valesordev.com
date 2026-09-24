// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AW-SRV-019's Definition of done: the projector is a replica of the
// simulation, not a second implementation of it. It imports server/sim, the
// binary hands the replica sim.Handlers() — the server's own table — and
// neither package names sim.ApplyContext or sim.Apply, which is what a handler
// of its own would have to. The depguard rule state-projector-is-a-replica
// holds the import half; this holds the half an import list cannot show.
func TestReplicaHasNoApplyOfItsOwn(t *testing.T) {
	t.Parallel()
	dirs := []string{".", filepath.Join("..", "..", "cmd", "andara-projector")}
	importsSim := map[string]bool{}
	handlers := false
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, im := range f.Imports {
				if im.Path.Value == `"github.com/valesordev/andara/server/sim"` {
					importsSim[dir] = true
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "sim" {
					switch sel.Sel.Name {
					case "ApplyContext", "Apply":
						t.Errorf("%s names sim.%s: the projector must run the server's handlers, not define its own", path, sel.Sel.Name)
					case "Handlers":
						handlers = true
					}
				}
				return true
			})
		}
	}
	for _, dir := range dirs {
		if !importsSim[dir] {
			t.Errorf("%s does not import server/sim", dir)
		}
	}
	if !handlers {
		t.Error("nothing in the projector hands the replica sim.Handlers()")
	}
}
