// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"path"
	"sort"
	"strings"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// SourceExt is the Content Language extension. Source blobs are published with
// a version (ADR-0009) so a decompile is never the only copy, and the server
// retains them without loading them: the server loads compiled definitions, and
// a server that compiled would be a second implementation of the compiler.
const SourceExt = ".aw"

// Store is the content store as resolution needs it. KafkaResolver is the one
// that talks to a broker; tests supply their own, which is what lets every
// rejection in AC-4 through AC-11 be a unit test rather than a broker fixture.
type Store interface {
	// Active returns the live version of every pack, by pack ID.
	Active(ctx context.Context) (map[string]uint64, error)
	// Manifest returns the manifest for exactly pack@version.
	Manifest(ctx context.Context, pack string, version uint64) (*contentv1.ContentVersion, error)
	// Blobs returns each ref's body, keyed by the ref's path.
	Blobs(ctx context.Context, refs []*contentv1.BlobRef) (map[string][]byte, error)
}

// Resolved is one pack at one version, decoded and ready for the validator.
// Pure with respect to (pack, version): the same pair resolves to the same
// value whether the blobs came from the broker or from the cache, which is
// what AC-7 asserts.
type Resolved struct {
	Pack        string
	Version     uint64
	CoreVersion uint64
	Zones       []sim.Input
	Templates   []sim.TemplateInput
	// Source is the Content Language the version was compiled from. Retained,
	// never loaded.
	Source []*contentv1.BlobRef
}

// Resolve reads pack@version out of the store and decodes it.
//
// Decoding refuses before the validator ever sees the content in three cases,
// each of which is a property of the *version* rather than of the World it
// would build: a definition this binary cannot parse (AC-4), a blob the store
// does not have (AC-6), and a Template published under the wrong pack (AC-11).
// Everything else is the validator's to refuse, unchanged (ADR-0004).
func Resolve(ctx context.Context, s Store, pack string, version uint64) (*Resolved, error) {
	mf, err := s.Manifest(ctx, pack, version)
	if err != nil {
		return nil, err
	}
	refs := mf.GetBlobs()
	bodies, err := s.Blobs(ctx, refs)
	if err != nil {
		return nil, err
	}

	out := &Resolved{Pack: pack, Version: version, CoreVersion: mf.GetCoreVersion()}

	// Manifest order is the publisher's, and the publisher sorts by path. Sort
	// again anyway: load order decides the order findings come out in, and a
	// manifest is data from outside this process.
	sorted := make([]*contentv1.BlobRef, len(refs))
	copy(sorted, refs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetPath() < sorted[j].GetPath() })

	for _, ref := range sorted {
		p := ref.GetPath()
		body := bodies[p]
		switch {
		case strings.HasSuffix(p, SourceExt):
			out.Source = append(out.Source, ref)

		case isTemplatePath(p):
			def, verr := parseTemplateJSON(p, body)
			if verr != nil {
				return nil, &ErrValidation{Findings: []sim.ValidationError{*verr}}
			}
			// AC-11. TemplateRef.Pack() is derived from the name, so a pack
			// publishing a blob named for another pack is claiming that pack's
			// namespace. Whether it would collide or merely stand in depends on
			// load order, which is exactly why it cannot be allowed to depend
			// on load order.
			if np := sim.TemplateRef(def.GetName()).Pack(); np != pack {
				return nil, &ErrPackMismatch{Blob: p, NamePack: np, PublishedPack: pack}
			}
			out.Templates = append(out.Templates, sim.TemplateInput{File: p, Def: def})

		case strings.HasSuffix(p, ".json"):
			def, verr := parseZoneJSON(p, body)
			if verr != nil {
				return nil, &ErrValidation{Findings: []sim.ValidationError{*verr}}
			}
			if v := def.GetFormatVersion(); v < sim.MinFormatVersion || v > sim.MaxFormatVersion {
				return nil, &ErrFormatVersion{
					Path: p, Have: v,
					Min: sim.MinFormatVersion, Max: sim.MaxFormatVersion,
				}
			}
			out.Zones = append(out.Zones, sim.Input{File: p, Def: def})

		default:
			// An unrecognized path is not a rejection. A pack may carry a
			// README; refusing it would make the manifest a closed vocabulary
			// that every future story has to widen.
		}
	}
	return out, nil
}

// isTemplatePath reports whether a manifest path is a Template blob:
// `templates/<name>.json`, the layout AW-CLI-006's `content compile --out`
// writes and this story reads back.
func isTemplatePath(p string) bool {
	dir, file := path.Split(p)
	return strings.TrimSuffix(dir, "/") == TemplatesSubdir && strings.HasSuffix(file, ".json")
}
