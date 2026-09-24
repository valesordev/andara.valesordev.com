// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import "fmt"

// ErrCode classifies a validation finding. Additive-only; never remove or repurpose.
type ErrCode string

const (
	ErrUnknownRoom        ErrCode = "unknown_room"
	ErrUnknownZone        ErrCode = "unknown_zone"
	ErrDuplicateRoom      ErrCode = "duplicate_room"
	ErrDuplicateZone      ErrCode = "duplicate_zone"
	ErrUnsupportedVersion ErrCode = "unsupported_format_version"
	ErrMalformed          ErrCode = "malformed_file"
	ErrEmptyContent       ErrCode = "no_zones_found"
	ErrOrphanRoom         ErrCode = "orphan_room"

	// AW-SRV-021.
	ErrUnknownDirection      ErrCode = "unknown_direction"
	ErrUnknownComponent      ErrCode = "unknown_component_type"
	ErrDuplicateComponent    ErrCode = "duplicate_component_type"
	ErrInvalidComponentField ErrCode = "invalid_component_field"
	ErrMissingReverseExit    ErrCode = "missing_reverse_exit"

	// AW-SRV-022. Templates are flattened by the compiler; the loader checks
	// the result rather than resolving anything (ADR-0010 decision 9).
	ErrUnresolvedExtends   ErrCode = "unresolved_extends"
	ErrUnflattenedTemplate ErrCode = "unflattened_template"
	ErrDuplicateTemplate   ErrCode = "duplicate_template"
	ErrChainMismatch       ErrCode = "chain_mismatch"
	ErrChainTooDeep        ErrCode = "chain_too_deep"
	ErrInvalidProvenance   ErrCode = "invalid_provenance"

	// AW-SRV-012. A Template blob published under a pack whose name says it
	// belongs to another one. TemplateRef.Pack() is derived from the name, so
	// without this check a Builder pack publishing templates/andara.core.Npc.json
	// could stand in for core depending on load order (AW-SRV-012 AC-11). The
	// code is the one docs/specs/content-language/v1/errors.md already assigns.
	ErrPackMismatch ErrCode = "pack_mismatch"
)

// warningCodes are findings that do not refuse a load. They are advisory
// because the thing they describe is legal — a Room a Builder has not connected
// yet, a chute that only goes down — and refusing content for being unfinished
// would make the loader useless to the person using it.
//
// The set is closed by construction, which is what makes it safe as a metric
// label (CLAUDE.md §7) on andara_content_load_warnings_total.
var warningCodes = map[ErrCode]struct{}{
	ErrOrphanRoom:         {},
	ErrMissingReverseExit: {},
}

// IsWarning reports whether a finding is advisory rather than a refusal.
// Orphans are the one finding whose class is policy: --strict-orphans promotes
// them, and nothing else moves.
func IsWarning(e ValidationError, strictOrphans bool) bool {
	if e.Code == ErrOrphanRoom {
		return !strictOrphans
	}
	_, ok := warningCodes[e.Code]
	return ok
}

// ValidationError carries everything a Builder needs to fix the problem without
// opening the loader source.
type ValidationError struct {
	File     string
	Line     int // 0 when not line-scoped
	Zone     ZoneID
	Room     RoomID
	Template TemplateRef // set for Template findings (AW-SRV-022)
	Code     ErrCode
	Detail   string
}

func (e ValidationError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s: %s", e.File, e.Line, e.Code, e.Detail)
	}
	if e.File != "" {
		return fmt.Sprintf("%s: %s: %s", e.File, e.Code, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

// Fatal reports whether the finding refuses a load under default policy.
// Orphans are warnings unless the caller opted into Options.StrictOrphans, in
// which case they are already treated as fatal by BuildWorld (World is nil);
// use IsWarning when that policy is in hand.
func (e ValidationError) Fatal() bool {
	return !IsWarning(e, false)
}
