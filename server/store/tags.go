// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"

	"github.com/valesordev/andara/server/sim"
)

// Round tags (docs/specs/deploy/lifecycle.md, "Round tags"): a zero-byte object
// at {zone_id}/{tick}/{state_version}/{offset}.tag/{name}, one per Zone object
// of the round. A tag key has five segments, so ParseSnapshotKey never reads one
// as a snapshot object and a listing of rounds skips it.
//
// This is the part AW-SRV-054's pre-stop needs: writing a round's tags and
// reading the names back. Admin.TagSnapshotRound, ListSnapshotRounds' tags and
// retention are AW-SRV-055's.

// tagDir is what a tag key adds to its object's key, before the name.
const tagDir = ".tag"

// TagNamePattern is the tag name grammar.
var TagNamePattern = regexp.MustCompile(`^(deploy|rollback):[A-Za-z0-9._@+-]{1,200}$`)

// DeployTagPrefix starts the tag pre-stop writes, `deploy:<image tag>`.
const DeployTagPrefix = "deploy:"

// ErrTagName: a name outside TagNamePattern.
var ErrTagName = errors.New("tag name must match " + TagNamePattern.String())

// TagKey is the key of name's tag on the object at objectKey.
func TagKey(objectKey, name string) string { return objectKey + tagDir + "/" + name }

// TagLister is a store that can list the tags on one object. Both stores
// implement it; a store that doesn't has no tags to report.
type TagLister interface {
	ListTags(ctx context.Context, objectKey string) ([]string, error)
}

// TagRound writes name's tag on every object in keys, the keys of one round.
// Each Put is idempotent, so a crash between two leaves a round that tagging
// again completes.
func TagRound(ctx context.Context, ws sim.WorldStore, keys []string, name string) error {
	if !TagNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrTagName, name)
	}
	if len(keys) == 0 {
		return errors.New("store: tag a round: the round has no objects")
	}
	for _, k := range keys {
		if err := ws.Put(ctx, TagKey(k, name), nil); err != nil {
			return fmt.Errorf("store: tag %s on %s: %w", name, k, err)
		}
	}
	return nil
}

// RoundTags is every tag name present on any of a round's objects, sorted. A
// store that is not a TagLister reports none.
func RoundTags(ctx context.Context, ws sim.WorldStore, keys []string) ([]string, error) {
	tl, ok := ws.(TagLister)
	if !ok {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, k := range keys {
		names, err := tl.ListTags(ctx, k)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			seen[n] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// ListTags implements TagLister: the names under the object's tag directory.
func (f *FS) ListTags(ctx context.Context, objectKey string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := f.path(objectKey + tagDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("%w: list tags: %w", sim.ErrStoreUnavailable, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), TempSuffix) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// ListTags implements TagLister: the names under the object's tag prefix.
func (s *S3) ListTags(ctx context.Context, objectKey string) ([]string, error) {
	prefix := objectKey + tagDir + "/"
	var out []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("%w: list tags %s: %w", sim.ErrStoreUnavailable, objectKey, obj.Err)
		}
		if name := strings.TrimPrefix(obj.Key, prefix); name != "" && !strings.Contains(name, "/") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}
