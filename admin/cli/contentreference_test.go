// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
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
// hand: every sim.ErrCode constant, and every lang Code* constant or variable,
// each resolved to its string value (AC-5, AC-7).
func parsedCodes(t *testing.T) (simCodes, langCodes map[string]string) {
	t.Helper()
	simCodes = map[string]string{} // value -> constant name
	simByName := map[string]string{}
	for _, spec := range valueSpecs(t, filepath.Join("..", "..", "server", "sim")) {
		id, ok := spec.Type.(*ast.Ident)
		if !ok || id.Name != "ErrCode" {
			continue
		}
		for i, n := range spec.Names {
			v := stringLit(t, spec.Values[i])
			simCodes[v] = n.Name
			simByName[n.Name] = v
		}
	}
	langCodes = map[string]string{}
	for _, spec := range valueSpecs(t, filepath.Join("..", "..", "content", "lang")) {
		for i, n := range spec.Names {
			if !strings.HasPrefix(n.Name, "Code") || i >= len(spec.Values) {
				continue
			}
			switch v := spec.Values[i].(type) {
			case *ast.BasicLit:
				langCodes[stringLit(t, v)] = n.Name
			case *ast.CallExpr: // string(sim.ErrX)
				sel, ok := v.Args[0].(*ast.SelectorExpr)
				if !ok {
					t.Fatalf("lang %s: unexpected value", n.Name)
				}
				val, ok := simByName[sel.Sel.Name]
				if !ok {
					t.Fatalf("lang %s quotes sim.%s, which is no sim.ErrCode", n.Name, sel.Sel.Name)
				}
				langCodes[val] = n.Name
			}
		}
	}
	if len(simCodes) == 0 || len(langCodes) == 0 {
		t.Fatalf("parsed %d sim codes and %d lang codes", len(simCodes), len(langCodes))
	}
	return simCodes, langCodes
}

func valueSpecs(t *testing.T, dir string) []*ast.ValueSpec {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go source in %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var out []*ast.ValueSpec
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || (g.Tok != token.CONST && g.Tok != token.VAR) {
				continue
			}
			for _, s := range g.Specs {
				out = append(out, s.(*ast.ValueSpec))
			}
		}
	}
	return out
}

func stringLit(t *testing.T, e ast.Expr) string {
	t.Helper()
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		t.Fatalf("not a string literal: %T", e)
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatal(err)
	}
	return v
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
