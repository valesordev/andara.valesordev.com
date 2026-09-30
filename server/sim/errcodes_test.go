// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// AllErrCodes is a metric's closed label set, so a code declared and not
// listed would count on a series nobody pre-seeded.
func TestAllErrCodesListsEveryErrCode(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "ErrCode" {
				for _, n := range vs.Names {
					declared = append(declared, n.Name)
				}
			}
		}
	}
	listed := map[ErrCode]bool{}
	for _, c := range AllErrCodes {
		listed[c] = true
	}
	if len(declared) != len(AllErrCodes) {
		t.Errorf("errors.go declares %d ErrCodes and AllErrCodes lists %d", len(declared), len(AllErrCodes))
	}
	if slices.Contains(declared, "") || len(listed) != len(AllErrCodes) {
		t.Error("AllErrCodes lists a code twice")
	}
}
