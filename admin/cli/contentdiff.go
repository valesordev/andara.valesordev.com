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

	"github.com/valesordev/andara/content/lang"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
)

// `content fetch` and `content diff` read published versions back
// (AW-CLI-003). Both go through fetchVersion: GetVersion, then GetBlob for
// each blob, every body checked against its manifest hash.

// --- fetch -------------------------------------------------------------------

func newContentFetchCmd(rt *runtime) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "fetch <pack> <version>",
		Short: "Write a published version's .aw source to a directory",
		Long: "Fetch the Content Language source pack@version was published with, byte for\n" +
			"byte, comments and all.\n\n" +
			"A compiled Template records its source as <pack directory>/<file>, so the sources\n" +
			"compile to exactly the version's blobs in a directory with the name it was\n" +
			"published from. --out defaults to that name, in the current directory.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			version, err := parseVersion(args[1])
			if err != nil {
				return err
			}
			return rt.fetch(args[0], version, out)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "directory to write the sources to (default: the directory it was published from)")
	return cmd
}

func (rt *runtime) fetch(pack string, version uint64, dir string) error {
	rt.commandAttrs(pack, version)
	_, bodies, err := rt.fetchVersion(pack, version)
	if err != nil {
		return rt.withTrace(err)
	}
	if dir == "" {
		dir = publishedDir(pack, bodies)
	}
	var files []string
	for p := range bodies {
		if strings.HasPrefix(p, lang.SourcePrefix) && strings.HasSuffix(p, content.SourceExt) {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return rt.withTrace(&AppError{Exit: ExitFail, Code: "no_sources",
			Message: fmt.Sprintf("%s@%d was published without sources; there's nothing to fetch", pack, version)})
	}
	written := make([]string, 0, len(files))
	for _, p := range files {
		rel := filepath.FromSlash(strings.TrimPrefix(p, lang.SourcePrefix))
		if !filepath.IsLocal(rel) {
			return rt.withTrace(&AppError{Exit: ExitFail, Code: "unsafe_source_path",
				Message: fmt.Sprintf("%s@%d publishes a source at %q, which leaves the pack; nothing more was written", pack, version, p),
				Detail:  map[string]any{"path": p}})
		}
		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
		}
		if err := os.WriteFile(target, bodies[p], 0o644); err != nil {
			return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
		}
		written = append(written, target)
	}
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(map[string]any{"pack": pack, "version": version, "dir": dir, "files": written, "trace_id": rt.traceID()})
	}
	for _, w := range written {
		fmt.Fprintln(rt.stdout, w)
	}
	return nil
}

// publishedDir is the directory name a version was compiled from, as its
// Templates record it (TemplateDefinition.source, `<dir>/<file>`), or the
// pack ID for a version with no Templates to say.
func publishedDir(pack string, bodies map[string][]byte) string {
	if res, err := content.ResolveBlobs(pack, bodies); err == nil {
		for _, t := range res.Templates {
			if d, _, ok := strings.Cut(t.Def.GetSource().GetFile(), "/"); ok && filepath.IsLocal(d) {
				return d
			}
		}
	}
	return pack
}

// --- diff --------------------------------------------------------------------

func newContentDiffCmd(rt *runtime) *cobra.Command {
	var cache string
	cmd := &cobra.Command{
		Use:   "diff <pack> <N> <M>",
		Short: "What changed from version N to M: Zones, Rooms, Exits, Templates, fields",
		Long: "Compare two published versions of a pack by what they define, not by their bytes:\n" +
			"Zones, Rooms, Exits, Templates and Component fields added, removed or changed.\n" +
			"Each line points into version M's source, or into N's for something M removed.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			n, err := parseVersion(args[1])
			if err != nil {
				return err
			}
			m, err := parseVersion(args[2])
			if err != nil {
				return err
			}
			return rt.diff(args[0], n, m, cache)
		},
	}
	cmd.Flags().StringVar(&cache, "cache", "", "core pack cache (default $ANDARA_CONTENT_CACHE, then ~/.cache/andara/packs)")
	return cmd
}

// side is one version of the pack as the diff reads it: its definitions,
// decoded from the published blobs, and where each was written.
type side struct {
	version   uint64
	zones     map[string]*contentv1.ZoneDefinition
	templates map[string]*contentv1.TemplateDefinition
	smap      *lang.SourceMap
}

func (rt *runtime) loadSide(pack string, version uint64, cacheFlag string) (*side, error) {
	_, bodies, err := rt.fetchVersion(pack, version)
	if err != nil {
		return nil, err
	}
	res, err := content.ResolveBlobs(pack, bodies)
	if err != nil {
		return nil, &AppError{Exit: ExitFail, Code: "undecodable_version",
			Message: fmt.Sprintf("%s@%d does not decode: %v", pack, version, err)}
	}
	s := &side{version: version, zones: map[string]*contentv1.ZoneDefinition{}, templates: map[string]*contentv1.TemplateDefinition{}}
	for _, z := range res.Zones {
		s.zones[z.Def.GetId()] = z.Def
	}
	for _, t := range res.Templates {
		s.templates[t.Def.GetName()] = t.Def
	}
	// The source map comes from compiling the published sources. A version
	// whose sources don't compile, or that has none, diffs without
	// positions.
	dir, err := os.MkdirTemp("", "andara-diff-")
	if err != nil {
		return s, nil
	}
	defer func() { _ = os.RemoveAll(dir) }()
	n := 0
	for p, b := range bodies {
		rel := filepath.FromSlash(strings.TrimPrefix(p, lang.SourcePrefix))
		if !strings.HasPrefix(p, lang.SourcePrefix) || !strings.HasSuffix(p, content.SourceExt) || !filepath.IsLocal(rel) {
			continue
		}
		target := filepath.Join(dir, rel)
		if os.MkdirAll(filepath.Dir(target), 0o755) != nil || os.WriteFile(target, b, 0o644) != nil {
			continue
		}
		n++
	}
	if n == 0 {
		return s, nil
	}
	corePack, err := rt.loadCore(dir, cacheFlag)
	if err != nil {
		return s, nil
	}
	if out, _ := lang.CompileOpts(dir, corePack, nil, compileOptions(nil)); out != nil {
		s.smap = out.SourceMap
	}
	return s, nil
}

// change is one line of a diff.
type change struct {
	Change  string `json:"change"` // added | removed | changed
	Kind    string `json:"kind"`   // zone | room | exit | template | component | field
	Ref     string `json:"ref"`
	Field   string `json:"field,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Version uint64 `json:"version"` // whose source File is in
}

func (rt *runtime) diff(pack string, n, m uint64, cacheFlag string) error {
	rt.commandAttrs(pack, m)
	a, err := rt.loadSide(pack, n, cacheFlag)
	if err != nil {
		return rt.withTrace(err)
	}
	b, err := rt.loadSide(pack, m, cacheFlag)
	if err != nil {
		return rt.withTrace(err)
	}
	changes := diffSides(a, b)
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(map[string]any{"pack": pack, "from": n, "to": m, "changes": changes, "trace_id": rt.traceID()})
	}
	if len(changes) == 0 {
		_, err := fmt.Fprintf(rt.stdout, "%s@%d and %s@%d define the same content\n", pack, n, pack, m)
		return err
	}
	for _, c := range changes {
		sym := map[string]string{"added": "+", "removed": "-", "changed": "~"}[c.Change]
		fmt.Fprintln(rt.stdout, renderChange(sym, c, pack, m))
	}
	return nil
}

// renderChange is one diff line: `+ exit town/plaza west -> well`,
// `~ room town/plaza: title "A" -> "B"`, `- field town.Merchant:
// Behavior.name = "x"`, then where it was written.
func renderChange(sym string, c change, pack string, m uint64) string {
	text := fmt.Sprintf("%s %s %s", sym, c.Kind, c.Ref)
	switch {
	case c.Kind == "exit" && c.Change == "changed":
		text += fmt.Sprintf(": %s -> %s", c.From, c.To)
	case c.Kind == "exit":
		text += " -> " + c.From + c.To
	case c.Field != "" && c.Change == "changed" && (c.From != "" || c.To != ""):
		text += fmt.Sprintf(": %s %s -> %s", c.Field, c.From, c.To)
	case c.Field != "" && (c.From != "" || c.To != ""):
		text += fmt.Sprintf(": %s = %s", c.Field, c.From+c.To)
	case c.Field != "":
		text += ": " + c.Field
	}
	if c.File != "" {
		loc := fmt.Sprintf("%s:%d", c.File, c.Line)
		if c.Version != m {
			loc = fmt.Sprintf("%s@%d %s", pack, c.Version, loc)
		}
		text += "  (" + loc + ")"
	}
	return text
}

// diffSides compares two versions, in a stable order: Zones and what's in
// them, then Templates.
func diffSides(a, b *side) []change {
	var out []change
	at := func(s *side, chain ...string) (string, int, uint64) {
		f, l, _ := s.smap.Position(chain...)
		return f, l, s.version
	}
	add := func(c change, s *side, chain ...string) {
		c.File, c.Line, c.Version = at(s, chain...)
		out = append(out, c)
	}

	for _, id := range unionKeys(a.zones, b.zones) {
		za, zb := a.zones[id], b.zones[id]
		switch {
		case za == nil:
			add(change{Change: "added", Kind: "zone", Ref: id}, b, id)
			continue
		case zb == nil:
			add(change{Change: "removed", Kind: "zone", Ref: id}, a, id)
			continue
		}
		if za.GetName() != zb.GetName() {
			add(change{Change: "changed", Kind: "zone", Ref: id, Field: "name", From: lang.Quote(za.GetName()), To: lang.Quote(zb.GetName())}, b, id)
		}
		if za.GetFallbackRoom() != zb.GetFallbackRoom() {
			add(change{Change: "changed", Kind: "zone", Ref: id, Field: "fallback", From: za.GetFallbackRoom(), To: zb.GetFallbackRoom()}, b, id)
		}
		for _, c := range diffComponents(za.GetComponents(), zb.GetComponents()) {
			c.Ref = id
			add(c, b, id)
		}
		diffRooms(id, za, zb, add, a, b)
	}

	for _, name := range unionKeys(a.templates, b.templates) {
		ta, tb := a.templates[name], b.templates[name]
		switch {
		case ta == nil:
			add(change{Change: "added", Kind: "template", Ref: name}, b, name)
			continue
		case tb == nil:
			add(change{Change: "removed", Kind: "template", Ref: name}, a, name)
			continue
		}
		if ta.GetKind() != tb.GetKind() {
			add(change{Change: "changed", Kind: "template", Ref: name, Field: "kind", From: ta.GetKind().String(), To: tb.GetKind().String()}, b, name)
		}
		if ca, cb := strings.Join(ta.GetChain(), " > "), strings.Join(tb.GetChain(), " > "); ca != cb {
			add(change{Change: "changed", Kind: "template", Ref: name, Field: "chain", From: ca, To: cb}, b, name)
		}
		for _, c := range diffComponents(ta.GetComponents(), tb.GetComponents()) {
			c.Ref = name
			add(c, b, name)
		}
	}
	return out
}

func diffRooms(zone string, za, zb *contentv1.ZoneDefinition, add func(change, *side, ...string), a, b *side) {
	ra, rb := roomsByID(za), roomsByID(zb)
	for _, id := range unionKeys(ra, rb) {
		ref := zone + "/" + id
		x, y := ra[id], rb[id]
		switch {
		case x == nil:
			add(change{Change: "added", Kind: "room", Ref: ref}, b, zone, id)
			continue
		case y == nil:
			add(change{Change: "removed", Kind: "room", Ref: ref}, a, zone, id)
			continue
		}
		if x.GetTitle() != y.GetTitle() {
			add(change{Change: "changed", Kind: "room", Ref: ref, Field: "title", From: lang.Quote(x.GetTitle()), To: lang.Quote(y.GetTitle())}, b, zone, id)
		}
		if x.GetDescription() != y.GetDescription() {
			add(change{Change: "changed", Kind: "room", Ref: ref, Field: "desc"}, b, zone, id)
		}
		for _, c := range diffComponents(x.GetComponents(), y.GetComponents()) {
			c.Ref = ref
			add(c, b, zone, id)
		}
		ea, eb := exitsByDir(x), exitsByDir(y)
		for _, d := range sim.Directions() {
			dir := string(d)
			p, q := ea[dir], eb[dir]
			eref := ref + " " + dir
			switch {
			case p == nil && q == nil:
			case p == nil:
				add(change{Change: "added", Kind: "exit", Ref: eref, To: exitTarget(zone, q)}, b, zone, id, dir)
			case q == nil:
				add(change{Change: "removed", Kind: "exit", Ref: eref, From: exitTarget(zone, p)}, a, zone, id, dir)
			case exitTarget(zone, p) != exitTarget(zone, q):
				add(change{Change: "changed", Kind: "exit", Ref: eref, From: exitTarget(zone, p), To: exitTarget(zone, q)}, b, zone, id, dir)
			}
		}
	}
}

// diffComponents compares two Component sets by type, then field by field.
func diffComponents(a, b []*contentv1.ComponentValue) []change {
	ca, cb := componentsByType(a), componentsByType(b)
	var out []change
	for _, typ := range unionKeys(ca, cb) {
		x, y := ca[typ], cb[typ]
		switch {
		case x == nil:
			out = append(out, change{Change: "added", Kind: "component", Field: typ})
			continue
		case y == nil:
			out = append(out, change{Change: "removed", Kind: "component", Field: typ})
			continue
		}
		fa, fb := fieldsByName(x), fieldsByName(y)
		for _, name := range unionKeys(fa, fb) {
			field := shortType(typ) + "." + name
			p, q := fa[name], fb[name]
			switch {
			case p == nil:
				out = append(out, change{Change: "added", Kind: "field", Field: field, To: fieldValue(q)})
			case q == nil:
				out = append(out, change{Change: "removed", Kind: "field", Field: field, From: fieldValue(p)})
			case fieldValue(p) != fieldValue(q):
				out = append(out, change{Change: "changed", Kind: "field", Field: field, From: fieldValue(p), To: fieldValue(q)})
			}
		}
	}
	return out
}

func exitTarget(zone string, e *contentv1.ExitDefinition) string {
	if tz := e.GetToZone(); tz != "" && tz != zone {
		return tz + "." + e.GetToRoom()
	}
	return e.GetToRoom()
}

func roomsByID(z *contentv1.ZoneDefinition) map[string]*contentv1.RoomDefinition {
	out := map[string]*contentv1.RoomDefinition{}
	for _, r := range z.GetRooms() {
		out[r.GetId()] = r
	}
	return out
}

func exitsByDir(r *contentv1.RoomDefinition) map[string]*contentv1.ExitDefinition {
	out := map[string]*contentv1.ExitDefinition{}
	for _, e := range r.GetExits() {
		out[e.GetDirection()] = e
	}
	return out
}

func componentsByType(cs []*contentv1.ComponentValue) map[string]*contentv1.ComponentValue {
	out := map[string]*contentv1.ComponentValue{}
	for _, c := range cs {
		out[c.GetType()] = c
	}
	return out
}

func fieldsByName(c *contentv1.ComponentValue) map[string]*contentv1.ComponentField {
	out := map[string]*contentv1.ComponentField{}
	for _, f := range c.GetFields() {
		out[f.GetName()] = f
	}
	return out
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]V{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
