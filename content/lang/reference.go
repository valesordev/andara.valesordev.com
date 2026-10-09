// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import "github.com/valesordev/andara/server/sim"

// Raisers of a diagnostic code (AW-CLI-009): the compiler alone, the loader
// (sim) alone, or both, decided by the code's string value.
const (
	RaisedByCompiler = "compiler"
	RaisedByLoader   = "loader"
	RaisedByBoth     = "both"
)

// Severities of a diagnostic code when nothing promotes it. --strict-orphans
// promotes orphan_room at load time; that is load policy, not the code's.
const (
	SeverityNameError   = "error"
	SeverityNameWarning = "warning"
)

// DiagnosticInfo is one row of the Builder's reference: a code, its default
// severity, and who raises it.
type DiagnosticInfo struct {
	Code     string
	Severity string
	RaisedBy string
}

// Diagnostics is every diagnostic code a Builder can meet, sorted by code:
// the sim.ErrCode values the loader raises and the Code* values the compiler
// raises, once each (AW-CLI-009). It is the one table, declared from the
// constants rather than copied from them, and admin/cli's reference test holds
// it complete against both packages' source: a code added to either without
// a row here fails `make check`.
var Diagnostics = []DiagnosticInfo{
	{string(sim.ErrChainMismatch), SeverityNameError, RaisedByLoader},
	{CodeChainTooDeep, SeverityNameError, RaisedByBoth},
	{CodeCoreVersionMismatch, SeverityNameError, RaisedByCompiler},
	{CodeDuplicateComponent, SeverityNameError, RaisedByBoth},
	{CodeDuplicateDecl, SeverityNameError, RaisedByCompiler},
	{CodeDuplicateDirection, SeverityNameError, RaisedByBoth},
	{CodeDuplicatePack, SeverityNameError, RaisedByCompiler},
	{CodeDuplicateRoom, SeverityNameError, RaisedByBoth},
	{CodeDuplicateTemplate, SeverityNameError, RaisedByBoth},
	{CodeDuplicateZone, SeverityNameError, RaisedByBoth},
	{CodeEncoding, SeverityNameError, RaisedByCompiler},
	{CodeExtendsCycle, SeverityNameError, RaisedByCompiler},
	{CodeFallbackMissing, SeverityNameError, RaisedByBoth},
	{CodeFloatLiteral, SeverityNameError, RaisedByCompiler},
	{CodeInvalidComponentField, SeverityNameError, RaisedByBoth},
	{CodeInvalidEscape, SeverityNameError, RaisedByCompiler},
	{string(sim.ErrInvalidProvenance), SeverityNameError, RaisedByLoader},
	{string(sim.ErrMalformed), SeverityNameError, RaisedByLoader},
	{CodeMissingReverseExit, SeverityNameWarning, RaisedByBoth},
	{string(sim.ErrEmptyContent), SeverityNameError, RaisedByLoader},
	{CodeOrphanRoom, SeverityNameWarning, RaisedByBoth},
	{CodePackMismatch, SeverityNameError, RaisedByBoth},
	{CodePackMissing, SeverityNameError, RaisedByCompiler},
	{CodeRemovedBySubtype, SeverityNameError, RaisedByCompiler},
	{string(sim.ErrSpawnRoomRemoved), SeverityNameError, RaisedByLoader},
	{CodeSyntax, SeverityNameError, RaisedByCompiler},
	{CodeTemplateHead, SeverityNameError, RaisedByCompiler},
	{string(sim.ErrUnflattenedTemplate), SeverityNameError, RaisedByLoader},
	{CodeUnknownBehavior, SeverityNameError, RaisedByCompiler},
	{CodeUnknownComponent, SeverityNameError, RaisedByBoth},
	{CodeUnknownDirection, SeverityNameError, RaisedByBoth},
	{CodeUnknownRoom, SeverityNameError, RaisedByBoth},
	{CodeUnknownSense, SeverityNameError, RaisedByCompiler},
	{CodeUnknownZone, SeverityNameError, RaisedByBoth},
	{CodeUnportableName, SeverityNameError, RaisedByCompiler},
	{CodeUnresolvedExtends, SeverityNameError, RaisedByBoth},
	{string(sim.ErrUnsupportedVersion), SeverityNameError, RaisedByLoader},
	{string(sim.ErrZoneRemoved), SeverityNameError, RaisedByLoader},
}
