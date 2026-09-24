// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"encoding/hex"
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

func (e *ErrPackMismatch) Error() string {
	return fmt.Sprintf("%s names pack %q but was published in pack %q",
		e.Blob, e.NamePack, e.PublishedPack)
}

// Reason maps a rejection to its metric label. An error that is not one of the
// taxonomy is "validation": it came from the validator or from decoding
// something the validator would have refused.
func Reason(err error) string {
	switch e := err.(type) {
	case *ErrFormatVersion:
		return ReasonFormatVersion
	case *ErrCoreVersion:
		return ReasonCoreVersion
	case *ErrBlobMissing:
		return ReasonBlobMissing
	case *ErrPackMismatch:
		return ReasonPackMismatch
	case *ErrValidation:
		_ = e
		return ReasonValidation
	default:
		return ReasonValidation
	}
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
	case nil:
		return nil
	default:
		return []sim.ValidationError{{Code: sim.ErrMalformed, Detail: err.Error()}}
	}
}
