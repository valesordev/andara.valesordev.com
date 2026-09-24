// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/valesordev/andara/server/sim"
)

// The reasons a version is refused, and the values of the `reason` label on
// andara_content_load_failures_total. Closed by construction, which is what
// makes the set safe as a metric label (CLAUDE.md §7).
const (
	ReasonFormatVersion  = "format_version"
	ReasonCoreVersion    = "core_version"
	ReasonValidation     = "validation"
	ReasonBlobMissing    = "blob_missing"
	ReasonFallbackRoom   = "fallback_missing"
	ReasonPackMismatch   = "pack_mismatch"
	ReasonManifestAbsent = "manifest_missing"
	// A blob record whose body does not hash to the key it is stored under.
	// Distinct from blob_missing: the store has a record and it is wrong,
	// which says something about the store rather than about the manifest.
	ReasonBlobCorrupt = "blob_corrupt"
	// The content store could not be read at all. Deliberately not one of the
	// content reasons: a broker restart or a leader move is not a Builder's
	// mistake, and counting it as `validation` would put infrastructure noise
	// into the number the content-freshness SLO is built on.
	ReasonStoreUnavailable = "store_unavailable"
	// A blob over content.max_blob_bytes.
	ReasonBlobTooLarge = "blob_too_large"
)

// A load rejection. Every one of these leaves the previously loaded version
// serving: content that does not load is a Builder's problem, and taking the
// World down for it would make one Builder's mistake everyone's outage
// (AW-SRV-012 AC-4 through AC-8). The only exception is a boot with nothing
// loadable, which Runtime turns into exit 1 — there is nothing to retain.
type (
	// ErrFormatVersion: a definition written by a newer compiler than this
	// binary understands. Have is what the blob declared.
	ErrFormatVersion struct {
		Path     string
		Have     uint32
		Min, Max uint32
	}
	// ErrCoreVersion: the pack was compiled against a core version that is not
	// the one running. Not fatal and not permanent — the pack is held and
	// re-evaluated when core's pointer moves (AC-8).
	ErrCoreVersion struct {
		Pack             string
		Compiled, Active uint64
	}
	// ErrBlobMissing: a manifest names a hash the blob topic does not carry.
	// Names both, because "which blob" is the only actionable part.
	ErrBlobMissing struct {
		Hash []byte
		Path string
	}
	// ErrValidation: sim.BuildWorld or sim.BuildTemplates refused it. The
	// findings are the loader's, unchanged (ADR-0004: one validator).
	ErrValidation struct {
		Findings []sim.ValidationError
	}
	// ErrPackMismatch: a Template blob whose name-pack is not the pack that
	// published it (AC-11).
	ErrPackMismatch struct {
		Blob                    string
		NamePack, PublishedPack string
	}
	// ErrManifestMissing: an Active Pointer naming a version whose manifest is
	// not on the versions topic. Typed rather than a bare error so it is not
	// counted as a validation failure.
	ErrManifestMissing struct {
		Pack    string
		Version uint64
		Topic   string
	}
	// ErrBlobCorrupt: a blob record whose body does not hash to its own key.
	// The whole store is content-addressed, so this is the one invariant a
	// reader can check for itself, and checking it is the only thing standing
	// between a damaged record and a World built from it.
	ErrBlobCorrupt struct {
		Path      string
		Want, Got []byte
	}
	// ErrBlobTooLarge: a blob over content.max_blob_bytes.
	ErrBlobTooLarge struct {
		Path  string
		Bytes int64
		Limit int64
	}
	// ErrCoreRollback: a core pointer moving backwards past a version some
	// retained pack was compiled against. The refusal is of the *core* move,
	// not of the pack — the pack is already serving and the Loader has no way
	// to unload it.
	ErrCoreRollback struct {
		From, To uint64
		Pack     string
		Pin      uint64
	}
	// ErrStoreUnavailable: the content store could not be read. Wraps the
	// underlying failure so a log line still names it.
	ErrStoreUnavailable struct {
		Op  string
		Err error
	}
)

func (e *ErrFormatVersion) Error() string {
	return fmt.Sprintf("%s declares format_version %d; this binary supports %d..%d",
		e.Path, e.Have, e.Min, e.Max)
}

func (e *ErrCoreVersion) Error() string {
	return fmt.Sprintf("%s was compiled against andara.core@%d; andara.core@%d is active",
		e.Pack, e.Compiled, e.Active)
}

func (e *ErrBlobMissing) Error() string {
	return fmt.Sprintf("blob %s for %s is not on %s", hex.EncodeToString(e.Hash), e.Path, TopicBlobs)
}

func (e *ErrValidation) Error() string {
	return fmt.Sprintf("content failed validation with %d findings", len(e.Findings))
}

func (e *ErrManifestMissing) Error() string {
	return fmt.Sprintf("no manifest for %s on %s", ManifestKey(e.Pack, e.Version), e.Topic)
}

func (e *ErrBlobCorrupt) Error() string {
	return fmt.Sprintf("blob for %s is stored under %s but its body hashes to %s",
		e.Path, hex.EncodeToString(e.Want), hex.EncodeToString(e.Got))
}

func (e *ErrBlobTooLarge) Error() string {
	return fmt.Sprintf("blob %s is %d bytes, over content.max_blob_bytes %d", e.Path, e.Bytes, e.Limit)
}

func (e *ErrCoreRollback) Error() string {
	return fmt.Sprintf("andara.core cannot roll back from %d to %d: %s@ is compiled against andara.core@%d and is serving",
		e.From, e.To, e.Pack, e.Pin)
}

func (e *ErrStoreUnavailable) Error() string {
	return fmt.Sprintf("content store unavailable during %s: %s", e.Op, e.Err.Error())
}

// Unwrap exposes the broker error to errors.Is and errors.As.
func (e *ErrStoreUnavailable) Unwrap() error { return e.Err }

func (e *ErrPackMismatch) Error() string {
	return fmt.Sprintf("%s names pack %q but was published in pack %q",
		e.Blob, e.NamePack, e.PublishedPack)
}

// Reason maps a rejection to its metric label.
//
// Every way content itself can be wrong has a type above. So anything that is
// not one of them is not the content's fault — it is the store failing to
// answer — and it falls through to store_unavailable rather than to
// validation. Getting this backwards would put every broker hiccup into
// andara_content_load_failures_total{reason="validation"}, which is the series
// the content-freshness SLO and the ContentLoadFailing alert are built on:
// operators would be paged about a Builder who did nothing wrong.
func Reason(err error) string {
	var (
		fv *ErrFormatVersion
		cv *ErrCoreVersion
		cr *ErrCoreRollback
		bm *ErrBlobMissing
		bc *ErrBlobCorrupt
		bl *ErrBlobTooLarge
		pm *ErrPackMismatch
		mm *ErrManifestMissing
		vl *ErrValidation
	)
	switch {
	case errors.As(err, &fv):
		return ReasonFormatVersion
	case errors.As(err, &cv), errors.As(err, &cr):
		return ReasonCoreVersion
	case errors.As(err, &bm):
		return ReasonBlobMissing
	case errors.As(err, &bc):
		return ReasonBlobCorrupt
	case errors.As(err, &bl):
		return ReasonBlobTooLarge
	case errors.As(err, &pm):
		return ReasonPackMismatch
	case errors.As(err, &mm):
		return ReasonManifestAbsent
	case errors.As(err, &vl):
		return ReasonValidation
	default:
		return ReasonStoreUnavailable
	}
}

// IsStoreFault reports whether a rejection was the store failing rather than
// the content being wrong. The two get different log lines, because telling a
// Builder their pack was refused when the broker was down wastes their day.
func IsStoreFault(err error) bool {
	return Reason(err) == ReasonStoreUnavailable
}

// Findings renders a rejection as validation findings, so a refused load logs
// through the same taxonomy a refused boot does (AW-SRV-001).
func Findings(err error) []sim.ValidationError {
	switch e := err.(type) {
	case *ErrValidation:
		return e.Findings
	case *ErrFormatVersion:
		return []sim.ValidationError{{File: e.Path, Code: sim.ErrUnsupportedVersion, Detail: e.Error()}}
	case *ErrPackMismatch:
		return []sim.ValidationError{{File: e.Blob, Code: sim.ErrPackMismatch, Detail: e.Error()}}
	case *ErrBlobMissing:
		return []sim.ValidationError{{File: e.Path, Code: sim.ErrMalformed, Detail: e.Error()}}
	case *ErrCoreVersion:
		return []sim.ValidationError{{Code: sim.ErrMalformed, Detail: e.Error()}}
	case *ErrCoreRollback:
		return []sim.ValidationError{{Code: sim.ErrMalformed, Detail: e.Error()}}
	case *ErrBlobCorrupt:
		return []sim.ValidationError{{File: e.Path, Code: sim.ErrMalformed, Detail: e.Error()}}
	case *ErrBlobTooLarge:
		return []sim.ValidationError{{File: e.Path, Code: sim.ErrMalformed, Detail: e.Error()}}
	case *ErrManifestMissing:
		return []sim.ValidationError{{Code: sim.ErrEmptyContent, Detail: e.Error()}}
	case *ErrStoreUnavailable:
		return []sim.ValidationError{{Code: sim.ErrMalformed, Detail: e.Error()}}
	case nil:
		return nil
	default:
		return []sim.ValidationError{{Code: sim.ErrMalformed, Detail: err.Error()}}
	}
}
