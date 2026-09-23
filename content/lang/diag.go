// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"sort"
	"strings"

	"github.com/valesordev/andara/server/sim"
)

// Severity separates a finding that refuses a compile from one that does not.
// Warnings exit 0 (errors.md rule 7): orphan_room and missing_reverse_exit are
// legal content that is more often a mistake than an intention, so they are
// never silent and never fatal.
type Severity int

// The two severities. SeverityError is the zero value so a finding built
// without stating one refuses, which is the safe direction to be wrong in.
const (
	SeverityError Severity = iota
	SeverityWarning
)

// String renders the severity as the JSON envelope spells it.
func (s Severity) String() string {
	if s == SeverityWarning {
		return "warning"
	}
	return "error"
}

// Diagnostic is one finding. The shape is errors.md §1, and the codes are
// sim.ErrCode strings rather than a parallel E_* set, so the three-way
// equivalence AW-CLI-002 AC-4 requires compares values rather than a mapping
// table (errors.md §2).
type Diagnostic struct {
	File     string   // as the Builder can open it: --path joined with the path inside the pack
	Line     int      // 1-based
	Col      int      // 1-based, in runes
	Code     string   // errors.md §3
	Message  string   // one sentence, no period, names the offending value
	Chain    []string // the declaration chain, outermost first; empty when not chain-scoped
	Severity Severity
}

// Codes the compiler raises that the loader has no occasion to raise, because
// they are failures that exist in source and not in compiled output
// (errors.md §3.1). The rest of the taxonomy is sim.ErrCode, quoted below
// rather than redeclared.
const (
	CodeSyntax              = "syntax_error"
	CodeEncoding            = "encoding"
	CodeInvalidEscape       = "invalid_escape"
	CodeFloatLiteral        = "float_literal"
	CodePackMissing         = "pack_missing"
	CodeDuplicatePack       = "duplicate_pack"
	CodePackMismatch        = "pack_mismatch"
	CodeCoreVersionMismatch = "core_version_mismatch"
	CodeTemplateHead        = "template_head"
	CodeExtendsCycle        = "extends_cycle"
	CodeRemovedBySubtype    = "removed_by_subtype"
	CodeDuplicateDecl       = "duplicate_declaration"
	CodeDuplicateDirection  = "duplicate_direction"

	// Pending a protobuf field (semantics.md §9). Specified, in the grammar,
	// and reachable only once the field lands.
	CodeFallbackMissing = "fallback_missing"
	CodeUnknownSense    = "unknown_sense"
	CodeUnknownBehavior = "unknown_behavior"
)

// Codes the loader defines and the compiler raises early, so a Builder hears
// about them with a file:line:col instead of at boot. Quoted from sim rather
// than copied: errors.md §2 requires that these are the same strings, and a
// literal here would let them drift silently.
var (
	CodeUnknownRoom           = string(sim.ErrUnknownRoom)
	CodeUnknownZone           = string(sim.ErrUnknownZone)
	CodeDuplicateRoom         = string(sim.ErrDuplicateRoom)
	CodeDuplicateZone         = string(sim.ErrDuplicateZone)
	CodeUnknownDirection      = string(sim.ErrUnknownDirection)
	CodeUnknownComponent      = string(sim.ErrUnknownComponent)
	CodeDuplicateComponent    = string(sim.ErrDuplicateComponent)
	CodeInvalidComponentField = string(sim.ErrInvalidComponentField)
	CodeUnresolvedExtends     = string(sim.ErrUnresolvedExtends)
	CodeDuplicateTemplate     = string(sim.ErrDuplicateTemplate)
	CodeChainTooDeep          = string(sim.ErrChainTooDeep)
	CodeOrphanRoom            = string(sim.ErrOrphanRoom)
	CodeMissingReverseExit    = string(sim.ErrMissingReverseExit)
)

// String renders a finding the way `content compile` prints it, without the
// chain: `file:line:col: CODE message` (AC-2).
func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Line, d.Col, d.Code, d.Message)
}

// Render writes one finding as AC-2 specifies it: the finding on one line, the
// declaration chain indented two spaces beneath.
func (d Diagnostic) Render(w *strings.Builder) {
	w.WriteString(d.String())
	w.WriteByte('\n')
	for _, c := range d.Chain {
		w.WriteString("  ")
		w.WriteString(c)
		w.WriteByte('\n')
	}
}

// sortDiagnostics orders findings by file, line, column, then code
// (errors.md rule 5). Two compiles of the same source produce the same
// diagnostics in the same order, which is what makes the .errors sidecars a
// byte comparison rather than a set comparison.
func sortDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		switch {
		case a.File != b.File:
			return a.File < b.File
		case a.Line != b.Line:
			return a.Line < b.Line
		case a.Col != b.Col:
			return a.Col < b.Col
		default:
			return a.Code < b.Code
		}
	})
}

// HasError reports whether any finding refuses the compile. Warnings do not
// (errors.md rule 7).
func HasError(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}
