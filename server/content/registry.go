// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

// Registry is the content store's write side, and the index the publish path
// answers from (AW-SRV-013). KafkaResolver is the read side the Loader uses;
// the two meet only at the topics.
//
// Every write, and the version number it assigns, happens under one lock. The
// server is a single replica (ADR-0001), which is also what AW-SRV-008's
// single-writer lock on the Account store rests on, so an in-process lock is
// the whole of the concurrency model. Last pointer move wins (ADR-0004).
//
// The index is rebuilt at open from the three content topics and the audit
// topic. The audit topic is the only history that survives compaction: the
// pointer topic keeps one record per pack, so the activation history
// ListVersions reports, and the real actor behind an acting-as publish (which
// the manifest's author can't carry), are read back from andara.audit.v1.
type Registry struct {
	blobs, versions, active recordlog.Log
	cache                   BlobCache
	now                     func() time.Time

	mu sync.Mutex
	// sizes is every blob the store holds, by hash, with its body's size:
	// what HasBlobs answers from, and what PublishVersion checks a
	// manifest's declared sizes against.
	sizes       map[[32]byte]uint64
	manifests   map[string]map[uint64]*contentv1.ContentVersion
	newest      map[string]uint64
	pointer     map[string]*contentv1.ActiveVersion
	history     map[string][]*contentv1.ActiveVersion
	publishedBy map[string]string // ManifestKey -> the real actor of the publish
}

// RegistryOptions configures a Registry.
type RegistryOptions struct {
	// Blobs, Versions and Active are the three content topics.
	Blobs, Versions, Active recordlog.Log
	// Audit is andara.audit.v1, read at open for history. Nil reads none.
	Audit recordlog.Log
	// Cache receives every blob written, so the Loader resolving a version
	// just published finds its blobs without a scan. The zero value is off.
	Cache BlobCache
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// ErrStaleParent: a publish named a parent that is no longer the pack's
// newest version. Someone published in between.
type ErrStaleParent struct {
	Pack           string
	Parent, Newest uint64
}

func (e *ErrStaleParent) Error() string {
	return fmt.Sprintf("parent_version %d is stale: %s's newest version is %d", e.Parent, e.Pack, e.Newest)
}

// OpenRegistry replays the store into an index.
func OpenRegistry(ctx context.Context, o RegistryOptions) (*Registry, error) {
	if o.Blobs == nil || o.Versions == nil || o.Active == nil {
		return nil, errors.New("content: the registry needs all three content topics")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &Registry{
		blobs: o.Blobs, versions: o.Versions, active: o.Active,
		cache: o.Cache, now: o.Now,
		sizes:       map[[32]byte]uint64{},
		manifests:   map[string]map[uint64]*contentv1.ContentVersion{},
		newest:      map[string]uint64{},
		pointer:     map[string]*contentv1.ActiveVersion{},
		history:     map[string][]*contentv1.ActiveVersion{},
		publishedBy: map[string]string{},
	}
	if err := o.Versions.Replay(ctx, func(rec recordlog.Record) error {
		if len(rec.Value) == 0 {
			return nil
		}
		var cv contentv1.ContentVersion
		if err := proto.Unmarshal(rec.Value, &cv); err != nil {
			return fmt.Errorf("content: decode manifest %q: %w", rec.Key, err)
		}
		r.index(&cv)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("content: replay versions: %w", err)
	}
	if err := o.Active.Replay(ctx, func(rec recordlog.Record) error {
		if len(rec.Value) == 0 {
			delete(r.pointer, rec.Key)
			return nil
		}
		var av contentv1.ActiveVersion
		if err := proto.Unmarshal(rec.Value, &av); err != nil {
			return fmt.Errorf("content: decode active pointer %q: %w", rec.Key, err)
		}
		r.pointer[av.GetPackId()] = &av
		return nil
	}); err != nil {
		return nil, fmt.Errorf("content: replay active pointers: %w", err)
	}
	// A blob's key is its hash, and the store is content-addressed, so the
	// index keeps presence and size, never the body.
	if err := o.Blobs.Replay(ctx, func(rec recordlog.Record) error {
		if len(rec.Key) != sha256.Size || len(rec.Value) == 0 {
			return nil
		}
		var b contentv1.Blob
		if err := proto.Unmarshal(rec.Value, &b); err != nil {
			return fmt.Errorf("content: decode blob %x: %w", rec.Key, err)
		}
		r.sizes[[32]byte([]byte(rec.Key))] = uint64(len(b.GetBody()))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("content: replay blobs: %w", err)
	}
	if o.Audit != nil {
		if err := o.Audit.Replay(ctx, func(rec recordlog.Record) error {
			var ar auditv1.AuditRecord
			if err := proto.Unmarshal(rec.Value, &ar); err != nil {
				return nil // not ours to judge; auth reads this topic too
			}
			r.replayAudit(&ar)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("content: replay audit: %w", err)
		}
	}
	for p := range r.history {
		sort.SliceStable(r.history[p], func(i, j int) bool {
			return r.history[p][i].GetActivatedAtUnixNano() < r.history[p][j].GetActivatedAtUnixNano()
		})
	}
	return r, nil
}

// replayAudit takes the history the content topics don't keep.
func (r *Registry) replayAudit(ar *auditv1.AuditRecord) {
	if ar.GetPackId() == "" || ar.GetOutcome() != auth.AuditOK {
		return
	}
	switch ar.GetAction() {
	case auth.ActionPublish:
		r.publishedBy[ManifestKey(ar.GetPackId(), ar.GetVersion())] = ar.GetActorAccountId()
	case auth.ActionActivate, auth.ActionRollback, auth.ActionOverride:
		r.history[ar.GetPackId()] = append(r.history[ar.GetPackId()], &contentv1.ActiveVersion{
			PackId: ar.GetPackId(), Version: ar.GetVersion(),
			ActivatedBy: ar.GetActorAccountId(), ActivatedAtUnixNano: ar.GetTsUnixNano(),
		})
	}
}

// index records a manifest. Caller holds mu, or is OpenRegistry.
func (r *Registry) index(cv *contentv1.ContentVersion) {
	pack := cv.GetPackId()
	if r.manifests[pack] == nil {
		r.manifests[pack] = map[uint64]*contentv1.ContentVersion{}
	}
	r.manifests[pack][cv.GetVersion()] = cv
	if cv.GetVersion() > r.newest[pack] {
		r.newest[pack] = cv.GetVersion()
	}
}

// HasBlob reports whether the store holds a blob with this hash.
func (r *Registry) HasBlob(hash []byte) bool {
	if len(hash) != sha256.Size {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sizes[[32]byte(hash)]
	return ok
}

// BlobSize is the size of the blob with this hash, if the store holds it.
func (r *Registry) BlobSize(hash []byte) (uint64, bool) {
	if len(hash) != sha256.Size {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.sizes[[32]byte(hash)]
	return n, ok
}

// PutBlob writes a blob the caller has already hashed and checked. A blob the
// store holds is not written again, and reported deduplicated.
func (r *Registry) PutBlob(ctx context.Context, hash []byte, mediaType string, body []byte) (deduplicated bool, err error) {
	if r.HasBlob(hash) {
		return true, nil
	}
	value, err := proto.Marshal(&contentv1.Blob{Hash: hash, Body: body, MediaType: mediaType})
	if err != nil {
		return false, err
	}
	if err := r.blobs.Append(ctx, string(hash), value); err != nil {
		return false, fmt.Errorf("content: write blob: %w", err)
	}
	_ = r.cache.Put(hash, body) // a cache that can't be written is slow, not wrong
	r.mu.Lock()
	r.sizes[[32]byte(hash)] = uint64(len(body))
	r.mu.Unlock()
	return false, nil
}

// Newest is the pack's newest version, 0 when it has none.
func (r *Registry) Newest(pack string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.newest[pack]
}

// Publish assigns cv the pack's next version and writes it, if parent is
// still the newest. actor is the real actor, kept for the self-approval
// check; cv.Author is the effective Account.
func (r *Registry) Publish(ctx context.Context, cv *contentv1.ContentVersion, parent uint64, actor string) (*contentv1.ContentVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pack := cv.GetPackId()
	if newest := r.newest[pack]; parent != newest {
		return nil, &ErrStaleParent{Pack: pack, Parent: parent, Newest: newest}
	}
	out := proto.Clone(cv).(*contentv1.ContentVersion)
	out.Version = r.newest[pack] + 1
	out.ParentVersion = parent
	out.PublishedAtUnixNano = r.now().UnixNano()
	if err := r.writeManifest(ctx, out); err != nil {
		return nil, err
	}
	r.publishedBy[ManifestKey(pack, out.Version)] = actor
	return proto.Clone(out).(*contentv1.ContentVersion), nil
}

// Approve sets approved_by on pack@version. The manifest is rewritten under
// the same key; compaction keeps the approved one. An approved version is
// left as it is, and reported with approved false: checked here, under the
// lock, so two approvals racing can't both write.
func (r *Registry) Approve(ctx context.Context, pack string, version uint64, by string) (out *contentv1.ContentVersion, approved bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cv, ok := r.manifests[pack][version]
	if !ok {
		return nil, false, &ErrManifestMissing{Pack: pack, Version: version}
	}
	if cv.GetApprovedBy() != "" {
		return proto.Clone(cv).(*contentv1.ContentVersion), false, nil
	}
	out = proto.Clone(cv).(*contentv1.ContentVersion)
	out.ApprovedBy, out.ApprovedAtUnixNano = by, r.now().UnixNano()
	if err := r.writeManifest(ctx, out); err != nil {
		return nil, false, err
	}
	return proto.Clone(out).(*contentv1.ContentVersion), true, nil
}

// writeManifest writes and indexes. Caller holds mu.
func (r *Registry) writeManifest(ctx context.Context, cv *contentv1.ContentVersion) error {
	value, err := proto.Marshal(cv)
	if err != nil {
		return err
	}
	if err := r.versions.Append(ctx, ManifestKey(cv.GetPackId(), cv.GetVersion()), value); err != nil {
		return fmt.Errorf("content: write manifest: %w", err)
	}
	r.index(cv)
	return nil
}

// MovePointer writes pack's Active Pointer and returns the version it moved
// from, 0 when nothing was active.
func (r *Registry) MovePointer(ctx context.Context, pack string, version uint64, by string) (previous uint64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	av := &contentv1.ActiveVersion{PackId: pack, Version: version, ActivatedBy: by, ActivatedAtUnixNano: r.now().UnixNano()}
	value, err := proto.Marshal(av)
	if err != nil {
		return 0, err
	}
	if err := r.active.Append(ctx, pack, value); err != nil {
		return 0, fmt.Errorf("content: write active pointer: %w", err)
	}
	if prev := r.pointer[pack]; prev != nil {
		previous = prev.GetVersion()
	}
	r.pointer[pack] = av
	r.history[pack] = append(r.history[pack], av)
	return previous, nil
}

// Manifest returns a copy of pack@version's manifest.
func (r *Registry) Manifest(pack string, version uint64) (*contentv1.ContentVersion, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cv, ok := r.manifests[pack][version]
	if !ok {
		return nil, false
	}
	return proto.Clone(cv).(*contentv1.ContentVersion), true
}

// Versions returns every version of pack, newest first; limit > 0 keeps that
// many.
func (r *Registry) Versions(pack string, limit int) []*contentv1.ContentVersion {
	r.mu.Lock()
	defer r.mu.Unlock()
	vs := make([]uint64, 0, len(r.manifests[pack]))
	for v := range r.manifests[pack] {
		vs = append(vs, v)
	}
	slices.Sort(vs)
	slices.Reverse(vs)
	if limit > 0 && len(vs) > limit {
		vs = vs[:limit]
	}
	out := make([]*contentv1.ContentVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, proto.Clone(r.manifests[pack][v]).(*contentv1.ContentVersion))
	}
	return out
}

// Pointer returns pack's Active Pointer, if it has one.
func (r *Registry) Pointer(pack string) (*contentv1.ActiveVersion, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	av, ok := r.pointer[pack]
	if !ok {
		return nil, false
	}
	return proto.Clone(av).(*contentv1.ActiveVersion), true
}

// Pointers returns every pack's active version.
func (r *Registry) Pointers() map[string]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]uint64, len(r.pointer))
	for p, av := range r.pointer {
		out[p] = av.GetVersion()
	}
	return out
}

// Activations returns pack's pointer moves, oldest first.
func (r *Registry) Activations(pack string) []*contentv1.ActiveVersion {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*contentv1.ActiveVersion, 0, len(r.history[pack]))
	for _, av := range r.history[pack] {
		out = append(out, proto.Clone(av).(*contentv1.ActiveVersion))
	}
	return out
}

// PublishedBy is the real actor who published pack@version, or "" when the
// history has no record of it.
func (r *Registry) PublishedBy(pack string, version uint64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.publishedBy[ManifestKey(pack, version)]
}

// BlobHashesDigest is the audit record's blob_hashes_sha256, and a core's
// digest: the sha256 of the sorted list of blob hashes.
func BlobHashesDigest(refs []*contentv1.BlobRef) []byte {
	hashes := make([][]byte, 0, len(refs))
	for _, ref := range refs {
		hashes = append(hashes, ref.GetHash())
	}
	slices.SortFunc(hashes, bytes.Compare)
	h := sha256.New()
	for _, x := range hashes {
		h.Write(x)
	}
	return h.Sum(nil)
}

// Close closes the three content topics. The audit log is the Account
// store's, and stays open.
func (r *Registry) Close() error {
	return errors.Join(r.blobs.Close(), r.versions.Close(), r.active.Close())
}

// PublishExact writes cv as exactly pack@version, with parent the pack's
// newest version below it: the server's own core at boot, which the build
// numbers (AW-SRV-013). An existing pack@version is never overwritten.
func (r *Registry) PublishExact(ctx context.Context, cv *contentv1.ContentVersion, version uint64, actor string) (*contentv1.ContentVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pack := cv.GetPackId()
	if _, ok := r.manifests[pack][version]; ok {
		return nil, fmt.Errorf("content: %s already exists", ManifestKey(pack, version))
	}
	var parent uint64
	for v := range r.manifests[pack] {
		if v < version && v > parent {
			parent = v
		}
	}
	out := proto.Clone(cv).(*contentv1.ContentVersion)
	out.Version, out.ParentVersion, out.PublishedAtUnixNano = version, parent, r.now().UnixNano()
	if err := r.writeManifest(ctx, out); err != nil {
		return nil, err
	}
	r.publishedBy[ManifestKey(pack, version)] = actor
	return proto.Clone(out).(*contentv1.ContentVersion), nil
}
