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
		Short:         "Compile, format, and decompile Content Language packs (builder)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newContentCompileCmd(rt))
	cmd.AddCommand(newContentFmtCmd(rt))
	cmd.AddCommand(newContentDecompileCmd(rt))
	cmd.AddCommand(newContentFetchCoreCmd(rt))
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
}

func toJSONDiagnostics(ds []lang.Diagnostic) []jsonDiagnostic {
	out := make([]jsonDiagnostic, 0, len(ds))
	for _, d := range ds {
		out = append(out, jsonDiagnostic{
			File: d.File, Line: d.Line, Col: d.Col, Code: d.Code,
			Message: d.Message, Chain: d.Chain, Severity: d.Severity.String(),
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
		shown.File = filepath.Join(path, d.File)
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

// loadCore reads the andara.core version a pack pins, from the cache. A pack
// that requires nothing — andara.core itself — needs no core.
func (rt *runtime) loadCore(path, cacheFlag string) (*lang.Pack, error) {
	ref, err := lang.RequiredCore(path)
	if err != nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
	}
	if ref.Pack == "" {
		return nil, nil
	}
	root := rt.contentCacheRoot(cacheFlag)
	pack, ok, err := lang.LoadCachedPack(root, ref.Pack, ref.Version)
	if err != nil {
		return nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: err.Error()}
	}
	if !ok {
		// Not an error here. Compile turns a missing cache into
		// core_version_mismatch naming both versions and the command to run,
		// which is the finding a Builder can act on (AC-5).
		return nil, nil
	}
	return pack, nil
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
			core, err := rt.loadCore(path, cache)
			if err != nil {
				return err
			}
			_, span := rt.childSpan("content.compile")
			result, ds := lang.Compile(path, core, nil)
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

// writeBlobs lays the compiled output out as a pack directory: Zones at the
// root, Templates under templates/, sources under src/ (semantics.md §6).
func writeBlobs(dir string, o *lang.Output) error {
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
			"--pack and --version are AW-CLI-003's, once Admin can serve a published version;\n" +
			"today --path decompiles a pack that is already on disk.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			core, err := rt.loadCore(path, cache)
			if err != nil {
				return err
			}
			result, ds := lang.Compile(path, core, nil)
			if err := rt.writeDiagnostics(ds, path); err != nil {
				return err
			}
			if result == nil {
				return compileFailed(path, ds)
			}
			files, err := lang.Decompile(result)
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
		Long: "Populate the local andara.core cache so that `content compile` works offline.\n\n" +
			"--from reads a pack directory that is already on disk — the shipped seed under\n" +
			"content/core, or a checkout. Fetching over Admin needs an RPC that serves a\n" +
			"published ContentVersion, which is AW-SRV-013's and AW-CLI-003's to define; see\n" +
			"docs/feedback/AW-CLI-006-content-language-compiler.md §7.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				return &AppError{
					Exit:    ExitConnect,
					Code:    "core_fetch_unavailable",
					Message: "no Admin RPC serves a published content version yet; pass --from with a pack directory to populate the cache from disk",
					Detail:  map[string]any{"story": "AW-SRV-013", "flag": "--from"},
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
