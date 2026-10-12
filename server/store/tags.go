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
// as a snapshot object and a listing of rounds skips it. The tick is in the path
// because a tag names a round, and an offset alone doesn't identify one: an
// idle Zone repeats its offset across rounds.
//
// AW-SRV-054's pre-stop writes a round's tags from its keys (TagRound) and reads
// them back per object (RoundTags). AW-SRV-055's Admin.TagSnapshotRound finds
// the round itself (TagRoundAt), and ListSnapshotRounds and retention read a
// Zone's tags in one listing (TagStore.ZoneTags).

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

// RoundTags is every tag name present on all of a round's objects, sorted:
// the rule ListSnapshotRounds reports by, so a tagging cut short between two
// Puts names no round. (Retention's rule, a tag on any object, is the other
// side of the same fail-safe and AW-SRV-055's.) A store that is not a
// TagLister reports none.
func RoundTags(ctx context.Context, ws sim.WorldStore, keys []string) ([]string, error) {
	tl, ok := ws.(TagLister)
	if !ok {
		return nil, nil
	}
	seen := map[string]int{}
	for _, k := range keys {
		names, err := tl.ListTags(ctx, k)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			seen[n]++
		}
	}
	out := make([]string, 0, len(seen))
	for n, c := range seen {
		if c == len(keys) {
			out = append(out, n)
		}
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

// ValidTagName reports whether name is in the tag grammar.
func ValidTagName(name string) bool { return TagNamePattern.MatchString(name) }

// ParseTagKey reads a tag key back into the snapshot key it tags and the tag's
// name. Reports false for anything else, a snapshot key included.
func ParseTagKey(key string) (object, name string, ok bool) {
	i := strings.LastIndex(key, tagDir+"/")
	if i < 0 {
		return "", "", false
	}
	object, name = key[:i], key[i+len(tagDir)+1:]
	if _, _, _, _, ok := sim.ParseSnapshotKey(object); !ok || !ValidTagName(name) {
		return "", "", false
	}
	return object, name, true
}

// TagStore is a snapshot store that holds round tags and can delete: what
// Admin.TagSnapshotRound, ListSnapshotRounds and the retention sweep need
// beyond sim.WorldStore. Both stores implement it. A plain sim.WorldStore
// reads as one with no tags.
type TagStore interface {
	sim.WorldStore
	TagLister
	// ZoneTags returns, for each of a Zone's snapshot keys with any tag, the
	// names it carries, sorted.
	ZoneTags(ctx context.Context, zone sim.ZoneID) (map[string][]string, error)
	// Delete removes the object at key. A key that isn't there is not an
	// error: a sweep that crashed half-way runs again over what is left.
	Delete(ctx context.Context, key string) error
}

var (
	_ TagStore = (*FS)(nil)
	_ TagStore = (*S3)(nil)
)

// attachTags sets Tags and Tagged on every round from the store's tag
// listing, and returns the listing by snapshot key; a store that holds no
// tags leaves every round untagged. Tags is RoundTags' rule, a name on every
// object; Tagged is retention's, any name on any object.
func attachTags(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID, rounds []Round) (map[string][]string, error) {
	byKey := map[string][]string{}
	ts, ok := ws.(TagStore)
	if !ok {
		return byKey, nil
	}
	for _, z := range listed {
		tags, err := ts.ZoneTags(ctx, z)
		if err != nil {
			return nil, fmt.Errorf("store: list tags: zone %s: %w", z, err)
		}
		for k, names := range tags {
			byKey[k] = names
		}
	}
	for i := range rounds {
		r := &rounds[i]
		count := map[string]int{}
		for _, ref := range r.Zones {
			for _, n := range byKey[ref.Key] {
				count[n]++
			}
		}
		r.Tagged = len(count) > 0
		r.Tags = nil
		for n, c := range count {
			if c == len(r.Zones) {
				r.Tags = append(r.Tags, n)
			}
		}
		sort.Strings(r.Tags)
	}
	return byKey, nil
}

// TagResult is what TagRoundAt did.
type TagResult struct {
	Round Round
	// AlreadyTagged: every object of the round already carried the name, and
	// nothing was written.
	AlreadyTagged bool
}

// ErrRoundNotFound is a tag for a tick with no round, or for the newest
// Complete round when there is none.
var ErrRoundNotFound = errors.New("round not found")

// TagRoundAt tags the round at tick, or the newest Complete round when tick is
// 0, with name, writing the tag on every object of the round that lacks it.
// It refuses a name outside the grammar (ErrTagName), a tick with no round
// (ErrRoundNotFound), and a round that isn't Complete (*sim.ErrRoundIncomplete,
// or RoundAt's other typed refusals). Tagging a round that carries the name on
// every object writes nothing; one that carries it on some, after a crash
// between Puts, is completed.
func TagRoundAt(ctx context.Context, ws TagStore, listed []sim.ZoneID, zonesAt ZonesAt, name string, tick sim.Tick) (TagResult, error) {
	if !ValidTagName(name) {
		return TagResult{}, fmt.Errorf("%w, got %q", ErrTagName, name)
	}
	var (
		r   Round
		err error
	)
	if tick == 0 {
		var ok bool
		r, _, ok, err = NewestComplete(ctx, ws, listed, zonesAt)
		if err == nil && !ok {
			return TagResult{}, fmt.Errorf("%w: no complete round", ErrRoundNotFound)
		}
	} else {
		r, _, err = RoundAt(ctx, ws, listed, zonesAt, tick)
		var ri *sim.ErrRoundIncomplete
		if errors.As(err, &ri) && ri.Cause == sim.RoundMissing && len(r.Zones) == 0 {
			return TagResult{}, fmt.Errorf("%w: round %d", ErrRoundNotFound, tick)
		}
	}
	if err != nil {
		return TagResult{}, err
	}
	res := TagResult{Round: r, AlreadyTagged: true}
	for _, ref := range r.Zones {
		have, err := ws.ListTags(ctx, ref.Key)
		if err != nil {
			return TagResult{}, err
		}
		if i := sort.SearchStrings(have, name); i < len(have) && have[i] == name {
			continue
		}
		res.AlreadyTagged = false
		if err := ws.Put(ctx, TagKey(ref.Key, name), nil); err != nil {
			return TagResult{}, fmt.Errorf("store: tag %s: %w", ref.Key, err)
		}
	}
	return res, nil
}

// addTag files one parsed tag key into a listing's map.
func addTag(into map[string][]string, key string) {
	if object, name, ok := ParseTagKey(key); ok {
		into[object] = append(into[object], name)
	}
}

func sortTags(m map[string][]string) map[string][]string {
	for _, names := range m {
		sort.Strings(names)
	}
	return m
}
