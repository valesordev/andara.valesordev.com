// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rawClientAllowlist names the files, outside this package, that may build a
// franz-go client directly, each with why it needs raw franz-go. It starts
// empty (AW-SRV-044 AC-1): a fixture built around the constructor is a client
// that never authenticates once a broker requires SASL.
var rawClientAllowlist = map[string]string{}

// kgoPath is the import path whose NewClient only this package may call.
const kgoPath = "github.com/twmb/franz-go/pkg/kgo"

// rawClientCalls is every call of kgo's NewClient in the file, under whatever
// name the file imports kgo as, by line.
func rawClientCalls(fset *token.FileSet, f *ast.File) []int {
	names := map[string]bool{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != kgoPath {
			continue
		}
		name := "kgo"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			name = ""
		}
		names[name] = true
	}
	if len(names) == 0 {
		return nil
	}
	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if id, ok := fn.X.(*ast.Ident); ok && names[id.Name] && fn.Sel.Name == "NewClient" {
				lines = append(lines, fset.Position(call.Pos()).Line)
			}
		case *ast.Ident:
			if names[""] && fn.Name == "NewClient" {
				lines = append(lines, fset.Position(call.Pos()).Line)
			}
		}
		return true
	})
	return lines
}

// AC-1: no Go file outside this package builds a franz-go client itself, test
// files included, every build tag included: each goes through New, which
// brings SASL. The allowlist is the only exemption.
func TestEveryKafkaClientGoesThroughTheConstructor(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root not found from %s: %v", root, err)
	}
	self := filepath.Join(root, "server", "kafkaclient")
	fset := token.NewFileSet()
	scanned := 0
	for _, dir := range []string{"server", "cmd", "internal", "admin"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == self {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Parsing ignores build constraints, so integration, smoke and
			// soak files are read like any other.
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			scanned++
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, ok := rawClientAllowlist[rel]; ok {
				return nil
			}
			for _, line := range rawClientCalls(fset, f) {
				t.Errorf("%s:%d calls kgo.NewClient; build the client with kafkaclient.New, which brings SASL", rel, line)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned %d Go files; the walk is not reading the tree", scanned)
	}
	for rel, why := range rawClientAllowlist {
		if why == "" {
			t.Errorf("allowlisted %s gives no reason", rel)
		}
	}
}

// The guard sees a call under kgo's own name, an alias and a dot import, and
// not another package's NewClient.
func TestRawClientCallsFindsEveryImportName(t *testing.T) {
	for name, src := range map[string]string{
		"plain": `package p
import "github.com/twmb/franz-go/pkg/kgo"
func f() { kgo.NewClient() }`,
		"alias": `package p
import franz "github.com/twmb/franz-go/pkg/kgo"
func f() { franz.NewClient() }`,
		"dot": `package p
import . "github.com/twmb/franz-go/pkg/kgo"
func f() { NewClient() }`,
	} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name+".go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := rawClientCalls(fset, f); len(got) != 1 || got[0] != 3 {
			t.Errorf("%s: calls at %v, want line 3", name, got)
		}
	}
	other := `package p
import "github.com/valesordev/andara/server/kafkaclient"
func f() { kafkaclient.New(kafkaclient.Config{}) }
type kgo struct{}
func (kgo) NewClient() {}
func g() { var k kgo; k.NewClient() }`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "other.go", other, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := rawClientCalls(fset, f); len(got) != 0 {
		t.Errorf("a file that doesn't import kgo: calls at %v", got)
	}
}
