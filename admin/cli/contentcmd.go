// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
)

// `content compile`, `content fmt`, and `content decompile` are the Builder's
// half of the content pipeline: the compiler AW-CLI-006 implements, behind the
// AW-CLI-001 output contract.
//
// None of the three talks to a server. `Compile` is pure — it reads a directory
// and a cached core pack and nothing else — which is what lets a Builder work
// offline and what makes the same function safe to call from AW-SRV-013's
// publish gate (AC-5, AC-8).
func newContentCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "content",
		Short:         "Compile, validate, publish, and roll back Content Language packs (builder)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newContentCompileCmd(rt))
	cmd.AddCommand(newContentValidateCmd(rt))
	cmd.AddCommand(newContentInspectCmd(rt))
	cmd.AddCommand(newContentPublishCmd(rt), newContentApproveCmd(rt), newContentActivateCmd(rt),
		newContentRollbackCmd(rt), newContentHistoryCmd(rt), newContentDiffCmd(rt), newContentFetchCmd(rt))
	cmd.AddCommand(newContentFmtCmd(rt))
	cmd.AddCommand(newContentDecompileCmd(rt))
	cmd.AddCommand(newContentFetchCoreCmd(rt))
	cmd.AddCommand(newContentReferenceCmd(rt))
	return cmd
}

// contentCacheRoot resolves ANDARA_CONTENT_CACHE through AW-CLI-001's
// precedence: the flag, then the environment, then ~/.cache/andara/packs.
func (rt *runtime) contentCacheRoot(flagValue string) string {
	if strings.TrimSpace(flagValue) != "" {
		return flagValue
	}
	if v, ok := rt.lookupEnv("ANDARA_CONTENT_CACHE"); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if home, ok := rt.lookupEnv("HOME"); ok && home != "" {
		return lang.DefaultCacheDir(home)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return lang.DefaultCacheDir(home)
}

// jsonDiagnostic is one finding in the --output json envelope (errors.md §1).
type jsonDiagnostic struct {
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Col      int      `json:"col"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Chain    []string `json:"chain,omitempty"`
	Severity string   `json:"severity"`
	// Pack is set on a finding in another pack's blobs, reported by the
	// publish gate (errors.md §1 rule 10.6); absent for the caller's own.
	Pack string `json:"pack,omitempty"`
}

func toJSONDiagnostics(ds []lang.Diagnostic) []jsonDiagnostic {
	out := make([]jsonDiagnostic, 0, len(ds))
	for _, d := range ds {
		out = append(out, jsonDiagnostic{
			File: d.File, Line: d.Line, Col: d.Col, Code: d.Code,
			Message: d.Message, Chain: d.Chain, Severity: d.Severity.String(), Pack: d.Pack,
		})
	}
	return out
}

// writeDiagnostics renders findings as AC-2 specifies: one line per finding as
// `file:line:col: CODE message` with the declaration chain indented beneath, on
// stderr, exit 1.
//
// It writes nothing under `--output json`. There the findings ride inside
// AW-CLI-001's error envelope (errors.md §1: "AW-CLI-001's error envelope with
// diagnostics: []"), so that a failed command emits one JSON object and not
// two — a consumer reading stdout with a single Decode should get the whole
// answer. compileFailed builds that envelope; a compile that only warns puts
// the same array in its success object.
//
// Human findings go to stderr even in the success case, because warnings ride
// out with a compile that succeeded and a Builder piping stdout to a file
// should still see them.
func (rt *runtime) writeDiagnostics(ds []lang.Diagnostic, path string) error {
	if rt.settings.Output == outputJSON {
		return nil
	}
	var sb strings.Builder
	for _, d := range ds {
		shown := d
		if d.Pack == "" {
			// Another pack's finding names its own blob, not a file under
			// --path.
			shown.File = filepath.Join(path, d.File)
		}
		shown.Render(&sb)
	}
	if sb.Len() > 0 {
		fmt.Fprint(rt.stderr, sb.String())
	}
	return nil
}

// compileFailed is the AW-CLI-001 error envelope for a refused compile, with
// the findings in its detail.
func compileFailed(path string, ds []lang.Diagnostic) *AppError {
	return &AppError{
		Exit:    ExitFail,
		Code:    "compile_failed",
		Message: fmt.Sprintf("%d finding(s) refused the compile", len(ds)),
		Detail: map[string]any{
			"path":        path,
			"diagnostics": toJSONDiagnostics(ds),
		},
	}
}

// loadCore reads the andara.core version a pack pins. A pack that requires
// nothing — andara.core itself — needs no core.
func (rt *runtime) loadCore(path, cacheFlag string) (*lang.Pack, error) {
	ref, err := lang.RequiredCore(path)
	if err != nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
	}
	return rt.findCore(ref, cacheFlag)
}

// findCore looks andara.core@M up where AW-CLI-002's contract says to, in
// order: the core this binary embeds, if M is its VERSION; then the cache.
// Neither is not an error here. Compile turns a missing core into
// core_version_mismatch naming both versions and the release to use, which is
// the finding a Builder can act on (AC-5).
func (rt *runtime) findCore(ref lang.CoreRef, cacheFlag string) (*lang.Pack, error) {
	if ref.Pack == "" {
		return nil, nil
	}
	if ref.Pack == core.Pack && uint64(ref.Version) == core.Version() {
		return embeddedCore()
	}
	root := rt.contentCacheRoot(cacheFlag)
	pack, ok, err := lang.LoadCachedPack(root, ref.Pack, ref.Version)
	if err != nil {
		return nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: err.Error()}
	}
	if !ok {
		return nil, nil
	}
	return pack, nil
}

// embeddedCore is content/core as a pack to resolve against: the same bytes,
// under the same VERSION, that every server embeds and publishes at boot
// (ADR-0004 and ADR-0010 §8, amended 2026-09-28).
func embeddedCore() (*lang.Pack, error) {
	p, err := lang.PackFromBlobs(core.Pack, embeddedCoreVersion(), core.Blobs())
	if err != nil {
		// Embedded at build time; a failure is a broken build, not input.
		return nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: "the embedded andara.core does not parse: " + err.Error()}
	}
	return p, nil
}

func embeddedCoreVersion() uint32 { return uint32(core.Version()) }

// compileOptions are the options every compile the CLI runs shares.
func compileOptions(ignore []string) lang.Options {
	return lang.Options{Ignore: ignore, EmbeddedCore: embeddedCoreVersion()}
}

func newContentCompileCmd(rt *runtime) *cobra.Command {
	var (
		path  string
		out   string
		cache string
	)
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile a Content Language pack to canonical blobs",
		Long: "Compile every *.aw under --path and write the canonical andara.content.v1 blobs.\n\n" +
			"Resolves `extends` against the cached andara.core the pack pins; no network.\n" +
			"Exits 1 with one finding per line when the source has errors, 0 with warnings.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			corePack, err := rt.loadCore(path, cache)
			if err != nil {
				return err
			}
			ignore, err := ignoredOutput(path, out)
			if err != nil {
				return err
			}
			_, span := rt.childSpan("content.compile")
			result, ds := lang.CompileOpts(path, corePack, nil, compileOptions(ignore))
			span.SetAttributes(attribute.Int("diagnostics", len(ds)))
			if result != nil {
				span.SetAttributes(
					attribute.Int("files", countSources(result)),
					attribute.Int("zones", len(result.Zones)),
					attribute.Int("templates", len(result.Templates)),
				)
			}
			span.End()

			if err := rt.writeDiagnostics(ds, path); err != nil {
				return err
			}
			if result == nil {
				return compileFailed(path, ds)
			}

			if out != "" {
				if err := writeBlobs(out, result); err != nil {
					return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
				}
			}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(struct {
					Pack        string           `json:"pack"`
					Zones       int              `json:"zones"`
					Rooms       int              `json:"rooms"`
					Templates   int              `json:"templates"`
					Blobs       int              `json:"blobs"`
					Diagnostics []jsonDiagnostic `json:"diagnostics"`
				}{
					Pack: result.Pack, Zones: len(result.Zones), Rooms: countRooms(result),
					Templates: len(result.Templates), Blobs: len(result.Blobs),
					Diagnostics: toJSONDiagnostics(ds),
				})
			}
			fmt.Fprintf(rt.stdout, "%d zones, %d rooms, %d templates\n",
				len(result.Zones), countRooms(result), len(result.Templates))
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&path, "path", ".", "pack directory to compile")
	fs.StringVar(&out, "out", "", "write the compiled blobs under this directory")
	fs.StringVar(&cache, "cache", "", "core pack cache (default $ANDARA_CONTENT_CACHE, then ~/.cache/andara/packs)")
	return cmd
}

func countRooms(o *lang.Output) int {
	n := 0
	for _, z := range o.Zones {
		n += len(z.GetRooms())
	}
	return n
}

func countSources(o *lang.Output) int {
	n := 0
	for _, b := range o.Blobs {
		if b.MediaType == lang.SourceMediaType {
			n++
		}
	}
	return n
}

// ignoredOutput reports the --out directory as a path Compile should not read
// back, when it sits inside --path.
//
// `content compile --path . --out build` is the natural local layout, and the
// pack publishes its own sources under src/ (semantics.md §6) — so without this
// the second run reads build/src/*.aw as pack input and reports duplicate_pack
// against the Builder's own output.
func ignoredOutput(path, out string) ([]string, error) {
	if out == "" {
		return nil, nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
	}
	rel, err := filepath.Rel(absPath, absOut)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, nil // outside the pack, or the pack itself — nothing to skip
	}
	return []string{filepath.ToSlash(rel)}, nil
}

// writeBlobs lays the compiled output out as a pack directory: Zones at the
// root, Templates under templates/, sources under src/ (semantics.md §6).
//
// It prunes first. A Builder who deletes a Zone or a Template and recompiles
// into the same --out would otherwise keep the old blob, and the directory
// loader globs `*.json` — so the removed content stays live in a pack the
// Builder believes they rebuilt. Only what this writer produces is pruned:
// `*.json` at the root, `templates/*.json`, and `src/**.aw`. Anything else in
// the directory is somebody else's and is left alone.
func writeBlobs(dir string, o *lang.Output) error {
	keep := make(map[string]bool, len(o.Blobs))
	for _, b := range o.Blobs {
		keep[filepath.FromSlash(b.Path)] = true
	}
	if err := pruneManaged(dir, keep); err != nil {
		return err
	}
	for _, b := range o.Blobs {
		target := filepath.Join(dir, filepath.FromSlash(b.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, b.Bytes, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pruneManaged removes the outputs a previous compile wrote that this one does
// not.
func pruneManaged(dir string, keep map[string]bool) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || keep[rel] {
			return err
		}
		if !isManagedOutput(rel) {
			return nil
		}
		return os.Remove(p)
	})
}

// isManagedOutput reports whether a path inside --out is one `content compile`
// produces, and therefore one it may remove.
func isManagedOutput(rel string) bool {
	rel = filepath.ToSlash(rel)
	dir, base := filepath.Split(rel)
	switch {
	case dir == "" && strings.HasSuffix(base, ".json"):
		return true // a Zone
	case dir == "templates/" && strings.HasSuffix(base, ".json"):
		return true // a Template
	case strings.HasPrefix(rel, lang.SourcePrefix) && strings.HasSuffix(base, ".aw"):
		return true // a published source
	}
	return false
}

func newContentFmtCmd(rt *runtime) *cobra.Command {
	var (
		path  string
		check bool
	)
	cmd := &cobra.Command{
		Use:   "fmt",
		Short: "Rewrite Content Language sources to the canonical form",
		Long: "Rewrite every *.aw under --path to the canonical form, so that layout stops\n" +
			"being a thing anyone argues about.\n\n" +
			"fmt reorders nothing and never changes meaning: a Builder who lays out a Zone\n" +
			"as the walk through it keeps that layout. --check rewrites nothing and exits 1\n" +
			"listing the files that would change.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := awFiles(path)
			if err != nil {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
			}
			var changed []string
			var ds []lang.Diagnostic
			for _, f := range files {
				src, err := os.ReadFile(f)
				if err != nil {
					return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
				}
				got, fds := lang.Format(src)
				if len(fds) > 0 {
					for i := range fds {
						fds[i].File = f
					}
					ds = append(ds, fds...)
					continue
				}
				if string(got) == string(src) {
					continue
				}
				changed = append(changed, f)
				if !check {
					if err := os.WriteFile(f, got, 0o644); err != nil {
						return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
					}
				}
			}
			if len(ds) > 0 {
				if err := rt.writeDiagnostics(ds, ""); err != nil {
					return err
				}
				return &AppError{Exit: ExitFail, Code: "compile_failed",
					Message: fmt.Sprintf("%d file(s) could not be parsed", len(ds)),
					Detail:  map[string]any{"path": path, "diagnostics": toJSONDiagnostics(ds)}}
			}
			sort.Strings(changed)
			if rt.settings.Output == outputJSON {
				if err := rt.writeJSON(struct {
					Checked int      `json:"checked"`
					Changed []string `json:"changed"`
				}{Checked: len(files), Changed: changed}); err != nil {
					return err
				}
			} else if check {
				for _, f := range changed {
					fmt.Fprintln(rt.stdout, f)
				}
			} else {
				fmt.Fprintf(rt.stdout, "%d files, %d rewritten\n", len(files), len(changed))
			}
			if check && len(changed) > 0 {
				return &AppError{Exit: ExitFail, Code: "would_reformat",
					Message: fmt.Sprintf("%d file(s) are not formatted", len(changed)),
					Detail:  map[string]any{"changed": changed}}
			}
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&path, "path", ".", "directory to format")
	fs.BoolVar(&check, "check", false, "rewrite nothing; exit 1 listing files that would change")
	return cmd
}

func awFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".aw") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func newContentDecompileCmd(rt *runtime) *cobra.Command {
	var (
		path  string
		out   string
		cache string
	)
	cmd := &cobra.Command{
		Use:   "decompile",
		Short: "Reconstruct Content Language source from compiled output",
		Long: "Reconstruct .aw source from compiled output, in the canonical layout.\n\n" +
			"The source it produces recompiles to the same blobs byte for byte. It carries no\n" +
			"comments: what preserves those is the source blob published beside the compiled\n" +
			"output, which `content fetch` returns.\n\n" +
			"For a published version, `content fetch` returns the source it was published with,\n" +
			"so there's nothing to decompile; --path decompiles a pack already on disk.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			corePack, err := rt.loadCore(path, cache)
			if err != nil {
				return err
			}
			result, ds := lang.CompileOpts(path, corePack, nil, compileOptions(nil))
			if err := rt.writeDiagnostics(ds, path); err != nil {
				return err
			}
			if result == nil {
				return compileFailed(path, ds)
			}
			files, err := lang.DecompileWith(result, corePack)
			if err != nil {
				return &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: err.Error()}
			}
			names := make([]string, 0, len(files))
			for n := range files {
				names = append(names, n)
			}
			sort.Strings(names)
			if out != "" {
				if err := os.MkdirAll(out, 0o755); err != nil {
					return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
				}
				for _, n := range names {
					if err := os.WriteFile(filepath.Join(out, n), files[n], 0o644); err != nil {
						return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
					}
				}
			}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(struct {
					Pack  string   `json:"pack"`
					Files []string `json:"files"`
				}{Pack: result.Pack, Files: names})
			}
			for _, n := range names {
				fmt.Fprintln(rt.stdout, n)
			}
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&path, "path", ".", "pack directory to decompile")
	fs.StringVar(&out, "out", "", "write the reconstructed sources under this directory")
	fs.StringVar(&cache, "cache", "", "core pack cache (default $ANDARA_CONTENT_CACHE, then ~/.cache/andara/packs)")
	return cmd
}

func newContentFetchCoreCmd(rt *runtime) *cobra.Command {
	var (
		version uint32
		from    string
		cache   string
	)
	cmd := &cobra.Command{
		Use:   "fetch-core",
		Short: "Populate the local andara.core pack cache",
		Long: "Populate the local andara.core cache with a core this andara-cli does not embed.\n\n" +
			"andara-cli embeds one andara.core (`andara-cli version` names it), and a pack\n" +
			"requiring it compiles offline with no cache at all. --from reads another core's\n" +
			"pack directory from disk into the cache. It never fetches over Admin: a core this\n" +
			"binary doesn't embed comes with the andara-cli release that embeds it.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				v := embeddedCoreVersion()
				return &AppError{
					Exit: ExitConnect,
					Code: "core_fetch_unavailable",
					Message: fmt.Sprintf("this andara-cli embeds %s@%d and fetches no other core; use the andara-cli release that embeds the core you need, or pass --from with its pack directory",
						core.Pack, v),
					Detail: map[string]any{"embedded_core": v, "flag": "--from"},
				}
			}
			result, ds := lang.Compile(from, nil, nil)
			if err := rt.writeDiagnostics(ds, from); err != nil {
				return err
			}
			if result == nil {
				return compileFailed(from, ds)
			}
			root := rt.contentCacheRoot(cache)
			if root == "" {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue,
					Message: "no cache directory; set ANDARA_CONTENT_CACHE or --cache"}
			}
			pack := &lang.Pack{Name: result.Pack, Version: version, Templates: result.Templates}
			if err := lang.WriteCachedPack(root, pack); err != nil {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
			}
			dir := lang.CacheDir(root, pack.Name, version)
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(struct {
					Pack      string `json:"pack"`
					Version   uint32 `json:"version"`
					Templates int    `json:"templates"`
					Dir       string `json:"dir"`
				}{Pack: pack.Name, Version: version, Templates: len(pack.Templates), Dir: dir})
			}
			fmt.Fprintf(rt.stdout, "cached %s@%d: %d templates in %s\n", pack.Name, version, len(pack.Templates), dir)
			return nil
		},
	}
	fs := cmd.Flags()
	fs.Uint32Var(&version, "version", 1, "the andara.core version to cache")
	fs.StringVar(&from, "from", "", "populate the cache from a pack directory on disk")
	fs.StringVar(&cache, "cache", "", "core pack cache (default $ANDARA_CONTENT_CACHE, then ~/.cache/andara/packs)")
	return cmd
}
