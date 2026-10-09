// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
	"sort"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"

	"github.com/valesordev/andara/content/lang"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	statev1 "github.com/valesordev/andara/gen/go/andara/state/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
)

// ErrorDomain is the ErrorInfo domain every publish-path error carries, so a
// client maps reasons rather than parsing messages (AW-SRV-013).
const ErrorDomain = "andara.content"

// The ErrorInfo reasons the contract pins (AW-SRV-013, Error taxonomy).
const (
	ErrReasonValidation   = "validation"
	ErrReasonHashMismatch = "blob_hash_mismatch"
	ErrReasonPackNotHeld  = "pack_not_held"
	ErrReasonSelfApproval = "self_approval"
	ErrReasonCorePublish  = "core_published_at_boot"
	ErrReasonOperatorOnly = "operator_only"
	ErrReasonUnapproved   = "unapproved"
	ErrReasonStaleParent  = "stale_parent"
	ErrReasonBlobTooLarge = "blob_too_large"
	ErrReasonPackTooLarge = "pack_too_large"
	ErrReasonNotFound     = "not_found"
)

// Code is the gRPC status an AdminError crosses the wire as. The gateway maps
// it; this package stays transport-free.
type Code int

const (
	CodeInvalidArgument Code = iota + 1
	CodePermissionDenied
	CodeFailedPrecondition
	CodeResourceExhausted
	CodeNotFound
	CodeUnauthenticated
	CodeUnavailable
)

// AdminError is a publish-path refusal: a status code, the contract's reason,
// and the status detail it carries, if any (PublishFindings on validation,
// ActivationRefusal on AC-14's refusals).
type AdminError struct {
	Code   Code
	Reason string
	Detail proto.Message
	Err    error
}

func (e *AdminError) Error() string { return e.Err.Error() }
func (e *AdminError) Unwrap() error { return e.Err }

// Status is how the gateway puts an AdminError on the wire without importing
// this package: the gRPC status code, the ErrorInfo domain and reason, and
// the status detail, if any.
func (e *AdminError) Status() (code uint32, domain, reason string, detail proto.Message) {
	return grpcCodes[e.Code], ErrorDomain, e.Reason, e.Detail
}

// grpcCodes are the gRPC status codes (google.golang.org/grpc/codes), which
// connect's codes equal.
var grpcCodes = map[Code]uint32{
	CodeInvalidArgument:    3,
	CodeNotFound:           5,
	CodePermissionDenied:   7,
	CodeResourceExhausted:  8,
	CodeFailedPrecondition: 9,
	CodeUnavailable:        14,
	CodeUnauthenticated:    16,
}

func adminErr(code Code, reason string, format string, args ...any) *AdminError {
	return &AdminError{Code: code, Reason: reason, Err: fmt.Errorf(format, args...)}
}

// Limits the Admin wire format sets, not configuration.
const (
	// MaxHasBlobs is the most hashes one HasBlobs may ask about (admin.proto).
	MaxHasBlobs = 10000
	// BlobChunkBytes is the most one GetBlob chunk carries, and the most one
	// PublishBlob data chunk may carry (docs/feedback/AW-SRV-013-publish-path.md,
	// For architecture 1).
	BlobChunkBytes = 1 << 20
)

// PackHolder answers which packs an Account may build. auth.Store is one.
type PackHolder interface {
	BuilderPacks(accountID string) []string
}

// AdminOptions configures the publish path.
type AdminOptions struct {
	Registry *Registry
	// Loader supplies the publish gate and the activation check: the same
	// validator, against the same World in effect, as a load.
	Loader *Loader
	// Blobs reads blob bodies back: the Loader's Store (a KafkaResolver,
	// cache first).
	Blobs    Store
	Accounts PackHolder
	Auditor  *auth.Auditor
	Metrics  *PublishMetrics
	Log      *slog.Logger
	Tracer   trace.Tracer
	// MaxBlobBytes and MaxPackBytes are content.max_blob_bytes and
	// content.max_pack_bytes.
	MaxBlobBytes, MaxPackBytes int64
	// CorePack is content.core_pack.
	CorePack string
	// OperatorSelfApproval is content.operator_self_approval.
	OperatorSelfApproval bool
	// Reload is ReloadContent: reconcile with the store, then the versions in
	// effect.
	Reload func(context.Context) (map[string]uint64, error)
}

// Admin is the content publish path (AW-SRV-013): publish, approve, activate,
// roll back, and read, each step authorized and audited. The gateway calls it
// with the caller's Principal on ctx.
type Admin struct {
	o      AdminOptions
	log    *slog.Logger
	tracer trace.Tracer
	m      *PublishMetrics
}

// NewAdmin builds the publish path.
func NewAdmin(o AdminOptions) (*Admin, error) {
	if o.Registry == nil || o.Loader == nil || o.Blobs == nil || o.Accounts == nil || o.Auditor == nil {
		return nil, errors.New("content: the publish path needs a registry, loader, blob store, account store and auditor")
	}
	if o.CorePack == "" {
		o.CorePack = CorePack
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.Metrics == nil {
		o.Metrics = NewPublishMetrics(nil)
	}
	return &Admin{o: o, log: o.Log, tracer: o.Tracer, m: o.Metrics}, nil
}

// --- authorization -----------------------------------------------------------

// caller is the authenticated Principal and what it may do with one pack.
type caller struct {
	p        auth.Principal
	operator bool
	// holds is whether the effective Account holds the pack. An Operator who
	// doesn't is acting on it by override, and audited so.
	holds bool
}

func (a *Admin) principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || p.AccountID == "" {
		return auth.Principal{}, &AdminError{Code: CodeUnauthenticated, Err: auth.ErrUnauthenticated}
	}
	return p, nil
}

// authorize is the matrix's first three columns: a Builder holding the pack,
// or an Operator. andara.core is read by any Builder, and written by no RPC.
func (a *Admin) authorize(ctx context.Context, pack string, read bool) (caller, error) {
	p, err := a.principal(ctx)
	if err != nil {
		return caller{}, err
	}
	c := caller{p: p, operator: p.Has(auth.RoleOperator)}
	c.holds = p.Has(auth.RoleBuilder) && slices.Contains(a.o.Accounts.BuilderPacks(p.EffectiveAccountID()), pack)
	switch {
	case c.operator, c.holds:
		return c, nil
	case read && pack == a.o.CorePack && p.Has(auth.RoleBuilder):
		return c, nil
	}
	return c, adminErr(CodePermissionDenied, ErrReasonPackNotHeld, "%s does not hold pack %s", p.EffectiveAccountID(), pack)
}

// --- audit and logs ----------------------------------------------------------

// record writes one audit record under its own audit.write span.
func (a *Admin) record(ctx context.Context, c caller, action, outcome, pack string, version uint64, ca auth.ContentAudit, detail string) {
	ctx, span := a.tracer.Start(ctx, "audit.write")
	defer span.End()
	ca.PackID, ca.Version = pack, version
	a.o.Auditor.Record(ctx, auth.Entry{
		Actor: c.p, Action: action, Target: ManifestKey(pack, version),
		Outcome: outcome, Detail: detail, Content: &ca,
	})
}

// attrs are the fields every publish-path log line carries.
func attrs(ctx context.Context, c caller, pack string, version uint64, more ...slog.Attr) []slog.Attr {
	return append([]slog.Attr{
		slog.String("actor_account_id", c.p.AccountID),
		slog.String("acting_as_account_id", c.p.ActingAs),
		slog.String("pack_id", pack),
		slog.Uint64("version", version),
		slog.String("session_id", auth.SessionIDFrom(ctx)),
		slog.String("trace_id", traceIDOf(ctx)),
	}, more...)
}

func traceIDOf(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// --- blobs -------------------------------------------------------------------

// HasBlobs reports which of the hashes the store holds.
func (a *Admin) HasBlobs(ctx context.Context, req *adminv1.HasBlobsRequest) (*adminv1.HasBlobsResponse, error) {
	if _, err := a.authorize(ctx, req.GetPackId(), false); err != nil {
		return nil, err
	}
	if n := len(req.GetHashes()); n > MaxHasBlobs {
		return nil, adminErr(CodeInvalidArgument, ErrReasonValidation, "HasBlobs asks about %d hashes; at most %d", n, MaxHasBlobs)
	}
	out := &adminv1.HasBlobsResponse{Present: make([]bool, len(req.GetHashes()))}
	for i, h := range req.GetHashes() {
		out.Present[i] = a.o.Registry.HasBlob(h)
	}
	return out, nil
}

// PublishBlob writes one blob: a header, then its body in chunks, from
// receive, which returns io.EOF after the last message. It's a func rather
// than an interface so the gateway can name the same type without importing
// this package. The size is
// checked against the header before anything is read, and against the bytes
// as they arrive, so nothing over the limit is ever produced (AC-12). The body
// is hashed before it's written, and a mismatch writes nothing.
func (a *Admin) PublishBlob(ctx context.Context, receive func() (*adminv1.PublishBlobRequest, error)) (*adminv1.PublishBlobResponse, error) {
	first, err := receive()
	if err != nil {
		return nil, adminErr(CodeInvalidArgument, ErrReasonValidation, "PublishBlob sent no header: %v", err)
	}
	hdr := first.GetHeader()
	if hdr == nil {
		return nil, adminErr(CodeInvalidArgument, ErrReasonValidation, "PublishBlob's first message must be its header")
	}
	pack := hdr.GetPackId()
	ctx, span := a.tracer.Start(ctx, "content.publish_blob", trace.WithAttributes(attribute.String("pack_id", pack)))
	defer span.End()

	c, err := a.authorizePublish(ctx, pack)
	if err != nil {
		return nil, err
	}
	if len(hdr.GetHash()) != sha256.Size {
		return nil, adminErr(CodeInvalidArgument, ErrReasonHashMismatch, "the header's hash is %d bytes; a sha256 is %d", len(hdr.GetHash()), sha256.Size)
	}
	if limit := a.o.MaxBlobBytes; limit > 0 && int64(hdr.GetSizeBytes()) > limit {
		return nil, a.tooLarge(ctx, c, pack, hdr.GetPath(), int64(hdr.GetSizeBytes()))
	}

	body := make([]byte, 0, hdr.GetSizeBytes())
	for {
		msg, err := receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		data := msg.GetData()
		if msg.GetHeader() != nil {
			return nil, adminErr(CodeInvalidArgument, ErrReasonValidation, "PublishBlob sent a second header")
		}
		if len(data) > BlobChunkBytes {
			return nil, adminErr(CodeInvalidArgument, ErrReasonValidation, "a PublishBlob chunk is %d bytes; at most %d", len(data), BlobChunkBytes)
		}
		if limit := a.o.MaxBlobBytes; limit > 0 && int64(len(body)+len(data)) > limit {
			return nil, a.tooLarge(ctx, c, pack, hdr.GetPath(), int64(len(body)+len(data)))
		}
		body = append(body, data...)
	}
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], hdr.GetHash()) || uint64(len(body)) != hdr.GetSizeBytes() {
		return nil, adminErr(CodeInvalidArgument, ErrReasonHashMismatch,
			"%s: the body is %d bytes hashing to %x; the header says %d bytes hashing to %x",
			hdr.GetPath(), len(body), sum, hdr.GetSizeBytes(), hdr.GetHash())
	}

	wctx, wspan := a.tracer.Start(ctx, "content.write_blob")
	dedup, err := a.o.Registry.PutBlob(wctx, sum[:], hdr.GetMediaType(), body)
	wspan.End()
	if err != nil {
		return nil, &AdminError{Code: CodeUnavailable, Err: err}
	}
	if !dedup {
		a.m.BlobBytes.Add(float64(len(body)))
	}
	span.SetAttributes(attribute.Int("bytes", len(body)), attribute.Bool("deduplicated", dedup))
	return &adminv1.PublishBlobResponse{Hash: sum[:], Deduplicated: dedup}, nil
}

func (a *Admin) tooLarge(ctx context.Context, c caller, pack, path string, n int64) error {
	a.m.Publishes.WithLabelValues(PublishTooLarge).Inc()
	err := adminErr(CodeResourceExhausted, ErrReasonBlobTooLarge, "%s is %d bytes; content.max_blob_bytes is %d", path, n, a.o.MaxBlobBytes)
	a.record(ctx, c, auth.ActionPublish, "too_large", pack, 0, auth.ContentAudit{}, err.Error())
	a.log.LogAttrs(ctx, slog.LevelWarn, "content publish refused: too large", attrs(ctx, c, pack, 0, slog.String("detail", err.Error()))...)
	return err
}

// authorizePublish is authorize for the two RPCs that write a pack's
// content, with andara.core refused to everyone first (AC-11): no RPC
// publishes it, whoever calls.
func (a *Admin) authorizePublish(ctx context.Context, pack string) (caller, error) {
	if pack == a.o.CorePack {
		p, err := a.principal(ctx)
		if err != nil {
			return caller{}, err
		}
		return caller{}, a.corePublishRefused(ctx, caller{p: p, operator: p.Has(auth.RoleOperator)})
	}
	c, err := a.authorize(ctx, pack, false)
	if err != nil {
		a.m.Publishes.WithLabelValues(PublishDenied).Inc()
		a.record(ctx, c, auth.ActionPublish, auth.AuditDenied, pack, 0, auth.ContentAudit{}, err.Error())
		a.log.LogAttrs(ctx, slog.LevelWarn, "content publish denied", attrs(ctx, c, pack, 0, slog.String("detail", err.Error()))...)
		return c, err
	}
	return c, nil
}

func (a *Admin) corePublishRefused(ctx context.Context, c caller) error {
	a.m.Publishes.WithLabelValues(PublishDenied).Inc()
	err := adminErr(CodePermissionDenied, ErrReasonCorePublish, "%s is published by the server at boot; no RPC publishes it", a.o.CorePack)
	a.record(ctx, c, auth.ActionPublish, auth.AuditDenied, a.o.CorePack, 0, auth.ContentAudit{}, err.Error())
	return err
}

// --- publish -----------------------------------------------------------------

// PublishVersion validates a version from blobs already written, and writes
// its manifest if the validator accepts it. The Active Pointer doesn't move.
func (a *Admin) PublishVersion(ctx context.Context, req *adminv1.PublishVersionRequest) (*adminv1.PublishVersionResponse, error) {
	pack := req.GetPackId()
	ctx, span := a.tracer.Start(ctx, "content.publish", trace.WithAttributes(attribute.String("pack_id", pack)))
	defer span.End()

	c, err := a.authorizePublish(ctx, pack)
	if err != nil {
		return nil, err
	}
	refs := req.GetBlobs()
	digest := BlobHashesDigest(refs)
	override := c.operator && !c.holds

	if err := a.checkRefs(refs); err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if UnsafeBlobPath(ref.GetPath()) {
			return nil, a.unsafePath(ctx, c, pack, ref.GetPath(), digest, override)
		}
	}
	// The limits are checked against the store's sizes, not the caller's:
	// a manifest declaring 0 bytes for a large blob would otherwise pass
	// content.max_pack_bytes. A declared size the store disagrees with is
	// refused. A blob the store doesn't hold is left to Resolve, which
	// refuses it as blob_missing.
	var total int64
	for _, ref := range refs {
		n := int64(ref.GetSizeBytes())
		if actual, ok := a.o.Registry.BlobSize(ref.GetHash()); ok {
			if actual != ref.GetSizeBytes() {
				return nil, adminErr(CodeInvalidArgument, ErrReasonValidation, "%s declares %d bytes; the blob with its hash is %d", ref.GetPath(), ref.GetSizeBytes(), actual)
			}
			n = int64(actual)
		}
		if limit := a.o.MaxBlobBytes; limit > 0 && n > limit {
			return nil, a.tooLarge(ctx, c, pack, ref.GetPath(), n)
		}
		total += n
	}
	if limit := a.o.MaxPackBytes; limit > 0 && total > limit {
		a.m.Publishes.WithLabelValues(PublishTooLarge).Inc()
		err := adminErr(CodeResourceExhausted, ErrReasonPackTooLarge, "%s is %d bytes; content.max_pack_bytes is %d", pack, total, limit)
		a.record(ctx, c, auth.ActionPublish, "too_large", pack, 0, auth.ContentAudit{BlobHashesSHA256: digest, Override: override}, err.Error())
		return nil, err
	}
	if newest := a.o.Registry.Newest(pack); req.GetParentVersion() != newest {
		return nil, a.staleParent(ctx, c, pack, req.GetParentVersion(), newest, digest, override)
	}

	cv := &contentv1.ContentVersion{PackId: pack, Blobs: refs, Author: c.p.EffectiveAccountID()}
	bodies, err := a.o.Blobs.Blobs(ctx, refs)
	res, rerr := a.resolveCandidate(ctx, cv, bodies, err)
	var refusing, warnings []sim.ValidationError
	switch {
	case rerr != nil && IsStoreFault(rerr):
		return nil, &AdminError{Code: CodeUnavailable, Err: rerr}
	case rerr != nil:
		refusing = Findings(rerr)
	default:
		cv.CoreVersion = res.CoreVersion
		refusing, warnings = a.o.Loader.CheckPublish(ctx, res)
	}
	if len(refusing) > 0 {
		return nil, a.rejected(ctx, c, pack, refusing, warnings, digest, override)
	}

	mctx, mspan := a.tracer.Start(ctx, "content.write_manifest")
	out, err := a.o.Registry.Publish(mctx, cv, req.GetParentVersion(), c.p.AccountID)
	mspan.End()
	var stale *ErrStaleParent
	switch {
	case errors.As(err, &stale):
		return nil, a.staleParent(ctx, c, pack, stale.Parent, stale.Newest, digest, override)
	case err != nil:
		return nil, &AdminError{Code: CodeUnavailable, Err: err}
	}
	a.m.Publishes.WithLabelValues(PublishOK).Inc()
	a.record(ctx, c, auth.ActionPublish, auth.AuditOK, pack, out.GetVersion(), auth.ContentAudit{BlobHashesSHA256: digest, Override: override, FindingsCount: uint32(len(warnings))}, "")
	a.log.LogAttrs(ctx, slog.LevelInfo, "content published", attrs(ctx, c, pack, out.GetVersion(),
		slog.Uint64("core_version", out.GetCoreVersion()), slog.Int("warnings", len(warnings)))...)
	return &adminv1.PublishVersionResponse{Version: out.GetVersion(), CoreVersion: out.GetCoreVersion(), Warnings: Diagnostics(warnings, contentv1.Severity_WARNING)}, nil
}

// checkRefs refuses a manifest that isn't one: a ref without a path or a
// sha256, or a path twice.
func (a *Admin) checkRefs(refs []*contentv1.BlobRef) error {
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		switch {
		case ref.GetPath() == "":
			return adminErr(CodeInvalidArgument, ErrReasonValidation, "a blob has no path")
		case len(ref.GetHash()) != sha256.Size:
			return adminErr(CodeInvalidArgument, ErrReasonValidation, "%s: the hash is %d bytes; a sha256 is %d", ref.GetPath(), len(ref.GetHash()), sha256.Size)
		case seen[ref.GetPath()]:
			return adminErr(CodeInvalidArgument, ErrReasonValidation, "%s is named twice", ref.GetPath())
		}
		seen[ref.GetPath()] = true
	}
	return nil
}

// UnsafeBlobPath reports whether a manifest path could leave the pack it is
// published in, or can't be written inside it (#267). The rule is the CLI's
// unsafe_source_path. That is filepath.IsLocal on the fetching Builder's
// machine, so the gate applies it as every OS would, whatever OS the server
// runs on. It refuses a path that:
//   - is absolute, or holds a backslash, a ".", ".." or empty element, or
//     isn't clean under path.Clean. "." alone names no file, and a fetch
//     would write onto its output directory (contract amended on #267).
//   - holds a colon or a NUL byte: a Windows drive ("C:/x") or stream, and
//     a name no OS writes (review of #322).
//   - has an element that is a Windows device name (lang.UnportableElement,
//     the rule the compiler reports offline as unportable_name).
//
// `content fetch` writes blobs to disk by these paths on every Builder's
// machine that fetches the version, so the gate refuses them rather than
// trusting each client's guard.
func UnsafeBlobPath(p string) bool {
	if strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return true
	}
	for _, el := range strings.Split(p, "/") {
		if el == "" || el == "." || el == ".." || lang.UnportableElement(el) {
			return true
		}
	}
	return false
}

// unsafePath refuses a manifest naming a path that leaves the pack: reason
// validation, audited as a reject, before any blob is read or manifest
// written.
func (a *Admin) unsafePath(ctx context.Context, c caller, pack, p string, digest []byte, override bool) error {
	a.m.Publishes.WithLabelValues(PublishRejected).Inc()
	err := adminErr(CodeInvalidArgument, ErrReasonValidation, "%s: the path %q leaves the pack", pack, p)
	a.record(ctx, c, auth.ActionReject, "rejected", pack, 0, auth.ContentAudit{BlobHashesSHA256: digest, Override: override}, err.Error())
	a.log.LogAttrs(ctx, slog.LevelWarn, "content publish rejected: unsafe path", attrs(ctx, c, pack, 0, slog.String("path", p), slog.String("detail", err.Error()))...)
	return err
}

func (a *Admin) staleParent(ctx context.Context, c caller, pack string, parent, newest uint64, digest []byte, override bool) error {
	a.m.Publishes.WithLabelValues(PublishStaleParent).Inc()
	err := &AdminError{Code: CodeFailedPrecondition, Reason: ErrReasonStaleParent, Err: &ErrStaleParent{Pack: pack, Parent: parent, Newest: newest}}
	a.record(ctx, c, auth.ActionPublish, "stale_parent", pack, 0, auth.ContentAudit{BlobHashesSHA256: digest, Override: override}, err.Error())
	a.log.LogAttrs(ctx, slog.LevelWarn, "content publish refused: stale parent", attrs(ctx, c, pack, 0, slog.String("detail", err.Error()))...)
	return err
}

func (a *Admin) rejected(ctx context.Context, c caller, pack string, refusing, warnings []sim.ValidationError, digest []byte, override bool) error {
	a.m.Publishes.WithLabelValues(PublishRejected).Inc()
	for _, f := range refusing {
		a.m.ValidationFailures.WithLabelValues(string(f.Code)).Inc()
	}
	findings := append(Diagnostics(refusing, contentv1.Severity_ERROR), Diagnostics(warnings, contentv1.Severity_WARNING)...)
	err := &AdminError{
		Code: CodeInvalidArgument, Reason: ErrReasonValidation,
		Detail: &adminv1.PublishFindings{Findings: findings},
		Err:    fmt.Errorf("%s refused: %d findings, the first %s: %s", pack, len(refusing), refusing[0].Code, refusing[0].Detail),
	}
	a.record(ctx, c, auth.ActionReject, "rejected", pack, 0,
		auth.ContentAudit{BlobHashesSHA256: digest, Override: override, FindingsCount: uint32(len(refusing))}, err.Error())
	a.log.LogAttrs(ctx, slog.LevelWarn, "content publish rejected", attrs(ctx, c, pack, 0,
		slog.Int("findings_count", len(refusing)), slog.String("code", string(refusing[0].Code)))...)
	return err
}

// resolveCandidate decodes a manifest that isn't written yet, from bodies the
// store already holds, with the same decoder a load uses. core_version comes
// from the pack's own `requires` clause, never from the caller.
func (a *Admin) resolveCandidate(ctx context.Context, cv *contentv1.ContentVersion, bodies map[string][]byte, fetchErr error) (*Resolved, error) {
	if fetchErr != nil {
		return nil, fetchErr
	}
	res, err := Resolve(ctx, candidateStore{cv: cv, bodies: bodies}, cv.GetPackId(), 0)
	if err != nil {
		return nil, err
	}
	core, err := requiredCore(cv.GetPackId(), a.o.CorePack, res.Source, bodies)
	if err != nil {
		return nil, err
	}
	res.CoreVersion = core
	return res, nil
}

// candidateStore serves one manifest and its bodies to Resolve.
type candidateStore struct {
	cv     *contentv1.ContentVersion
	bodies map[string][]byte
}

func (s candidateStore) Active(context.Context) (map[string]uint64, error) { return nil, nil }
func (s candidateStore) Manifest(context.Context, string, uint64) (*contentv1.ContentVersion, error) {
	return s.cv, nil
}
func (s candidateStore) Blobs(context.Context, []*contentv1.BlobRef) (map[string][]byte, error) {
	return s.bodies, nil
}

// requiredCore reads `pack <id> requires andara.core@N` from the pack's
// published sources. A pack published without sources pins no core (0),
// which the Loader treats as compatible with any.
func requiredCore(pack, corePack string, sources []*contentv1.BlobRef, bodies map[string][]byte) (uint64, error) {
	for _, ref := range sources {
		f, ds := lang.ParseFile(ref.GetPath(), string(bodies[ref.GetPath()]))
		if len(ds) > 0 {
			continue // compiled output is what's validated; a source that doesn't parse pins nothing
		}
		for _, d := range f.Decls {
			pd, ok := d.(*lang.PackDecl)
			if !ok {
				continue
			}
			if pd.Name != pack {
				return 0, &ErrValidation{Findings: []sim.ValidationError{{File: ref.GetPath(), Code: sim.ErrPackMismatch,
					Detail: fmt.Sprintf("%s declares pack %s, published as %s", ref.GetPath(), pd.Name, pack)}}}
			}
			if pd.Requires == nil {
				return 0, nil
			}
			if pd.Requires.Pack != corePack {
				return 0, &ErrValidation{Findings: []sim.ValidationError{{File: ref.GetPath(), Code: sim.ErrPackMismatch,
					Detail: fmt.Sprintf("%s requires %s; a pack may require only %s", ref.GetPath(), pd.Requires.Pack, corePack)}}}
			}
			return uint64(pd.Requires.Version), nil
		}
	}
	return 0, nil
}

// Diagnostics renders loader findings in errors.md §1's shape. The loader
// knows no column, so col is 0; the chain is the Zone, Room and Exit, or the
// Template.
//
// The file and line are the compiled blob's. A caller holding the pack's
// source — `andara-cli`, which compiled it — places each finding back on the
// `.aw` line with the compiler's source map (lang.SourceMap, AW-CLI-002),
// keyed by this chain.
func Diagnostics(fs []sim.ValidationError, sev contentv1.Severity) []*contentv1.Diagnostic {
	out := make([]*contentv1.Diagnostic, 0, len(fs))
	for _, f := range fs {
		d := &contentv1.Diagnostic{File: f.File, Line: uint32(max(f.Line, 0)), Code: string(f.Code), Message: f.Detail, Severity: sev}
		if f.Pack != "" {
			// Another pack's finding (errors.md §1 rule 10.6): its blob path,
			// no position, no chain. The caller has no source to place it on.
			d.Pack = f.Pack
		} else {
			d.Chain = FindingChain(f)
		}
		out = append(out, d)
	}
	return out
}

// FindingChain is a loader finding's declaration chain, as the compiler
// reports the same finding (errors.md §1, AW-CLI-002 AC-4): the whole
// inheritance chain for a finding about the chain; otherwise the Template, or
// the Zone and Room, then the Exit direction or the Component type it's about.
func FindingChain(f sim.ValidationError) []string {
	if len(f.Chain) > 0 {
		out := make([]string, len(f.Chain))
		for i, c := range f.Chain {
			out[i] = string(c)
		}
		return out
	}
	var chain []string
	switch {
	case f.Template != "":
		chain = []string{string(f.Template)}
	case f.Zone == "":
		return nil
	case f.Code == sim.ErrFallbackMissing:
		// The Room it names is the missing one: a reference, not a
		// declaration. The finding is about the Zone.
		return []string{string(f.Zone)}
	default:
		chain = []string{string(f.Zone)}
		if f.Room != "" {
			chain = append(chain, string(f.Room))
			if f.Exit != "" {
				chain = append(chain, string(f.Exit))
			}
		}
	}
	if f.Component != "" {
		chain = append(chain, string(f.Component))
	}
	return chain
}

// --- approve -----------------------------------------------------------------

// ApproveVersion records a second identity's approval of pack@version. A
// Builder may not approve what they published; an Operator may, flagged and
// counted apart, while content.operator_self_approval is on (AC-4, AC-13).
func (a *Admin) ApproveVersion(ctx context.Context, req *adminv1.ApproveVersionRequest) (*adminv1.ApproveVersionResponse, error) {
	pack, version := req.GetPackId(), req.GetVersion()
	ctx, span := a.tracer.Start(ctx, "content.approve", trace.WithAttributes(attribute.String("pack_id", pack), attribute.Int64("version", int64(version))))
	defer span.End()

	c, err := a.authorize(ctx, pack, false)
	if err != nil {
		a.m.Approvals.WithLabelValues(ApprovalDenied).Inc()
		a.record(ctx, c, auth.ActionApprove, auth.AuditDenied, pack, version, auth.ContentAudit{}, err.Error())
		return nil, err
	}
	cv, ok := a.o.Registry.Manifest(pack, version)
	if !ok {
		return nil, adminErr(CodeNotFound, ErrReasonNotFound, "%s@%d is not published", pack, version)
	}
	digest := BlobHashesDigest(cv.GetBlobs())
	publisher := []string{cv.GetAuthor(), a.o.Registry.PublishedBy(pack, version)}
	self := slices.Contains(publisher, c.p.AccountID) || slices.Contains(publisher, c.p.EffectiveAccountID())
	if self && (!c.operator || !a.o.OperatorSelfApproval) {
		a.m.Approvals.WithLabelValues(ApprovalSelf).Inc()
		err := adminErr(CodePermissionDenied, ErrReasonSelfApproval, "%s published %s@%d and may not approve it; a second Builder holding %s must", c.p.AccountID, pack, version, pack)
		a.record(ctx, c, auth.ActionApprove, auth.AuditDenied, pack, version, auth.ContentAudit{BlobHashesSHA256: digest, SelfApproval: true}, err.Error())
		a.log.LogAttrs(ctx, slog.LevelWarn, "content approval refused: self-approval", attrs(ctx, c, pack, version)...)
		return nil, err
	}
	mctx, mspan := a.tracer.Start(ctx, "content.write_manifest")
	out, approved, err := a.o.Registry.Approve(mctx, pack, version, c.p.AccountID)
	mspan.End()
	if err != nil {
		return nil, &AdminError{Code: CodeUnavailable, Err: err}
	}
	if !approved {
		// Approvals bind to pack@version and never expire; a second one,
		// racing or not, changes nothing and records nothing.
		return &adminv1.ApproveVersionResponse{ApprovedBy: out.GetApprovedBy(), ApprovedAtUnixNano: out.GetApprovedAtUnixNano()}, nil
	}
	outcome := ApprovalOK
	if self {
		outcome = ApprovalSelfOperator
		a.log.LogAttrs(ctx, slog.LevelWarn, "content approved by its own publisher (operator self-approval)",
			attrs(ctx, c, pack, version, slog.Bool("self_approval", true))...)
	} else {
		a.log.LogAttrs(ctx, slog.LevelInfo, "content approved", attrs(ctx, c, pack, version)...)
	}
	a.m.Approvals.WithLabelValues(outcome).Inc()
	a.record(ctx, c, auth.ActionApprove, auth.AuditOK, pack, version, auth.ContentAudit{BlobHashesSHA256: digest, SelfApproval: self}, "")
	return &adminv1.ApproveVersionResponse{ApprovedBy: out.GetApprovedBy(), ApprovedAtUnixNano: out.GetApprovedAtUnixNano(), SelfApproval: self}, nil
}

// --- activate ----------------------------------------------------------------

// ActivateVersion moves pack's Active Pointer to version: forward, or back to
// a version already approved, which needs no fresh approval. An Operator may
// skip approval with override and a reason. Nothing skips AC-14's refusals.
func (a *Admin) ActivateVersion(ctx context.Context, req *adminv1.ActivateVersionRequest) (*adminv1.ActivateVersionResponse, error) {
	pack, version := req.GetPackId(), req.GetVersion()
	// The ActivateVersion server span, which the pointer record carries so
	// the Loader's content.load links to it (AW-SRV-045).
	activation := command.TraceParent(ctx)
	ctx, span := a.tracer.Start(ctx, "content.activate", trace.WithAttributes(attribute.String("pack_id", pack), attribute.Int64("version", int64(version))))
	defer span.End()

	isCore := pack == a.o.CorePack
	c, err := a.authorize(ctx, pack, isCore)
	if err == nil && isCore && !c.operator {
		err = adminErr(CodePermissionDenied, ErrReasonOperatorOnly, "only an Operator moves %s's pointer", pack)
	}
	if err == nil && req.GetOverride() && !c.operator {
		err = adminErr(CodePermissionDenied, ErrReasonOperatorOnly, "only an Operator may override approval")
	}
	if err != nil {
		a.record(ctx, c, auth.ActionActivate, auth.AuditDenied, pack, version, auth.ContentAudit{Override: req.GetOverride(), Reason: req.GetReason()}, err.Error())
		return nil, err
	}
	if req.GetOverride() && strings.TrimSpace(req.GetReason()) == "" {
		return nil, adminErr(CodeInvalidArgument, ErrReasonValidation, "an override needs a reason")
	}
	cv, ok := a.o.Registry.Manifest(pack, version)
	if !ok {
		return nil, adminErr(CodeNotFound, ErrReasonNotFound, "%s@%d is not published", pack, version)
	}
	digest := BlobHashesDigest(cv.GetBlobs())
	approved := cv.GetApprovedBy() != "" || isCore
	overridden := !approved && req.GetOverride()
	if !approved && !overridden {
		a.m.ActivationsRefused.WithLabelValues(RefusedUnapproved).Inc()
		err := adminErr(CodeFailedPrecondition, ErrReasonUnapproved, "%s@%d has no approval: a second Builder holding %s, or an Operator, must approve it first", pack, version, pack)
		a.record(ctx, c, auth.ActionActivate, "refused", pack, version, auth.ContentAudit{BlobHashesSHA256: digest}, err.Error())
		a.log.LogAttrs(ctx, slog.LevelWarn, "content activation refused", attrs(ctx, c, pack, version, slog.String("reason", RefusedUnapproved))...)
		return nil, err
	}

	refusal, err := a.o.Loader.CheckActivation(ctx, pack, version)
	if err != nil {
		return nil, &AdminError{Code: CodeUnavailable, Err: err}
	}
	if refusal != nil {
		return nil, a.refused(ctx, c, pack, version, refusal, digest, overridden, req.GetReason())
	}

	previous := uint64(0)
	if av, ok := a.o.Registry.Pointer(pack); ok {
		previous = av.GetVersion()
	}
	rollback := previous != 0 && version < previous
	direction := DirectionForward
	if rollback {
		direction = DirectionRollback
	}
	span.SetAttributes(attribute.String("direction", direction))
	pctx, pspan := a.tracer.Start(ctx, "content.write_pointer", trace.WithAttributes(attribute.String("direction", direction)))
	previous, err = a.o.Registry.MovePointer(pctx, pack, version, c.p.AccountID, activation)
	pspan.End()
	if err != nil {
		return nil, &AdminError{Code: CodeUnavailable, Err: err}
	}
	a.m.PointerMoves.WithLabelValues(direction, fmt.Sprint(overridden)).Inc()
	action := auth.ActionActivate
	switch {
	case overridden:
		action = auth.ActionOverride
	case rollback:
		action = auth.ActionRollback
	}
	a.record(ctx, c, action, auth.AuditOK, pack, version, auth.ContentAudit{BlobHashesSHA256: digest, Override: overridden, Reason: req.GetReason()}, "")
	if overridden {
		a.log.LogAttrs(ctx, slog.LevelWarn, "content activated by override", attrs(ctx, c, pack, version,
			slog.String("reason", req.GetReason()), slog.Uint64("previous_version", previous))...)
	} else {
		a.log.LogAttrs(ctx, slog.LevelInfo, "content activated", attrs(ctx, c, pack, version,
			slog.String("direction", direction), slog.Uint64("previous_version", previous))...)
	}
	return &adminv1.ActivateVersionResponse{PreviousVersion: previous, Rollback: rollback}, nil
}

func (a *Admin) refused(ctx context.Context, c caller, pack string, version uint64, r *Refusal, digest []byte, override bool, reason string) error {
	ca := auth.ContentAudit{BlobHashesSHA256: digest, Override: override, Reason: reason, FindingsCount: uint32(len(r.Findings))}
	if r.Reason == RefusalValidation {
		err := &AdminError{Code: CodeInvalidArgument, Reason: ErrReasonValidation,
			Detail: &adminv1.PublishFindings{Findings: Diagnostics(r.Findings, contentv1.Severity_ERROR)}, Err: r.Err}
		a.record(ctx, c, auth.ActionActivate, "refused", pack, version, ca, err.Error())
		a.log.LogAttrs(ctx, slog.LevelWarn, "content activation refused", attrs(ctx, c, pack, version,
			slog.String("reason", r.Reason), slog.Int("findings_count", len(r.Findings)))...)
		return err
	}
	a.m.ActivationsRefused.WithLabelValues(r.Reason).Inc()
	err := &AdminError{Code: CodeFailedPrecondition, Reason: r.Reason,
		Detail: &adminv1.ActivationRefusal{Reason: r.Reason, Subjects: r.Subjects}, Err: r.Err}
	a.record(ctx, c, auth.ActionActivate, "refused", pack, version, ca, err.Error())
	a.log.LogAttrs(ctx, slog.LevelWarn, "content activation refused", attrs(ctx, c, pack, version,
		slog.String("reason", r.Reason), slog.String("subjects", strings.Join(r.Subjects, ",")))...)
	return err
}

// --- reads -------------------------------------------------------------------

// ListVersions is every version of a pack, newest first, with its pointer and
// the pointer's history.
func (a *Admin) ListVersions(ctx context.Context, req *adminv1.ListVersionsRequest) (*adminv1.ListVersionsResponse, error) {
	pack := req.GetPackId()
	if _, err := a.authorize(ctx, pack, true); err != nil {
		return nil, err
	}
	out := &adminv1.ListVersionsResponse{
		Versions:    a.o.Registry.Versions(pack, int(req.GetLimit())),
		Activations: a.o.Registry.Activations(pack),
	}
	if av, ok := a.o.Registry.Pointer(pack); ok {
		out.ActiveVersion = av.GetVersion()
	}
	return out, nil
}

// GetVersion is one manifest.
func (a *Admin) GetVersion(ctx context.Context, req *adminv1.GetVersionRequest) (*adminv1.GetVersionResponse, error) {
	pack, version := req.GetPackId(), req.GetVersion()
	if _, err := a.authorize(ctx, pack, true); err != nil {
		return nil, err
	}
	cv, ok := a.o.Registry.Manifest(pack, version)
	if !ok {
		return nil, adminErr(CodeNotFound, ErrReasonNotFound, "%s@%d is not published", pack, version)
	}
	return &adminv1.GetVersionResponse{Version: cv}, nil
}

// GetBlob streams one blob of pack@version. Authorization is on the pack and
// manifest membership: a hash the store holds for another pack is NOT_FOUND
// here, so a Builder can't read what they don't hold by guessing its hash.
func (a *Admin) GetBlob(ctx context.Context, req *adminv1.GetBlobRequest, send func(*adminv1.GetBlobResponse) error) error {
	pack, version := req.GetPackId(), req.GetVersion()
	ctx, span := a.tracer.Start(ctx, "content.get_blob", trace.WithAttributes(attribute.String("pack_id", pack), attribute.Int64("version", int64(version))))
	defer span.End()
	if _, err := a.authorize(ctx, pack, true); err != nil {
		return err
	}
	cv, ok := a.o.Registry.Manifest(pack, version)
	if !ok {
		return adminErr(CodeNotFound, ErrReasonNotFound, "%s@%d is not published", pack, version)
	}
	i := slices.IndexFunc(cv.GetBlobs(), func(r *contentv1.BlobRef) bool { return bytes.Equal(r.GetHash(), req.GetHash()) })
	if i < 0 {
		return adminErr(CodeNotFound, ErrReasonNotFound, "%s@%d has no blob %x", pack, version, req.GetHash())
	}
	ref := cv.GetBlobs()[i]
	bodies, err := a.o.Blobs.Blobs(ctx, []*contentv1.BlobRef{ref})
	if err != nil {
		if IsStoreFault(err) {
			return &AdminError{Code: CodeUnavailable, Err: err}
		}
		return adminErr(CodeNotFound, ErrReasonNotFound, "%s@%d: %v", pack, version, err)
	}
	body := bodies[ref.GetPath()]
	for off := 0; off < len(body) || off == 0; off += BlobChunkBytes {
		end := min(off+BlobChunkBytes, len(body))
		if err := send(&adminv1.GetBlobResponse{Data: body[off:end]}); err != nil {
			return err
		}
		if end == len(body) {
			break
		}
	}
	span.SetAttributes(attribute.Int("bytes", len(body)))
	return nil
}

// ReloadContent reconciles the Loader with the store now, rather than at the
// next pointer move. Operator only.
func (a *Admin) ReloadContent(ctx context.Context, _ *adminv1.ReloadContentRequest) (*adminv1.ReloadContentResponse, error) {
	p, err := a.principal(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Has(auth.RoleOperator) {
		return nil, adminErr(CodePermissionDenied, ErrReasonOperatorOnly, "ReloadContent is for Operators")
	}
	if a.o.Reload == nil {
		return nil, &AdminError{Code: CodeUnavailable, Err: errors.New("content reload is not configured")}
	}
	versions, err := a.o.Reload(ctx)
	if err != nil {
		return nil, &AdminError{Code: CodeUnavailable, Err: err}
	}
	packs := make([]string, 0, len(versions))
	for p := range versions {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	out := &adminv1.ReloadContentResponse{}
	for _, p := range packs {
		out.Active = append(out.Active, &statev1.PackVersion{PackId: p, Version: versions[p]})
	}
	return out, nil
}
