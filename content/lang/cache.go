// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// The core pack cache. A pack compiles against a pinned andara.core version,
// fetched once over Admin and used offline thereafter, so a Builder on a train
// can still compile (AC-5).
//
// The layout is the loader's own: <cache>/andara.core@N/templates/*.json, which
// is what server/content.LoadTemplatesDir already reads. A cache that were a
// bespoke format would be a second thing to keep in step with the loader.

// DefaultCacheDir is the cache root when ANDARA_CONTENT_CACHE is unset.
func DefaultCacheDir(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "andara", "packs")
}

// CacheDir is the directory one pack version occupies.
func CacheDir(root, pack string, version uint32) string {
	return filepath.Join(root, fmt.Sprintf("%s@%d", pack, version))
}

// LoadCachedPack reads a pack from the cache. A missing directory is not an
// error here: the caller turns it into core_version_mismatch naming both
// versions and pointing at `content fetch-core`, which is the message a Builder
// can act on — "no such directory" is not (AC-5).
func LoadCachedPack(root, pack string, version uint32) (*Pack, bool, error) {
	dir := CacheDir(root, pack, version)
	entries, err := os.ReadDir(filepath.Join(dir, "templates"))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	out := &Pack{Name: pack, Version: version}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, "templates", n))
		if err != nil {
			return nil, false, err
		}
		var def contentv1.TemplateDefinition
		if err := protojson.Unmarshal(b, &def); err != nil {
			return nil, false, fmt.Errorf("%s: %w", filepath.Join(dir, "templates", n), err)
		}
		out.Templates = append(out.Templates, &def)
	}
	return out, true, nil
}

// WriteCachedPack stores a pack's Templates in the cache, replacing whatever
// was there. The bytes are the canonical ones, so a cached pack and a published
// pack are the same file.
func WriteCachedPack(root string, p *Pack) error {
	dir := filepath.Join(CacheDir(root, p.Name, p.Version), "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, t := range p.Templates {
		name := t.GetName() + ".json"
		keep[name] = true
		if err := os.WriteFile(filepath.Join(dir, name), CanonicalJSON(t), 0o644); err != nil {
			return err
		}
	}
	// Drop Templates the new version does not carry, so a downgrade does not
	// leave a Template behind that `extends` can still resolve against.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !keep[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// RequiredCore reads the `requires` clause out of a pack's sources without
// compiling it, so a caller can find the version to load from the cache before
// it has a core pack to compile against.
//
// It returns an empty CoreRef for a pack that requires nothing, which is
// andara.core itself.
func RequiredCore(dir string) (CoreRef, error) {
	sources, err := readSources(dir, nil)
	if err != nil {
		return CoreRef{}, err
	}
	for _, s := range sources {
		f, ds := ParseFile(s.path, s.text)
		if len(ds) > 0 {
			continue // the compile reports it properly
		}
		for _, d := range f.Decls {
			pd, isPack := d.(*PackDecl)
			if !isPack || pd.Requires == nil {
				continue
			}
			return CoreRef{Pack: pd.Requires.Pack, Version: pd.Requires.Version}, nil
		}
	}
	return CoreRef{}, nil
}
