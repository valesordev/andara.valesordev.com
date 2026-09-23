// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// Compile parses every *.aw under dir, resolves against core and deps, and
// emits canonical blobs. Diagnostics are sorted by file, line, col, then code.
// Pure: no network, no clock, and the only filesystem it touches is the
// directory it was handed.
//
// The compile runs in stages and stops at the first that finds an error,
// because a later stage reading a broken earlier one produces findings about
// the compiler rather than about the source:
//
//	encoding  → the bytes are UTF-8 with LF and no BOM
//	parse     → every file is well-formed (one syntax_error per file at most)
//	pack      → exactly one pack declaration, naming this directory, against a
//	            core version the cache holds
//	resolve   → Templates and Zones, every finding reported (errors.md rule 4)
//
// Warnings never stop anything: they ride out with a successful Output
// (errors.md rule 7).
func Compile(dir string, core *Pack, deps []*Pack) (*Output, []Diagnostic) {
	return CompileOpts(dir, core, deps, Options{})
}

// Options carries what Compile cannot derive from the directory.
//
// Pack is the pack the caller believes it is compiling. It is empty in the
// ordinary case — a Builder compiling their own working copy — and set by a
// caller that already knows the pack id, such as the publish gate checking that
// a pack published as `town` declares itself `town`. When it is empty
// pack_mismatch cannot fire, because a pack directory's *name* is not its pack
// name: the shipped andara.core lives in content/core/ and the corpus compiles
// `pack p` out of a directory called `minimal`.
type Options struct {
	Pack string
}

// CompileOpts is Compile with the inputs a directory cannot supply.
func CompileOpts(dir string, core *Pack, deps []*Pack, opts Options) (*Output, []Diagnostic) {
	sources, err := readSources(dir)
	if err != nil {
		return nil, []Diagnostic{{File: dir, Line: 1, Col: 1, Code: CodeEncoding, Message: err.Error()}}
	}
	if len(sources) == 0 {
		return &Output{Pack: filepath.Base(dir)}, nil
	}

	// Encoding is checked before a grammar is reached (corpus README), and the
	// first violation in the pack stops the compile: a BOM or a CR makes every
	// position after it a guess.
	if d, bad := checkEncoding(sources); bad {
		return nil, []Diagnostic{d}
	}

	files := make([]*File, 0, len(sources))
	var ds []Diagnostic
	for _, s := range sources {
		f, fds := ParseFile(s.path, s.text)
		files = append(files, f)
		ds = append(ds, fds...)
	}
	if len(ds) > 0 {
		sortDiagnostics(ds)
		return nil, ds
	}

	r := &resolver{dir: dir, files: files, core: core, deps: deps, want: opts.Pack}
	out := r.run()
	sortDiagnostics(r.ds)
	if HasError(r.ds) {
		// Only the errors. A warning is advice about content the compiler
		// otherwise accepted, and a compile that failed has not accepted
		// anything: "Room r is reachable by no Exit" on a pack whose Exits did
		// not resolve is noise about a conclusion the compiler did not reach.
		return nil, onlyErrors(r.ds)
	}
	return out, r.ds
}

// onlyErrors drops advisory findings, for a compile that failed.
func onlyErrors(ds []Diagnostic) []Diagnostic {
	out := ds[:0:0]
	for _, d := range ds {
		if d.Severity == SeverityError {
			out = append(out, d)
		}
	}
	return out
}

type source struct {
	path string // relative to the pack directory, slash-separated
	text string
	raw  []byte
}

// readSources reads every *.aw under dir, recursively, sorted by path. Sorted
// because pack-level findings name "the first file" and two compiles of the
// same pack have to agree on which that is.
func readSources(dir string) ([]source, error) {
	var out []source
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".aw") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, source{path: filepath.ToSlash(rel), text: string(b), raw: b})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// checkEncoding enforces "UTF-8, LF, no BOM" (semantics.md §1) and reports the
// first violation in the pack. One finding, not one per file: the corpus's crlf
// case has CRs in two files and names only the first
// (invalid/encoding/crlf/expected.errors).
func checkEncoding(sources []source) (Diagnostic, bool) {
	for _, s := range sources {
		if strings.HasPrefix(s.text, "\ufeff") {
			return Diagnostic{File: s.path, Line: 1, Col: 1, Code: CodeEncoding,
				Message: "file begins with a byte order mark; source is UTF-8 with LF and no BOM"}, true
		}
		line, col := 1, 1
		for i := 0; i < len(s.text); {
			r, n := utf8.DecodeRuneInString(s.text[i:])
			switch {
			case r == utf8.RuneError && n == 1:
				return Diagnostic{File: s.path, Line: line, Col: col, Code: CodeEncoding,
					Message: "file is not valid UTF-8; source is UTF-8 with LF and no BOM"}, true
			case r == '\r':
				return Diagnostic{File: s.path, Line: line, Col: col, Code: CodeEncoding,
					Message: "file has a carriage return; source is UTF-8 with LF and no BOM"}, true
			case r == '\n':
				line, col = line+1, 1
			default:
				col++
			}
			i += n
		}
	}
	return Diagnostic{}, false
}

// resolver holds one compile. Every finding goes through report, so the
// ordering rule and the severity default live in one place.
type resolver struct {
	dir   string
	files []*File
	core  *Pack
	deps  []*Pack
	want  string // the pack the caller is compiling; "" skips pack_mismatch
	ds    []Diagnostic

	pack string
}

func (r *resolver) report(file string, pos Pos, code, msg string, chain ...string) {
	r.ds = append(r.ds, Diagnostic{
		File: file, Line: pos.Line, Col: pos.Col,
		Code: code, Message: msg, Chain: chain, Severity: SeverityError,
	})
}

func (r *resolver) warn(file string, pos Pos, code, msg string, chain ...string) {
	r.ds = append(r.ds, Diagnostic{
		File: file, Line: pos.Line, Col: pos.Col,
		Code: code, Message: msg, Chain: chain, Severity: SeverityWarning,
	})
}

func (r *resolver) run() *Output {
	requires, ok := r.resolvePack()
	if !ok {
		return nil
	}
	out := &Output{Pack: r.pack, Requires: requires}
	out.Templates = r.resolveTemplates()
	out.Zones = r.resolveZones()
	if HasError(r.ds) {
		return nil
	}
	out.Blobs = r.blobs(out)
	return out
}

// resolvePack finds the one pack declaration, checks it names this directory,
// and checks the core version against the cache.
func (r *resolver) resolvePack() (CoreRef, bool) {
	type decl struct {
		file string
		d    *PackDecl
	}
	var decls []decl
	for _, f := range r.files {
		for _, d := range f.Decls {
			if pd, isPack := d.(*PackDecl); isPack {
				decls = append(decls, decl{f.Path, pd})
			}
		}
	}
	switch {
	case len(decls) == 0:
		// At 1:1 of the first file in the pack: there is no declaration to
		// point at, and the Builder has to add one somewhere.
		r.report(r.files[0].Path, Pos{Line: 1, Col: 1}, CodePackMissing,
			fmt.Sprintf("pack %q declares no `pack` line; one file in the pack must declare it", dirBase(r.dir)))
		return CoreRef{}, false
	case len(decls) > 1:
		// Positioned at the first declaration, not the second: this is a
		// pack-level finding — its chain is empty, unlike every other
		// duplicate_* — and the message names both files.
		names := make([]string, len(decls))
		for i, d := range decls {
			names[i] = d.file
		}
		r.report(decls[0].file, decls[0].d.Pos, CodeDuplicatePack,
			fmt.Sprintf("a pack declares `pack` once, and it is declared in %s", strings.Join(names, " and ")))
		return CoreRef{}, false
	}

	pd := decls[0].d
	r.pack = pd.Name
	// pack_mismatch compares against the pack the caller is compiling, which
	// is not the directory name: corpus/valid/core/ declares `pack
	// andara.core` and corpus/valid/minimal/ declares `pack p`. See
	// docs/feedback/AW-CLI-006-content-language-compiler.md §5.
	if r.want != "" && pd.Name != r.want {
		r.report(decls[0].file, pd.NamePos, CodePackMismatch,
			fmt.Sprintf("this compile is of pack %q, but the declaration names %q", r.want, pd.Name))
		return CoreRef{}, false
	}

	if pd.Requires == nil {
		// andara.core is the root pack: it requires nothing, because there is
		// nothing under it.
		return CoreRef{}, true
	}
	ref := CoreRef{Pack: pd.Requires.Pack, Version: pd.Requires.Version}
	switch {
	case r.core == nil:
		r.report(decls[0].file, pd.Requires.VersionPos, CodeCoreVersionMismatch,
			fmt.Sprintf("this pack requires %s@%d and no cached copy was found; run `andara-cli content fetch-core --version %d`",
				ref.Pack, ref.Version, ref.Version))
		return ref, false
	case r.core.Name != ref.Pack || r.core.Version != ref.Version:
		r.report(decls[0].file, pd.Requires.VersionPos, CodeCoreVersionMismatch,
			fmt.Sprintf("this pack requires %s@%d and the cache holds %s@%d; run `andara-cli content fetch-core --version %d`",
				ref.Pack, ref.Version, r.core.Name, r.core.Version, ref.Version))
		return ref, false
	}
	return ref, true
}

// blobs assembles what a ContentVersion manifests, sorted by path: one blob per
// declaration, plus the sources byte for byte as authored (semantics.md §6).
func (r *resolver) blobs(out *Output) []Blob {
	blobs := make([]Blob, 0, len(out.Zones)+len(out.Templates)+len(r.files))
	for _, z := range out.Zones {
		blobs = append(blobs, Blob{Path: z.GetId() + ".json", MediaType: BlobMediaType, Bytes: CanonicalJSON(z)})
	}
	for _, t := range out.Templates {
		blobs = append(blobs, Blob{
			Path:      path.Join("templates", t.GetName()+".json"),
			MediaType: BlobMediaType,
			Bytes:     CanonicalJSON(t),
		})
	}
	for _, f := range r.files {
		blobs = append(blobs, Blob{Path: SourcePrefix + f.Path, MediaType: SourceMediaType, Bytes: []byte(f.Src)})
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].Path < blobs[j].Path })
	return blobs
}

// dirBase is filepath.Base with a trailing separator tolerated, so that a
// --path of "./town/" names the pack "town" rather than "".
func dirBase(dir string) string {
	return filepath.Base(filepath.Clean(dir))
}

// componentChain is the chain a Component finding carries: the carrier's chain
// with the Component type appended. invalid_component_field names the type
// because the field belongs to it; duplicate_component_type does not, because
// the type is the offending value and is already at the position.
func componentChain(carrier []string, typ string) []string {
	return append(append([]string{}, carrier...), typ)
}

// buildComponents validates a carrier's Components against the server registry
// and returns them sorted by type, at most one of each (semantics.md §3).
//
// The registry is sim's, read rather than copied: an unregistered type is
// unknown_component_type and the message says types are server-defined, because
// without that a Builder re-checks their spelling forever for a type that was
// never going to exist (ADR-0010 decision 7).
func (r *resolver) buildComponents(file string, decls []*ComponentDecl, chain []string) []*contentv1.ComponentValue {
	seen := map[string]bool{}
	out := make([]*contentv1.ComponentValue, 0, len(decls))
	for _, c := range decls {
		if seen[c.Type] {
			r.report(file, c.Pos, CodeDuplicateComponent,
				fmt.Sprintf("Component %q is declared twice here; a carrier holds at most one of each type", c.Type),
				chain...)
			continue
		}
		seen[c.Type] = true
		if !sim.KnownComponentType(sim.ComponentType(c.Type)) {
			r.report(file, c.TypePos, CodeUnknownComponent,
				fmt.Sprintf("no Component type %q; types are server-defined (%s) and a new one is a server change, so file an issue rather than re-checking the spelling",
					c.Type, componentTypesList()),
				chain...)
			continue
		}
		cv := &contentv1.ComponentValue{Type: c.Type}
		cv.Fields = r.buildFields(file, c, chain)
		out = append(out, cv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetType() < out[j].GetType() })
	return out
}

func (r *resolver) buildFields(file string, c *ComponentDecl, chain []string) []*contentv1.ComponentField {
	typ := sim.ComponentType(c.Type)
	cc := componentChain(chain, c.Type)
	out := make([]*contentv1.ComponentField, 0, len(c.Fields))
	for _, f := range c.Fields {
		want, known := sim.ComponentFieldKind(typ, f.Name)
		if !known {
			r.report(file, f.NamePos, CodeInvalidComponentField,
				fmt.Sprintf("Component %q declares no field %q%s", c.Type, f.Name, fieldsList(typ)), cc...)
			continue
		}
		cf, ok := r.fieldValue(file, c.Type, f, want, cc, chain)
		if !ok {
			continue
		}
		out = append(out, cf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// fieldValue validates one assignment. fieldChain names the Component, because
// the field belongs to it; carrier is the chain without it, for the two
// findings whose subject is the literal rather than the field —
// corpus/invalid/semantic/float-literal names p.T and not
// p.T + andara.core.Behavior.
func (r *resolver) fieldValue(file, typ string, f *FieldAssign, want sim.FieldKind, fieldChain, carrier []string) (*contentv1.ComponentField, bool) {
	v := f.Value
	if v.Kind == Float {
		r.report(file, v.Pos, CodeFloatLiteral,
			fmt.Sprintf("%s is a float, and a Component field has no float member; a quantity that wants a fraction is an integer in fixed units", v.Raw),
			carrier...)
		return nil, false
	}
	if v.Kind == String && v.BadEsc != nil {
		r.report(file, *v.BadEsc, CodeInvalidEscape,
			fmt.Sprintf("%s is not an escape this language has; the three that exist are \\n, \\\" and \\\\", v.BadEscWh),
			carrier...)
		return nil, false
	}
	got := sim.FieldString
	switch v.Kind {
	case Int:
		got = sim.FieldInt
	case LowerID:
		got = sim.FieldBool
	}
	if got != want {
		r.report(file, v.Pos, CodeInvalidComponentField,
			fmt.Sprintf("field %q of Component %q is declared %s, and this value is %s", f.Name, typ, want, got),
			fieldChain...)
		return nil, false
	}
	cf := &contentv1.ComponentField{Name: f.Name}
	switch want {
	case sim.FieldString:
		cf.Value = &contentv1.ComponentField_StringValue{StringValue: v.Str}
	case sim.FieldInt:
		cf.Value = &contentv1.ComponentField_IntValue{IntValue: v.Int}
	case sim.FieldBool:
		cf.Value = &contentv1.ComponentField_BoolValue{BoolValue: v.Bool}
	}
	return cf, true
}

func componentTypesList() string {
	ts := sim.ComponentTypes()
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}

func fieldsList(t sim.ComponentType) string {
	names := sim.ComponentFieldNames(t)
	if len(names) == 0 {
		return "; it is a marker Component and carries no fields"
	}
	return "; it carries " + strings.Join(names, ", ")
}
