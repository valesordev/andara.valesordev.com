// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/auth"
)

// ServerPrincipal is the reserved principal the boot publishes and activates
// core as. It is not an Account and is never authenticated; Account IDs are
// 32 hex characters, so none can collide with it (AW-SRV-013).
const ServerPrincipal = "server"

// ErrCoreDigest: the store holds andara.core@N with bytes other than this
// build's (AC-16). Two builds disagree about what core N is, and the server
// overwrites neither.
type ErrCoreDigest struct {
	Pack          string
	Version       uint64
	Stored, Built []byte
}

func (e *ErrCoreDigest) Error() string {
	return fmt.Sprintf("the store holds %s@%d with digest %x, and this build embeds %x: two builds disagree about core %d; content/core/VERSIONS is append-only",
		e.Pack, e.Version, e.Stored, e.Built, e.Version)
}

// CoreBootOptions is the server's own core, and where it goes.
type CoreBootOptions struct {
	Registry *Registry
	Auditor  *auth.Auditor
	Metrics  *PublishMetrics
	Log      *slog.Logger
	Tracer   trace.Tracer
	// Pack is content.core_pack.
	Pack string
	// Version is content/core/VERSION, and Blobs the embedded pack.
	Version uint64
	Blobs   map[string][]byte
	// Build is the build version, for the audit records' reason.
	Build string
}

// CoreBoot is what the boot did with core.
type CoreBoot struct {
	Published bool
	// Active is core's active version afterwards, and ActiveBy who moved its
	// pointer there.
	Active    uint64
	ActiveBy  string
	Activated bool
}

// BootCore publishes the build's core and activates it, under the five rules
// of AW-SRV-013's *andara.core at boot*:
//  1. the store holds core@N with the build's digest: publish nothing;
//  2. it holds core@N with another digest: ErrCoreDigest, and write nothing;
//  3. it lacks core@N: write the blobs and the manifest, author server;
//  4. activate N if nothing is active, or if the active M < N was moved there
//     by the server; leave an Account's pointer, and never move backwards;
//  5. readiness waiting for core in effect is the caller's.
func BootCore(ctx context.Context, o CoreBootOptions) (CoreBoot, error) {
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Metrics == nil {
		o.Metrics = NewPublishMetrics(nil)
	}
	ctx, span := o.Tracer.Start(ctx, "content.core_boot", trace.WithNewRoot(),
		trace.WithAttributes(attribute.String("pack_id", o.Pack), attribute.Int64("version", int64(o.Version))))
	defer span.End()

	server := auth.Principal{AccountID: ServerPrincipal}
	reason := "boot " + o.Build
	refs := make([]*contentv1.BlobRef, 0, len(o.Blobs))
	for p, b := range o.Blobs {
		sum := sha256.Sum256(b)
		refs = append(refs, &contentv1.BlobRef{Path: p, Hash: sum[:], SizeBytes: uint64(len(b))})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].GetPath() < refs[j].GetPath() })
	digest := BlobHashesDigest(refs)
	audit := func(action string, version uint64) {
		actx, aspan := o.Tracer.Start(ctx, "audit.write")
		defer aspan.End()
		o.Auditor.Record(actx, auth.Entry{
			Actor: server, Action: action, Target: ManifestKey(o.Pack, version), Outcome: auth.AuditOK,
			Content: &auth.ContentAudit{PackID: o.Pack, Version: version, BlobHashesSHA256: digest, Reason: reason},
		})
	}

	var out CoreBoot
	if existing, ok := o.Registry.Manifest(o.Pack, o.Version); ok {
		if stored := BlobHashesDigest(existing.GetBlobs()); !bytes.Equal(stored, digest) {
			err := &ErrCoreDigest{Pack: o.Pack, Version: o.Version, Stored: stored, Built: digest}
			o.Log.LogAttrs(ctx, slog.LevelError, "content core: "+err.Error(),
				slog.String("stored_digest", fmt.Sprintf("%x", stored)), slog.String("built_digest", fmt.Sprintf("%x", digest)))
			return out, err
		}
	} else {
		for _, ref := range refs {
			wctx, wspan := o.Tracer.Start(ctx, "content.write_blob")
			_, err := o.Registry.PutBlob(wctx, ref.GetHash(), "application/json", o.Blobs[ref.GetPath()])
			wspan.End()
			if err != nil {
				return out, err
			}
		}
		mctx, mspan := o.Tracer.Start(ctx, "content.write_manifest")
		_, err := o.Registry.PublishExact(mctx, &contentv1.ContentVersion{PackId: o.Pack, Blobs: refs, Author: ServerPrincipal, Publisher: ServerPrincipal}, o.Version)
		mspan.End()
		if err != nil {
			return out, err
		}
		audit(auth.ActionPublish, o.Version)
		out.Published = true
	}

	av, has := o.Registry.Pointer(o.Pack)
	move := !has || (av.GetVersion() < o.Version && av.GetActivatedBy() == ServerPrincipal)
	if move {
		pctx, pspan := o.Tracer.Start(ctx, "content.write_pointer", trace.WithAttributes(attribute.String("direction", DirectionForward)))
		_, err := o.Registry.MovePointer(pctx, o.Pack, o.Version, ServerPrincipal, "")
		pspan.End()
		if err != nil {
			return out, err
		}
		o.Metrics.PointerMoves.WithLabelValues(DirectionForward, "false").Inc()
		audit(auth.ActionActivate, o.Version)
		out.Activated, out.Active, out.ActiveBy = true, o.Version, ServerPrincipal
	} else {
		out.Active, out.ActiveBy = av.GetVersion(), av.GetActivatedBy()
	}

	state := "present"
	if out.Published {
		state = "published"
	}
	msg := fmt.Sprintf("content core: %s@%d %s; ", o.Pack, o.Version, state)
	level := slog.LevelInfo
	switch {
	case out.Activated:
		msg += "activated"
	default:
		msg += fmt.Sprintf("active %s@%d by %s", o.Pack, out.Active, out.ActiveBy)
		// An Account moved core's pointer below this build's: that's an
		// Operator's decision, left alone and said out loud (AC-17).
		if out.Active < o.Version {
			level = slog.LevelWarn
		}
	}
	o.Log.LogAttrs(ctx, level, msg,
		slog.String("pack_id", o.Pack), slog.Uint64("version", o.Version),
		slog.Uint64("active_version", out.Active), slog.String("activated_by", out.ActiveBy))
	return out, nil
}
