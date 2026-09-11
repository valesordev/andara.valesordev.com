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
)

// ValidationError carries everything a Builder needs to fix the problem without
// opening the loader source.
type ValidationError struct {
	File   string
	Line   int // 0 when not line-scoped
	Zone   ZoneID
	Room   RoomID
	Code   ErrCode
	Detail string
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

// Fatal reports whether the finding refuses a load. Orphans are warnings unless
// the caller opted into Options.StrictOrphans, in which case they are already
// treated as fatal by BuildWorld (World is nil).
func (e ValidationError) Fatal() bool {
	return e.Code != ErrOrphanRoom
}
