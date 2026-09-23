// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package lang is the Andara Content Language: lexer, parser, resolver,
// merger, canonical emitter, formatter, and decompiler.
//
// It is one package with three entry points — Compile, Format, Decompile —
// because a formatter and a decompiler that share the compiler's AST are the
// only way the round-trip contract holds without three parsers drifting
// (AW-CLI-006 Context).
//
// It lives at the repository root rather than under server/ because three
// callers import it: admin/cli for the `content` commands, server/content for
// AW-SRV-013's publish gate, and the conformance harness. It depends on
// server/sim for the Component registry, the Direction set, and the ErrCode
// taxonomy — quoted rather than copied, so the compiler and the loader speak
// one vocabulary (errors.md §2, ADR-0004) — and on nothing else under server/.
package lang

import (
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// FormatVersion is the content format this compiler emits. It is emitted, not
// authored: a Builder has no way to claim a format version, which is why
// unsupported_format_version is a loader-only code (errors.md §3.5).
const FormatVersion = 1

// CorePack is the one pack a Builder pack may resolve `extends` against, and
// the one `requires` may name (semantics.md §4).
const CorePack = "andara.core"

// SourceMediaType is the media type of a published `.aw` source blob, and
// BlobMediaType that of a compiled one (semantics.md §6).
const (
	SourceMediaType = "text/x-andara"
	BlobMediaType   = "application/json"
)

// SourcePrefix is the path prefix a pack's own sources are published under, so
// they are grouped for `content fetch` and out of the way of a loader that
// globs `*.json` (semantics.md §6, pinned 2026-09-23).
const SourcePrefix = "src/"

// CoreRef names a pack and the version of it a compile resolved against.
type CoreRef struct {
	Pack    string
	Version uint32
}

// Pack is a compiled pack held for resolution: andara.core from the cache, or
// a dependency. Only its Templates are reachable from another pack — Zones are
// not, because a pack is the unit of publication and an Exit may not leave it
// (semantics.md §3).
type Pack struct {
	Name      string
	Version   uint32
	Templates []*contentv1.TemplateDefinition
}

// Blob is one file a ContentVersion manifests: the compiled Zones and
// Templates, and the sources published beside them.
type Blob struct {
	Path      string
	MediaType string
	Bytes     []byte
}

// Output is what a pack compiles to. Blobs are sorted by path and carry both
// the compiled output and the source, which is what lets `content fetch`
// return exactly what the Builder wrote, comments and all (ADR-0009).
type Output struct {
	Pack      string
	Zones     []*contentv1.ZoneDefinition
	Templates []*contentv1.TemplateDefinition
	Blobs     []Blob
	Requires  CoreRef
}
