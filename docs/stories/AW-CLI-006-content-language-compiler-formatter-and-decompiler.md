---
id: AW-CLI-006
title: Content Language compiler, formatter, and decompiler
epic: EPIC-05
component: cli
type: feature
status: review
size: M
depends_on: [AW-CLI-001, AW-CLI-005, AW-SRV-021, AW-SRV-022]
blocks: [AW-CLI-002, AW-CLI-003, AW-SRV-034]
lane: implementation
risk: high
---

## Context

`AW-CLI-005` specifies the Content Language; this story implements it as a Go package the CLI, CI, and
— for the three-way equivalence `AW-CLI-002` requires — the server's publish gate can all import. It is
one package with three entry points, because a formatter and a decompiler that share the compiler's AST
are the only way the round-trip contract holds without three parsers drifting.

## User story

As a builder, I want `andara-cli content compile` to tell me exactly which line is wrong, and `fmt` to
settle every layout argument, so that I spend my time on the world and not the format.

## Scope

### In scope
- `content/lang` (repo root, shared by `admin/cli` and `server/content`): lexer, parser, resolver (`extends` against a cached `andara.core` and pack
  dependencies), merger, emitter to canonical `andara.content.v1`, formatter, decompiler.
- `andara-cli content compile [--path DIR] [--out DIR]`, `content fmt [--check] [--path DIR]`,
  `content decompile --pack ID --version N [--out DIR]`.
- Core pack cache: `~/.cache/andara/packs/andara.core@N/`, fetched by `content fetch-core` over `Admin`
  and used offline thereafter.
- `make content-conformance`: runs the `AW-CLI-005` corpus against this compiler; part of `make check`.
- Error rendering per `AW-CLI-001`'s output contract, human and JSON.

### Out of scope
- The spec — `AW-CLI-005`. Publish/activate commands — `AW-CLI-003`. `validate`/`inspect` — `AW-CLI-002`.
- Any validation the sim already does: after emit, output goes through `sim.BuildWorld` unchanged.

## Acceptance criteria

1. **Given** the `AW-CLI-005` corpus **when** `make content-conformance` runs **then** every valid pair
   matches byte for byte, every invalid pair matches its `.errors` sidecar, and the round-trip pairs
   are identity. Exit non-zero on any mismatch naming the pair.
2. **Given** a source error **when** `content compile` runs **then** stderr has one line per error as
   `file:line:col: CODE message` with the declaration chain indented beneath, exit `1`; `--output json`
   emits the same as an array.
3. **Given** unformatted source **when** `content fmt` runs **then** the file is rewritten to the
   canonical form and a second run is a no-op; `--check` exits `1` listing files that would change.
4. **Given** a published version **when** `content decompile` runs **then** the produced `.aw` files
   compile to the same canonical blobs byte for byte and, if the version was published from canonical
   source, equal the published source blobs.
5. **Given** no network and a cached `andara.core@3` **when** `content compile` runs **then** it
   succeeds; **given** no cache **then** it fails with `core_version_mismatch` telling the Builder
   to run `content fetch-core`.
6. **Given** a pack declaring `requires andara.core@4` and a cache of `@3` **when** compiled **then**
   `core_version_mismatch` names both.
7. **Given** a 2,000-Room pack **when** compiled on the kind box **then** it completes in under 5 s.
8. **Given** the compiler package **when** imported by `server/content` for the publish gate **then**
   `depguard` permits it and it imports nothing from `server/` except `sim` types via `gen/`.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package lang   // content/lang; imported by admin/cli and server/content

type Diagnostic struct { File string; Line, Col int; Code string; Message string; Chain []string; Severity Severity }

// Compile parses every *.aw under dir, resolves against core and deps, and emits
// canonical blobs. Diagnostics are sorted by file, line, col. Pure; no network.
func Compile(dir string, core *Pack, deps []*Pack) (*Output, []Diagnostic)

type Output struct {
    Zones     []*contentv1.ZoneDefinition
    Templates []*contentv1.TemplateDefinition
    Blobs     []Blob            // path, media_type, bytes — compiled and source, sorted by path
    Requires  CoreRef           // pack, version
}

func Format(src []byte) ([]byte, []Diagnostic)
func Decompile(out *Output) (map[string][]byte, error)   // path → .aw source
```

### Commands

| Command | Flags | Exit |
|---------|-------|-----:|
| `content compile` | `--path DIR` (default `.`), `--out DIR` | `0` ok · `1` diagnostics · `2` usage/IO |
| `content fmt` | `--path`, `--check` | `0` · `1` would change · `2` |
| `content decompile` | `--pack`, `--version`, `--out` | `0` · `3` server unreachable (`AW-CLI-001` taxonomy) |
| `content fetch-core` | `--version N` (default active) | `0` · `3` |

### Configuration

`ANDARA_CONTENT_CACHE` (default `~/.cache/andara/packs`), via `AW-CLI-001`'s precedence.

## Data / state impact

Local cache only. Nothing here writes to the server.

## Observability requirements

Per `AW-CLI-001`: `cli.command` root span with `content.compile` child carrying `files`, `zones`,
`templates`, `diagnostics`; no metrics. CI publishes `content-conformance.json` per run.

## Test plan

- **Unit:** lexer and parser tables; merge semantics (field merge, list replace, shallow nesting) as a
  table; resolver cycle/depth; formatter idempotence (AC-3).
- **Integration:** `make content-conformance` (AC-1); decompile round-trip against a throwaway
  Redpanda (AC-4); offline compile (AC-5); the 2,000-Room fixture (AC-7).
- **Manual/operator:**
  ```
  andara-cli content fetch-core
  andara-cli content compile --path ./town       # expect: "3 zones, 41 rooms, 7 templates"
  andara-cli content fmt --check --path ./town   # expect: exit 0
  ```

## Definition of done

CLAUDE.md §8, plus: `make content-conformance` in `make check`; the package is imported by the server's
publish gate in `AW-SRV-013`'s equivalence test.

**Inherited from the `AW-CLI-005` review (2026-09-24):** `Conformance()` checks only `roundtrip/`
for AC-4 (compiled → source → compiled). Add a pass over every `valid/` case: decompile with the core
pack, recompile from a directory named like the case, and compare the blobs with
`TemplateDefinition.source` masked. `semantics.md` §8 now states that exception. At review a
throwaway program found 16 of 16 identical under that mask, and 7 of 16 differing without it. This
story is `review` until the pass is in `make check`, and so is `AW-CLI-005`, which closes on the same
evidence.


### §8 pass (2026-09-24, architecture) — stays `review`

This story had no §8 record. The only review so far was the one inside `AW-CLI-005`. Run against
`origin/main` `033f2c6`.

**Holds:**
- ACs 1, 2, 3, 6, 7 and 8 have tests in `content/lang` and `admin/cli`, untagged and run in
  `make test`. `make content-conformance` and `make content-grammar-check` are in `make check`.
- The `cli.command` → `content.compile{files, zones, templates, diagnostics}` span is asserted by
  `TestContentCompileEmitsTheSpan` with an in-process recorder. That satisfies §8's
  real-backend item for a CLI: `AW-CLI-001` decided the CLI exports nowhere, so there is no backend
  for its spans to reach, and the recorder is the most real observation available.
- Glossary: no new terms beyond `AW-CLI-005`'s. No migrations and no Helm keys.
- The parser `[ASSUMPTION]` is struck above as settled by the build.

**Owed by implementation:**
1. **The `valid/` decompile-and-recompile pass in `make check`.** This is the inherited line above,
   `SPRINT-01` implementation item 2. On `033f2c6`, `conformance.go` calls `diffRoundTrip` only for
   `roundtrip/`.
2. **`admin/README.md`.** It still says "`content` arrives with `AW-CLI-002`", and its Commands
   table lacks `content compile|fmt|decompile|fetch-core`. The flags that exist only in
   `admin/cli/testdata/help/content-*.txt` need rows: `--cache` and `ANDARA_CONTENT_CACHE`,
   `fetch-core --from`, and `decompile --path`.

**Passes to other stories (the no-caller rule):**
- "The package is imported by the server's publish gate": `AW-SRV-013` is the gate, and its
  Definition of done now names the import (added in this pass).
- AC-4 on a *published* version (`decompile --pack/--version`) and AC-5's cache filled over Admin
  (`fetch-core`) have no RPC to call. No story defines one (feedback §7). They are tested today
  against `--path` and `--from`. The RPC is sent to PM in the feedback file (§14). Whichever story
  gets it inherits these two ACs' published-version halves.

## Open questions

- **Inherited from `AW-SRV-022` (2026-09-18):** the compiler emits one blob per declaration at
  `templates/<name>.json` (the `BlobRef.path` convention the loader reads), `resolved: true`,
  `chain` root-first ending in self, Components and provenance sorted, and rejects a chain deeper
  than `sim.MaxChainDepth` (16). Its output for the seed must be byte-identical to
  `content/core/templates/`, held today by `TestCoreSeedMatchesFixture`.

- **Inherited from `AW-CLI-005` (2026-09-22), when the spec landed:** diagnostics use `sim.ErrCode`
  strings, not a parallel `E_*` set — `errors.md` §2, and the rename above is part of it. `Diagnostic`
  gains `Severity`, because `orphan_room` and `missing_reverse_exit` are warnings that must not fail a
  compile. Expected output is **canonical JSON**, not `.pb`: `formatVersion` first, then field-number
  order, two-space indent, LF, sorted `repeated` fields (`semantics.md` §7) — and Go's `protojson`
  injects non-deterministic whitespace, so emitting those bytes means re-serializing through a
  deterministic encoder rather than trusting `protojson.MarshalOptions{Indent: "  "}`. Exits sort
  lexicographically by direction string, matching `TestBuildWorld_ExitsSortedByDirection`.
  `make content-conformance` skips `corpus/pending/`, printing the count and each case's gating story;
  `corpus/invalid/encoding/` is checked before the grammar is reached. `make content-grammar-check`
  already exists and is in `make check`.

- **Resolved 2026-09-24 (§8, as built in `content/lang/parser.go`):** Hand-written recursive-descent parser rather than a generated one; the grammar is
  small and the error messages are the product. The spec's grammar is checked with lark's Earley
  parser and a dynamic lexer, because the language has no reserved words — a hand-written parser is
  contextual by construction, so this is a property of the checking tool, not a constraint on the
  compiler.
