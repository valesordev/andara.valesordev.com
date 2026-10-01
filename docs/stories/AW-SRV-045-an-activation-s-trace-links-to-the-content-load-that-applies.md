---
id: AW-SRV-045
title: An activation's trace links to the content load that applies it
epic: EPIC-05
component: server
type: feature
status: draft
size: S
depends_on: [AW-SRV-013, AW-INF-021]
blocks: []
lane: implementation
risk: low
---

## Context

On `dev`, an `andara-cli content activate` is one trace: `cli.command` → `ActivateVersion`. The
Loader's `content.load` → `content.swap` that applies the move is a separate root trace (SRE, from
`AW-INF-021`'s rollout). The Loader starts `content.load` in its own context when it sees the
pointer move, and `content.v1.ActiveVersion` has no field to carry the activator's context. So
today the two traces join only on `pack@version` and time.

Architecture ruled on 2026-10-01 (option 1, `docs/feedback/AW-INF-021-dev-content-store.md`, "The
activation and the swap, ruled"). `ActiveVersion` carries the activation's W3C traceparent, and
`content.load` adds a span link to it for every pointer move it coalesces. It's a link and not a
parent, because one debounced load can serve several moves, and a parent would silently pick one.
`AW-INF-021`'s Observability line and `AW-CLI-003`'s inherited line already read "two traces joined
by a link". This story makes that true. It isn't on the M3 demo path.

## User story

As an operator, I want to follow a content activation from the CLI to the tick that applied it, so
that "I activated it, why isn't it live?" is one trace hop rather than a search by time.

## Scope

### In scope
- `ActivateVersion` writes `trace_parent` on the `ActiveVersion` record it produces.
- The pointer watch carries each record's `trace_parent` through to `Follow`.
- `Follow`'s debounce keeps, per pack, the `trace_parent` of every move it coalesces, not only the
  newest. Today `pending` keeps only the newest version per pack, so a superseded move's context
  would be lost.
- Each per-pack `content.load` (`loadOnce`) adds one span link per move of *that pack* coalesced into
  it, provided the move's `trace_parent` is non-empty. A burst across several packs still produces
  one load per pack, as today, each linking its own pack's moves. No batch-level span is added.
  *(Revised after review of #316: the first draft described one load linking moves across packs,
  which the Loader doesn't do.)*

### Out of scope
- Parenting `content.load` when exactly one move was coalesced. That's option 2, which architecture
  didn't choose.
- `AW-SRV-042`'s leaving line. It already takes its `trace_id` from the swap record, which is the
  load's trace (ruling 4, corrected).

## Acceptance criteria

1. **Given** `andara-cli content activate` **when** `ActivateVersion` produces the pointer record
   **then** its `trace_parent` is the W3C traceparent of the `ActivateVersion` server span.
2. **Given** the Loader applying that one move **then** its `content.load` span has exactly one link,
   whose trace ID and span ID equal AC-1's `trace_parent`.
3. **Given** three moves of one pack inside one debounce window (v1 → v2 → v3) **then** that pack's
   single `content.load` (for v3) has three links, one per move, and no parent from any of them.
   **Given** moves of two packs in one window **then** there are two `content.load` spans, as today,
   each linking only its own pack's moves.
4. **Given** the boot's core activation **then** `trace_parent` is empty, and its load has no link
   for it.
5. **Given** a pointer record written before this story (field absent) **then** the load adds no link
   for it, and nothing errors.
6. **Given** a malformed `trace_parent` **then** the load adds no link for it and logs one `warn`
   with `pack`, `version` and `trace_parent`. The load proceeds.
7. **Given** `dev` with a trace backend **when** an activation is made **then** Tempo shows the
   `content.load` trace with a link to the `ActivateVersion` trace. This closes `AW-INF-021`'s and
   `AW-CLI-003`'s one-link observation.
8. **Given** a retry of a refused load (the existing backoff) **then** the retry's `content.load`
   carries the same links as the attempt it retries.

## Interface contract

- **Protocol:** `content.v1.ActiveVersion` gains `string trace_parent = 5;`. That's the W3C
  `traceparent` of the `ActivateVersion` call that moved the pointer, and it's empty for the boot's
  core activation. It's additive: an old record reads empty, meaning no link. Architecture pins it
  in `content.proto` at contract review, and the implementing PR regenerates `gen/`.
- **Span:** `content.load` gets links with attributes `content.pack` and `content.version` per
  link. Its parent is unchanged.
- The State Hash doesn't change, because the pointer record isn't World state.

## Data / state impact

One additive field on a compacted pointer topic. Old records read empty. Rolling back the binary
leaves the field unread.

## Observability requirements

- **Metrics:** none new.
- **Logs:** the `warn` in AC-6. Nothing else.
- **Traces:** `content.load` links (AC-2, AC-3). This story exists for that.
- **Alerts:** none.

## Test plan

- **Unit:** writing the field (AC-1); one link and three links (AC-2, AC-3); the empty, absent and
  malformed field (AC-4 to AC-6). Each is asserted with an in-memory span exporter.
- **Integration:** against the local stack, activate, then assert the load span's link through the
  local Tempo (`docs/specs/testing/live-assertions.md`: poll to a deadline).
- **Manual/operator:** AC-7 on `dev`.

## Definition of done

CLAUDE.md §8.

## Open questions

None. The decision is architecture's ruling of 2026-10-01.
