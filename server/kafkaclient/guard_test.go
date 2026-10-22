// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"fmt"
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

// constructorDir is this package, relative to the repository root: the only
// directory whose own files may call kgo.NewClient. Its subpackages may not.
const constructorDir = "server/kafkaclient"

// skippedDirs are the directories the scan doesn't enter: tooling and
// dependencies, and testdata, which Go never builds.
var skippedDirs = map[string]bool{".git": true, ".claude": true, "vendor": true, "node_modules": true, "testdata": true}

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

// scanTree is every raw client call in the Go files under root, as
// "path:line" relative to root, outside constructorDir's own files and the
// allowlist; and how many files it parsed. Parsing ignores build constraints,
// so test files and integration, smoke and soak files are read like any
// other.
func scanTree(root string, allow map[string]string) (violations []string, scanned int, err error) {
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		if filepath.ToSlash(filepath.Dir(rel)) == constructorDir {
			return nil
		}
		if _, ok := allow[rel]; ok {
			return nil
		}
		for _, line := range rawClientCalls(fset, f) {
			violations = append(violations, fmt.Sprintf("%s:%d", rel, line))
		}
		return nil
	})
	return violations, scanned, err
}

// AC-1: no Go file in the repository outside this package's own files builds a
// franz-go client itself, test files and every build tag included: each goes
// through New, which brings SASL. The allowlist is the only exemption.
func TestEveryKafkaClientGoesThroughTheConstructor(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root not found from %s: %v", root, err)
	}
	violations, scanned, err := scanTree(root, rawClientAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d Go files; the walk is not reading the tree", scanned)
	}
	for _, v := range violations {
		t.Errorf("%s calls kgo.NewClient; build the client with kafkaclient.New, which brings SASL", v)
	}
	for rel, why := range rawClientAllowlist {
		if why == "" {
			t.Errorf("allowlisted %s gives no reason", rel)
		}
	}
}

// The walk itself, on a fixture tree: a raw client in a _test.go file, in a
// file behind a build tag, in a top-level directory outside server/, and in a
// subpackage of the constructor's package are all reported; the
// constructor's own files, testdata and an allowlisted file are not.
func TestScanTreeReportsEveryRawClient(t *testing.T) {
	root := t.TempDir()
	raw := "package p\n\nimport \"github.com/twmb/franz-go/pkg/kgo\"\n\nfunc f() { kgo.NewClient() }\n"
	tagged := "//go:build integration\n\n" + raw
	files := map[string]string{
		"server/x/x_test.go":                        raw,
		"server/x/y_integration_test.go":            tagged,
		"content/lang/z.go":                         raw,
		"server/kafkaclient/kafkatest/k.go":         raw,
		"server/kafkaclient/client.go":              raw,
		"server/x/testdata/fixture.go":              raw,
		"admin/allowed.go":                          raw,
		"server/x/clean.go":                         "package p\n",
		"server/kafkaclient/kafkatest/kafkatest.go": "package kafkatest\n",
	}
	for rel, src := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, scanned, err := scanTree(root, map[string]string{"admin/allowed.go": "a fixture"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"server/x/x_test.go:5":                true,
		"server/x/y_integration_test.go:7":    true,
		"content/lang/z.go:5":                 true,
		"server/kafkaclient/kafkatest/k.go:5": true,
	}
	if len(got) != len(want) {
		t.Fatalf("violations %v, want %v", got, want)
	}
	for _, v := range got {
		if !want[v] {
			t.Errorf("unexpected violation %s", v)
		}
	}
	if scanned != len(files)-1 { // testdata isn't parsed
		t.Errorf("scanned %d files, want %d", scanned, len(files)-1)
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
