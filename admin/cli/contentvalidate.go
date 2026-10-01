// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
)

// `content validate` is the Builder's fast loop before a publish (AW-CLI-002):
// compile, then the validator the server runs, with both stages' findings in
// the compiler's one diagnostic format. It has no validation logic of its own.
// The compile is content/lang's and the validator is content.Validate, the
// Loader's, so a pack that passes here passes the publish gate on the same
// core (ADR-0004).

// validated is one pack, compiled and validated: what `content validate`
// reports and what `content inspect` reads.
type validated struct {
	// label prefixes file names in human output: --path, or pack@version.
	label string
	pack  string
	// core is the andara.core version the pack requires; zero when it
	// requires none.
	core     lang.CoreRef
	corePack *lang.Pack
	// resolved is the pack as the validator saw it: decoded from the
	// compiled blobs with the server's decoder. Nil when there was nothing
	// to validate.
	resolved *content.Resolved
	diags    []lang.Diagnostic
	// compiled is the compile's output, when the source compiled: its
	// blobs are what `content publish` uploads, and its source map places
	// the server's findings (AW-CLI-003).
	compiled *lang.Output
}

func (v *validated) failed() bool { return v.resolved == nil || lang.HasError(v.diags) }

func (v *validated) zones() []*contentv1.ZoneDefinition {
	var out []*contentv1.ZoneDefinition
	if v.resolved != nil {
		for _, z := range v.resolved.Zones {
			out = append(out, z.Def)
		}
	}
	return out
}

func (v *validated) templates() []*contentv1.TemplateDefinition {
	var out []*contentv1.TemplateDefinition
	if v.resolved != nil {
		for _, t := range v.resolved.Templates {
			out = append(out, t.Def)
		}
	}
	return out
}

func (v *validated) rooms() int {
	n := 0
	for _, z := range v.zones() {
		n += len(z.GetRooms())
	}
	return n
}

// validatePath compiles and validates a pack directory on disk. pack, when
// set, is the pack the caller means to compile, and a source declaring
// another is pack_mismatch.
func (rt *runtime) validatePath(dir, pack, cacheFlag string) (*validated, error) {
	corePack, err := rt.loadCore(dir, cacheFlag)
	if err != nil {
		return nil, err
	}
	v := &validated{label: dir, corePack: corePack}
	out, ok := rt.compileSpan(dir, corePack, lang.Options{Pack: pack}, v)
	if !ok {
		return v, nil
	}
	v.compiled = out
	blobs := make(map[string][]byte, len(out.Blobs))
	for _, b := range out.Blobs {
		blobs[b.Path] = b.Bytes
	}
	return v, rt.runValidator(v, blobs, out.SourceMap)
}

// compileSpan is the `content.compile` child span around one compile. It
// records the pack and its core on v, and its findings.
func (rt *runtime) compileSpan(dir string, corePack *lang.Pack, opts lang.Options, v *validated) (*lang.Output, bool) {
	_, span := rt.childSpan("content.compile")
	defer span.End()
	opts.EmbeddedCore = embeddedCoreVersion()
	out, ds := lang.CompileOpts(dir, corePack, nil, opts)
	v.diags = ds
	span.SetAttributes(attribute.Int("diagnostics", len(ds)))
	if out == nil {
		return nil, false
	}
	v.pack, v.core = out.Pack, out.Requires
	span.SetAttributes(
		attribute.Int("files", countSources(out)),
		attribute.Int("zones", len(out.Zones)),
		attribute.Int("rooms", countRooms(out)),
		attribute.Int("templates", len(out.Templates)),
	)
	return out, true
}

// runValidator is the `content.validate` stage: the compiled blobs through
// the server's decoder and validator, against the core, with each finding
// placed back on its source by the compiler's source map. A nil map (a
// published version with no sources) leaves the findings on the blobs.
func (rt *runtime) runValidator(v *validated, blobs map[string][]byte, smap *lang.SourceMap) error {
	_, span := rt.childSpan("content.validate")
	defer span.End()

	var refusing, warnings []sim.ValidationError
	res, err := content.ResolveBlobs(v.pack, blobs)
	if err != nil {
		refusing = content.Findings(err)
	} else {
		packs := []*content.Resolved{res}
		if v.corePack != nil {
			coreRes, err := resolvedPack(v.corePack)
			if err != nil {
				return &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: err.Error()}
			}
			packs = append(packs, coreRes)
		}
		refusing, warnings = content.Validate(packs, false)
		v.resolved = res
	}

	placed := make([]lang.Diagnostic, 0, len(refusing)+len(warnings))
	for _, f := range refusing {
		placed = append(placed, place(smap, f, lang.SeverityError))
	}
	for _, f := range warnings {
		placed = append(placed, place(smap, f, lang.SeverityWarning))
	}
	v.diags = mergeDiagnostics(v.diags, placed)
	if lang.HasError(v.diags) {
		v.resolved = nil
	}

	span.SetAttributes(
		attribute.Int("zones", len(v.zones())),
		attribute.Int("rooms", v.rooms()),
		attribute.Int("templates", len(v.templates())),
		attribute.Int("error_count", len(refusing)),
		attribute.Int("warning_count", len(warnings)),
	)
	return nil
}

// resolvedPack is a compiled pack in the shape the validator takes, through
// the same decoder: the canonical bytes of each Template, as published.
func resolvedPack(p *lang.Pack) (*content.Resolved, error) {
	blobs := make(map[string][]byte, len(p.Templates))
	for _, t := range p.Templates {
		blobs[path.Join(content.TemplatesSubdir, t.GetName()+".json")] = lang.CanonicalJSON(t)
	}
	res, err := content.ResolveBlobs(p.Name, blobs)
	if err != nil {
		return nil, fmt.Errorf("%s@%d does not decode: %w", p.Name, p.Version, err)
	}
	res.Version = uint64(p.Version)
	return res, nil
}

// place is one validator finding as a Diagnostic: on the declaration its chain
// names, when the source map has it, and otherwise on the blob it was found
// in, with no column.
func place(smap *lang.SourceMap, f sim.ValidationError, sev lang.Severity) lang.Diagnostic {
	chain := content.FindingChain(f)
	if d, ok := smap.Place(string(f.Code), f.Detail, chain, sev); ok {
		return d
	}
	return lang.Diagnostic{File: f.File, Line: f.Line, Code: string(f.Code), Message: f.Detail, Chain: chain, Severity: sev}
}

// mergeDiagnostics joins the compiler's findings with the validator's.
//
// The two stages overlap by design: the compiler raises orphan_room,
// missing_reverse_exit and every referential error early, so a Builder hears
// about them with a column, and the validator raises them again on the
// compiled output. A finding both report is the same finding, at the same
// position and chain once placed, and is listed once. And a refused pack
// reports only its errors, whichever stage refused it (errors.md rule 7).
func mergeDiagnostics(a, b []lang.Diagnostic) []lang.Diagnostic {
	seen := map[string]bool{}
	var out []lang.Diagnostic
	for _, d := range slices.Concat(a, b) {
		k := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s", d.File, d.Line, d.Col, d.Code, strings.Join(d.Chain, "\x00"))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	if lang.HasError(out) {
		out = slices.DeleteFunc(out, func(d lang.Diagnostic) bool { return d.Severity != lang.SeverityError })
	}
	lang.SortDiagnostics(out)
	return out
}

// validatePublished fetches pack@version over Admin and validates it: the
// sources it was published with compiled, for their findings and their
// source map, and the compiled blobs it was published with validated
// (AC-6). What's validated is what the server holds, not a recompile of it.
func (rt *runtime) validatePublished(pack string, version uint64, cacheFlag string) (*validated, error) {
	cv, bodies, err := rt.fetchVersion(pack, version)
	if err != nil {
		return nil, err
	}
	v := &validated{label: fmt.Sprintf("%s@%d", pack, version), pack: pack}

	var sources []string
	for p := range bodies {
		if strings.HasPrefix(p, lang.SourcePrefix) && strings.HasSuffix(p, content.SourceExt) {
			sources = append(sources, p)
		}
	}
	if len(sources) == 0 {
		// Published without sources: andara.core, or a pack from before
		// ADR-0009. Nothing to place findings on; the blobs are validated
		// as they are.
		if err := rt.manifestCore(v, cv, cacheFlag); err != nil {
			return nil, err
		}
		return v, rt.runValidator(v, bodies, nil)
	}

	dir, err := os.MkdirTemp("", "andara-validate-")
	if err != nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, p := range sources {
		// A manifest path is the publisher's, and the gate takes any unique
		// one. Only a path that stays under the scratch directory is
		// written, or a Builder's version could overwrite files on the
		// machine of whoever validates it.
		rel := filepath.FromSlash(strings.TrimPrefix(p, lang.SourcePrefix))
		if !filepath.IsLocal(rel) {
			return nil, &AppError{Exit: ExitFail, Code: "unsafe_source_path",
				Message: fmt.Sprintf("%s@%d publishes a source at %q, which leaves the pack; nothing was written", pack, version, p),
				Detail:  map[string]any{"path": p}}
		}
		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
		}
		if err := os.WriteFile(target, bodies[p], 0o644); err != nil {
			return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
		}
	}
	if v.corePack, err = rt.loadCore(dir, cacheFlag); err != nil {
		return nil, err
	}
	out, ok := rt.compileSpan(dir, v.corePack, lang.Options{Pack: pack}, v)
	if ok {
		return v, rt.runValidator(v, bodies, out.SourceMap)
	}

	// The sources don't compile. They're retained with the version, not
	// loaded: the server validated, and would load, the compiled blobs. So
	// the blobs are still validated, and they decide the result. The
	// source's findings are reported beside them as warnings, since they
	// say something true about the version without refusing it.
	for i := range v.diags {
		v.diags[i].Severity = lang.SeverityWarning
		v.diags[i].Message = "the published source does not compile: " + v.diags[i].Message
	}
	if err := rt.manifestCore(v, cv, cacheFlag); err != nil {
		return nil, err
	}
	return v, rt.runValidator(v, bodies, nil)
}

// manifestCore resolves the andara.core a published version's manifest names,
// for validating its blobs without a compile to say which core it pinned.
func (rt *runtime) manifestCore(v *validated, cv *contentv1.ContentVersion, cacheFlag string) error {
	if cv.GetCoreVersion() == 0 {
		v.corePack, v.core = nil, lang.CoreRef{}
		return nil
	}
	ref := lang.CoreRef{Pack: core.Pack, Version: uint32(cv.GetCoreVersion())}
	p, err := rt.findCore(ref, cacheFlag)
	if err != nil {
		return err
	}
	if p == nil {
		return coreMissing(ref)
	}
	v.corePack, v.core = p, ref
	return nil
}

// coreMissing is AC-5's finding for a version whose core this binary can't
// supply and has no source line to report it at.
func coreMissing(ref lang.CoreRef) error {
	return &AppError{Exit: ExitFail, Code: lang.CodeCoreVersionMismatch,
		Message: fmt.Sprintf("this version requires %s@%d, and neither the embedded core nor the cache holds it; this andara-cli embeds %s@%d; use the andara-cli release that embeds %s@%d",
			ref.Pack, ref.Version, core.Pack, embeddedCoreVersion(), ref.Pack, ref.Version),
		Detail: map[string]any{"required": ref.Version, "embedded": embeddedCoreVersion()}}
}

// fetchVersion reads pack@version's manifest with Admin.GetVersion and each
// blob with Admin.GetBlob, checking every body against the hash its manifest
// names.
func (rt *runtime) fetchVersion(pack string, version uint64) (*contentv1.ContentVersion, map[string][]byte, error) {
	client, err := rt.adminClient()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	resp, err := client.GetVersion(ctx, connect.NewRequest(&adminv1.GetVersionRequest{PackId: pack, Version: version}))
	if err != nil {
		return nil, nil, rt.contentError(err)
	}
	cv := resp.Msg.GetVersion()
	bodies := make(map[string][]byte, len(cv.GetBlobs()))
	for _, ref := range cv.GetBlobs() {
		stream, err := client.GetBlob(ctx, connect.NewRequest(&adminv1.GetBlobRequest{PackId: pack, Version: version, Hash: ref.GetHash()}))
		if err != nil {
			return nil, nil, rt.contentError(err)
		}
		var buf bytes.Buffer
		for stream.Receive() {
			buf.Write(stream.Msg().GetData())
		}
		if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
			return nil, nil, rt.contentError(err)
		}
		if sum := sha256.Sum256(buf.Bytes()); !bytes.Equal(sum[:], ref.GetHash()) {
			return nil, nil, &AppError{Exit: ExitFail, Code: "blob_hash_mismatch",
				Message: fmt.Sprintf("%s@%d: %s does not hash to the value its manifest names", pack, version, ref.GetPath()),
				Detail:  map[string]any{"path": ref.GetPath()}}
		}
		bodies[ref.GetPath()] = buf.Bytes()
	}
	return cv, bodies, nil
}

// packFlags are --path and --pack/--version, which validate and inspect share.
type packFlags struct {
	path    string
	pack    string
	version uint64
	cache   string
}

func (f *packFlags) register(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.StringVar(&f.path, "path", "", "pack directory of .aw source (default .)")
	fs.StringVar(&f.pack, "pack", "", "a published pack to fetch over Admin, with --version")
	fs.Uint64Var(&f.version, "version", 0, "the published version of --pack")
	fs.StringVar(&f.cache, "cache", "", "core pack cache (default $ANDARA_CONTENT_CACHE, then ~/.cache/andara/packs)")
}

// load validates the pack the flags name.
func (rt *runtime) load(f *packFlags) (*validated, error) {
	switch {
	case f.pack != "" && f.path != "":
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--path and --pack are exclusive: validate a directory, or a published version"}
	case f.pack != "" && f.version == 0:
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--pack needs --version"}
	case f.pack == "" && f.version != 0:
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--version needs --pack"}
	case f.pack != "":
		return rt.validatePublished(f.pack, f.version, f.cache)
	}
	dir := f.path
	if dir == "" {
		dir = "."
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: fmt.Sprintf("--path %s is not a directory", dir), Detail: map[string]any{"path": dir}}
	}
	return rt.validatePath(dir, "", f.cache)
}

func newContentValidateCmd(rt *runtime) *cobra.Command {
	var f packFlags
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Compile and validate a pack as the server would, offline",
		Long: "Compile a pack's .aw source and run the server's validator over the result.\n\n" +
			"--path validates a directory of source against the andara.core this andara-cli\n" +
			"embeds (`andara-cli version`), or a cached one; no network. --pack and --version\n" +
			"fetch a published version over Admin and validate it.\n\n" +
			"Exits 1 with one finding per line when the pack would be refused, 0 with\n" +
			"warnings. Under --output json, stdout is the array of findings and nothing else.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := rt.load(&f)
			if err != nil {
				return err
			}
			return rt.reportValidated(v)
		},
	}
	f.register(cmd)
	return cmd
}

// reportValidated writes AC-1 to AC-3: the findings, then the summary.
func (rt *runtime) reportValidated(v *validated) error {
	coreName := "none"
	if v.core.Pack != "" {
		coreName = fmt.Sprintf("%s@%d", v.core.Pack, v.core.Version)
	}
	summary := fmt.Sprintf("%d zones, %d rooms, %d templates, core %s", len(v.zones()), v.rooms(), len(v.templates()), coreName)
	if v.failed() {
		summary = fmt.Sprintf("%s: %d finding(s) refuse the pack", v.label, len(v.diags))
	}

	if rt.settings.Output == outputJSON {
		if err := rt.writeJSON(toJSONDiagnostics(v.diags)); err != nil {
			return err
		}
		if v.failed() {
			return &AppError{Exit: ExitFail, Code: "validation_failed", Message: summary, Rendered: true}
		}
		rt.writeSummary("info", summary)
		return nil
	}

	if err := rt.writeDiagnostics(v.diags, v.label); err != nil {
		return err
	}
	if v.failed() {
		return &AppError{Exit: ExitFail, Code: "validation_failed", Message: summary,
			Detail: map[string]any{"diagnostics": toJSONDiagnostics(v.diags)}}
	}
	_, err := fmt.Fprintln(rt.stdout, summary)
	return err
}
