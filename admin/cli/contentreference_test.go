// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
	"github.com/valesordev/andara/server/sim"
)

// referenceGolden compares out with testdata/reference/<name>, rewriting it
// under -update or UPDATE_GOLDENS=1.
func referenceGolden(t *testing.T, name, out string) {
	t.Helper()
	path := filepath.Join("testdata", "reference", name)
	if *updateGoldens || os.Getenv("UPDATE_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if string(want) != out {
		t.Fatalf("%s changed; rerun with -update if intended.\n--- want\n%s\n--- got\n%s", path, want, out)
	}
}

func referenceJSON(t *testing.T) reference {
	t.Helper()
	res := runCLI(t, []string{"content", "reference", "--output", "json"}, nil)
	if res.exit != ExitOK || res.stderr != "" {
		t.Fatalf("exit %d, stderr %q", res.exit, res.stderr)
	}
	dec := json.NewDecoder(strings.NewReader(res.stdout))
	dec.DisallowUnknownFields()
	var ref reference
	if err := dec.Decode(&ref); err != nil {
		t.Fatalf("stdout is not the reference object: %v\n%s", err, res.stdout)
	}
	if dec.More() {
		t.Fatalf("stdout has more than one object: %q", res.stdout)
	}
	return ref
}

// AC-1, AC-8: no config, no credentials, no network: exit 0, one JSON object
// on stdout and nothing on stderr, byte-identical across runs and equal to the
// golden file.
func TestContentReference_JSONIsStableAndGolden(t *testing.T) {
	t.Parallel()
	a := runCLI(t, []string{"content", "reference", "--output", "json"}, nil)
	b := runCLI(t, []string{"content", "reference", "--output", "json"}, nil)
	if a.exit != ExitOK || a.stderr != "" || a.stdout != b.stdout {
		t.Fatalf("exit %d, stderr %q, identical %t", a.exit, a.stderr, a.stdout == b.stdout)
	}
	referenceJSON(t)
	referenceGolden(t, "reference.json", a.stdout)
}

// AC-9: --output human, and the default with no configured output, print the
// four headed sections, with the same rows as the JSON.
func TestContentReference_Human(t *testing.T) {
	t.Parallel()
	explicit := runCLI(t, []string{"content", "reference", "--output", "human"}, nil)
	byDefault := runCLI(t, []string{"content", "reference"}, nil)
	if explicit.exit != ExitOK || byDefault.exit != ExitOK || explicit.stdout != byDefault.stdout {
		t.Fatalf("exits %d/%d, same output %t", explicit.exit, byDefault.exit, explicit.stdout == byDefault.stdout)
	}
	ref := referenceJSON(t)
	out := explicit.stdout
	heads := []string{"Directions\n", "Component types\n", "Core (andara.core@" + strconv.FormatUint(core.Version(), 10) + ")\n", "Diagnostics\n"}
	at := -1
	for _, h := range heads {
		i := strings.Index(out, h)
		if i <= at {
			t.Fatalf("heading %q missing or out of order:\n%s", h, out)
		}
		at = i
	}
	rows := 0
	for l := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(strings.TrimSpace(l), strings.ToUpper(strings.Fields(l)[0])) {
			rows++
		}
	}
	if want := len(ref.Directions) + len(ref.ComponentTypes) + len(ref.Core.Templates) + len(ref.Diagnostics); rows != want {
		t.Fatalf("%d rows in the human output, %d in the JSON", rows, want)
	}
	referenceGolden(t, "reference.txt", out)
}

// AC-2: every Direction, in sim.Directions() order, with its reverse.
func TestContentReference_Directions(t *testing.T) {
	t.Parallel()
	ref := referenceJSON(t)
	ds := sim.Directions()
	if len(ref.Directions) != len(ds) || len(ds) != 12 {
		t.Fatalf("%d directions, sim has %d", len(ref.Directions), len(ds))
	}
	for i, d := range ds {
		rev, _ := d.Reverse()
		if ref.Directions[i] != (referenceDirection{Name: string(d), Reverse: string(rev)}) {
			t.Fatalf("direction %d: %+v, want %s/%s", i, ref.Directions[i], d, rev)
		}
	}
}

// AC-3: one entry per registered Component type, sorted, with its fields and
// their kinds; a marker has an empty list.
func TestContentReference_ComponentTypes(t *testing.T) {
	t.Parallel()
	ref := referenceJSON(t)
	types := sim.ComponentTypes()
	if len(ref.ComponentTypes) != len(types) {
		t.Fatalf("%d component types, sim registers %d", len(ref.ComponentTypes), len(types))
	}
	if !sort.SliceIsSorted(ref.ComponentTypes, func(i, j int) bool { return ref.ComponentTypes[i].Type < ref.ComponentTypes[j].Type }) {
		t.Fatal("component types are not sorted by type")
	}
	byType := map[string]referenceComponent{}
	for _, c := range ref.ComponentTypes {
		byType[c.Type] = c
	}
	for _, ty := range types {
		c, ok := byType[string(ty)]
		if !ok {
			t.Fatalf("component type %s is missing from the reference", ty)
		}
		names := sim.ComponentFieldNames(ty)
		sort.Strings(names)
		if c.Fields == nil || len(c.Fields) != len(names) {
			t.Fatalf("%s: fields %v, sim has %v", ty, c.Fields, names)
		}
		for i, n := range names {
			k, _ := sim.ComponentFieldKind(ty, n)
			if c.Fields[i] != (referenceField{Name: n, Kind: fieldKindName(k)}) || c.Fields[i].Kind == "unknown" {
				t.Fatalf("%s field %d: %+v", ty, i, c.Fields[i])
			}
		}
	}
}

// AC-4: the embedded core's VERSION and every Template, sorted, each with its
// kind keyword and its chain root-first, ending with itself.
func TestContentReference_Core(t *testing.T) {
	t.Parallel()
	ref := referenceJSON(t)
	if ref.Core.Pack != core.Pack || ref.Core.Version != core.Version() {
		t.Fatalf("core %s@%d, the binary embeds %s@%d", ref.Core.Pack, ref.Core.Version, core.Pack, core.Version())
	}
	if len(ref.Core.Templates) != len(core.Blobs()) || len(ref.Core.Templates) == 0 {
		t.Fatalf("%d templates, the core embeds %d", len(ref.Core.Templates), len(core.Blobs()))
	}
	for i, tpl := range ref.Core.Templates {
		if i > 0 && ref.Core.Templates[i-1].Name >= tpl.Name {
			t.Fatal("templates are not sorted by name")
		}
		if !slices.Contains([]string{"entity", "item", "behavior"}, tpl.Kind) {
			t.Fatalf("%s: kind %q", tpl.Name, tpl.Kind)
		}
		if n := len(tpl.Chain); n == 0 || tpl.Chain[n-1] != tpl.Name {
			t.Fatalf("%s: chain %v does not end with the Template", tpl.Name, tpl.Chain)
		}
	}
}

// parsedCodes reads the diagnostic codes from source, not from a list kept by
// hand (AC-5, AC-7). Each package is type-checked, so a code is found however
// it is spelled: every package-level constant or variable of type
// sim.ErrCode in sim, and every package-level Code* of string type in lang,
// each with the value the type checker folds. One whose value isn't a
// constant string fails the test rather than being skipped.
//
// Only sim and lang are checked, once per test binary: lang is given the sim
// just checked, and every other import an empty package. The type errors that
// leaves (a selector on an empty package) are collected and ignored, since no
// code depends on them, and a code that did would fail to fold and fail the
// test.
func parsedCodes(t *testing.T) (simCodes, langCodes map[string]string) {
	t.Helper()
	codesOnce.Do(func() {
		codesSim, codesLang = map[string]string{}, map[string]string{}
		const simPath = "github.com/valesordev/andara/server/sim"
		simPkg, err := codeCheck(filepath.Join("..", "..", "server", "sim"), simPath, nil,
			func(name string, typ types.Type, init ast.Expr) bool {
				if n, ok := types.Unalias(typ).(*types.Named); ok {
					return n.Obj().Name() == "ErrCode" && n.Obj().Pkg().Path() == simPath
				}
				// Untyped in the source and unresolved under a stubbed
				// import (var ErrX = ErrCode(fmt.Sprint(...))): a code if its
				// initializer names ErrCode, so it fails rather than being
				// skipped. Other unresolved objects (errors.New values)
				// don't name it.
				b, ok := typ.Underlying().(*types.Basic)
				return ok && b.Kind() == types.Invalid && mentions(init, "ErrCode")
			}, codesSim)
		if err == nil {
			_, err = codeCheck(filepath.Join("..", "..", "content", "lang"), "github.com/valesordev/andara/content/lang", simPkg,
				func(name string, typ types.Type, _ ast.Expr) bool {
					// A Code* whose type couldn't be determined (it
					// calls into a stubbed import) counts too: it folds to
					// no constant and fails, rather than being skipped.
					b, ok := typ.Underlying().(*types.Basic)
					return strings.HasPrefix(name, "Code") && ok && (b.Info()&types.IsString != 0 || b.Kind() == types.Invalid)
				}, codesLang)
		}
		codesErr = err
	})
	if codesErr != nil {
		t.Fatal(codesErr)
	}
	if len(codesSim) == 0 || len(codesLang) == 0 {
		t.Fatalf("found %d sim codes and %d lang codes", len(codesSim), len(codesLang))
	}
	return codesSim, codesLang
}

var (
	codesOnce           sync.Once
	codesSim, codesLang map[string]string
	codesErr            error
)

// mentions reports whether e names the identifier name anywhere.
func mentions(e ast.Expr, name string) bool {
	found := false
	if e != nil {
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				found = true
			}
			return !found
		})
	}
	return found
}

// stubImporter gives the package being checked sim, when it imports it, and
// an empty package for anything else.
type stubImporter struct{ sim *types.Package }

func (s stubImporter) Import(path string) (*types.Package, error) {
	if s.sim != nil && path == s.sim.Path() {
		return s.sim, nil
	}
	pkg := types.NewPackage(path, filepath.Base(path))
	pkg.MarkComplete()
	return pkg, nil
}

// codeCheck type-checks the package in dir (its non-test files, as go/build
// selects them) and records, for every package-level const or var that
// isCode accepts, its folded string value.
func codeCheck(dir, path string, sim *types.Package, isCode func(string, types.Type, ast.Expr) bool, into map[string]string) (*types.Package, error) {
	bp, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	for _, name := range bp.GoFiles {
		af, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, af)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: stubImporter{sim: sim}, Error: func(error) {}}
	pkg, _ := conf.Check(path, fset, parsed, info)
	for _, f := range parsed {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || (g.Tok != token.CONST && g.Tok != token.VAR) {
				continue
			}
			for _, sp := range g.Specs {
				spec := sp.(*ast.ValueSpec)
				for i, id := range spec.Names {
					obj := info.Defs[id]
					var init ast.Expr
					if i < len(spec.Values) {
						init = spec.Values[i]
					}
					if obj == nil || !isCode(id.Name, obj.Type(), init) {
						continue
					}
					var val constant.Value
					switch o := obj.(type) {
					case *types.Const:
						val = o.Val()
					case *types.Var:
						if i < len(spec.Values) {
							val = info.Types[spec.Values[i]].Value
						}
					}
					if val == nil || val.Kind() != constant.String {
						return nil, fmt.Errorf("%s.%s looks like a diagnostic code, but the checker could not resolve it to a constant string", path, id.Name)
					}
					into[constant.StringVal(val)] = id.Name
				}
			}
		}
	}
	return pkg, nil
}

// AC-5, AC-7: the diagnostics table holds exactly the codes the source
// declares, once each, sorted, and each one's raised_by follows from which
// package's constants hold its value.
func TestContentReference_DiagnosticsAreComplete(t *testing.T) {
	t.Parallel()
	simCodes, langCodes := parsedCodes(t)
	ref := referenceJSON(t)
	listed := map[string]referenceDiag{}
	for i, d := range ref.Diagnostics {
		if i > 0 && ref.Diagnostics[i-1].Code >= d.Code {
			t.Fatalf("diagnostics are not sorted, or %s repeats", d.Code)
		}
		listed[d.Code] = d
	}
	for v, name := range simCodes {
		if _, ok := listed[v]; !ok {
			t.Errorf("sim.%s (%q) is missing from the reference", name, v)
		}
	}
	for v, name := range langCodes {
		if _, ok := listed[v]; !ok {
			t.Errorf("lang.%s (%q) is missing from the reference", name, v)
		}
	}
	for v, d := range listed {
		_, inSim := simCodes[v]
		_, inLang := langCodes[v]
		var want string
		switch {
		case inSim && inLang:
			want = lang.RaisedByBoth
		case inSim:
			want = lang.RaisedByLoader
		case inLang:
			want = lang.RaisedByCompiler
		default:
			t.Errorf("the reference names %q, which no sim or lang constant holds", v)
			continue
		}
		if d.RaisedBy != want {
			t.Errorf("%s: raised_by %q, the source says %q", v, d.RaisedBy, want)
		}
	}
}

// AC-6: orphan_room and missing_reverse_exit are warnings, as sim's warning
// set says, and every other code is an error.
func TestContentReference_WarningsAreSims(t *testing.T) {
	t.Parallel()
	simCodes, _ := parsedCodes(t)
	ref := referenceJSON(t)
	var warnings []string
	for _, d := range ref.Diagnostics {
		switch d.Severity {
		case lang.SeverityNameWarning:
			warnings = append(warnings, d.Code)
		case lang.SeverityNameError:
		default:
			t.Fatalf("%s: severity %q", d.Code, d.Severity)
		}
	}
	var simWarnings []string
	for v := range simCodes {
		if sim.IsWarning(sim.ValidationError{Code: sim.ErrCode(v)}, false) {
			simWarnings = append(simWarnings, v)
		}
	}
	sort.Strings(simWarnings)
	if !slices.Equal(warnings, simWarnings) || !slices.Equal(warnings, []string{"missing_reverse_exit", "orphan_room"}) {
		t.Fatalf("warnings %v, sim's %v", warnings, simWarnings)
	}
}

// AC-10: an extra argument or an unknown flag is a usage error, exit 2.
func TestContentReference_UsageErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"content", "reference", "extra"},
		{"content", "reference", "--nope"},
	} {
		res := runCLI(t, args, nil)
		if res.exit != ExitUsage {
			t.Errorf("%v: exit %d, want %d; stderr %q", args, res.exit, ExitUsage, res.stderr)
		}
		if res.stdout != "" {
			t.Errorf("%v: stdout %q", args, res.stdout)
		}
	}
}

// failingWriter refuses every write, as a full disk or a closed pipe does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// The contract's exit 2: writing to stdout failed, in either output.
func TestContentReference_StdoutFailureExitsTwo(t *testing.T) {
	t.Parallel()
	for _, out := range []string{"json", "human"} {
		var stderr strings.Builder
		exit := execute(&runtime{
			args:      []string{"content", "reference", "--output", out},
			stdout:    failingWriter{},
			stderr:    &stderr,
			lookupEnv: lookupFrom(isolatedEnv(t, nil)),
		})
		if exit != ExitUsage {
			t.Errorf("--output %s to a failing stdout: exit %d, want %d; stderr %q", out, exit, ExitUsage, stderr.String())
		}
	}
}
