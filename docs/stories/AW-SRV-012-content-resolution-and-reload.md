---
id: AW-SRV-012
title: Content resolution from the store and reload at a tick boundary
epic: EPIC-05
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-001]
blocks: [AW-SRV-013]
assignee: cursor
risk: high
---

> `status: draft` — unblocked by ADR-0004 and scoped. Groomed to `ready` when M3 approaches.

## Context

ADR-0004 put content in the data layer: immutable blobs keyed by hash, immutable version manifests keyed
by `packID@version`, and one mutable Active Pointer per pack. This story is the server's read side —
resolving the active version into `ZoneDefinition` values, handing them to `AW-SRV-001`'s validator, and
swapping the World when the pointer moves.

The interesting problem is not the load. It is what happens to Entities standing in a Room that the new
version deleted.

## User story

As a builder, I want my published Zone to go live without a deploy, so that world-building is not gated
on an engineering release.

## Scope

### In scope
- Reading the Active Pointer, resolving the version manifest, fetching blobs by hash, and building
  `ZoneDefinition` values.
- Watching `andara.content.active.v1` for pointer moves and reloading.
- Reload at a tick boundary: build the new immutable topology, then swap.
- Relocation policy for Entities in a removed Room — ADR-0004 decided they are relocated to a designated
  Zone fallback Room with an Event, because refusing the load would let one Builder's mistake block every
  other Builder.
- `format_version` skew: reject the load, **keep serving the last version that loaded**, and say so
  loudly. Never fail to boot because a Builder published something newer than this binary understands.
- Blob caching, since blobs are content-addressed and therefore immutable.

### Out of scope
- Publishing — `AW-SRV-013`.
- The Builder-facing authoring surface — `AW-CLI-003`.
- Validation logic itself — `AW-SRV-001`, reused unchanged.

## Acceptance criteria (known now; completed at grooming)

1. **Given** an Active Pointer naming version `N` **when** the server starts **then** it resolves the
   manifest, fetches the blobs, validates, and serves version `N`.
2. **Given** a running World **when** the Active Pointer moves to version `N+1` **then** the new topology
   is built and swapped at a tick boundary, with no partially-applied state visible at any tick.
3. **Given** a Character in a Room that version `N+1` removes **when** the reload completes **then** the
   Character is relocated to the Zone's fallback Room and an Event is emitted so everyone present sees
   it happen.
4. **Given** a version whose `format_version` exceeds what this binary supports **when** the pointer moves
   to it **then** the load is rejected, the previous version continues to be served, an `error` line names
   both versions, and `andara_content_load_failures_total` increments. The World stays up.
5. **Given** a version that fails referential validation **when** the pointer moves to it **then** the
   same holds: rejected, previous version retained, findings logged per `AW-SRV-001`'s error taxonomy.
6. **Given** a blob referenced by a manifest but absent from the blob topic **when** resolution runs
   **then** it fails with the hash named, and the previous version is retained.
7. **Given** the same version resolved twice **when** the topologies are compared **then** they are
   byte-identical, per `AW-SRV-001` AC-10.

## Interface contract

To be written at grooming. Committed now: this story produces `[]ZoneDefinition` and calls
`sim.BuildWorld`. It adds no validation of its own — a rule that exists here and not in the CLI or at
publish is a defect.

## Data / state impact

Content version is now part of what identifies a running World. `andara_build_info` carries
`content_version` alongside `version` and `commit` (`AW-INF-002`), because ADR-0004 decoupled content
from code and reproducing a bug now requires naming both.

The relocation policy in AC-3 mutates World state as a side effect of a content change, which is the one
place where a Builder action directly moves a player. It is therefore a Command in the log like any other
mutation, not an out-of-band edit.

## Observability requirements

- **Metrics:** `andara_content_active_version` (gauge, label `pack`), `andara_content_load_duration_seconds`
  (histogram), `andara_content_load_failures_total` (counter, label `reason`),
  `andara_content_reload_stall_seconds` (histogram — the tick cost of a swap),
  `andara_content_relocations_total` (counter, label `zone`).
- **Logs:** `info` on every load with pack, version, blob count, and duration; `error` on rejection with
  the full validation findings; `warn` per relocation with Character and both Rooms.
- **Traces:** `content.resolve`, `content.validate`, `content.swap`.
- **Alerts:** `ContentLoadFailing` — symptom: the world is not showing what Builders published. Tied to
  an SLO, runbook ships in this story.

## Test plan

Reload with a removed Room asserting relocation; `format_version` skew asserting the previous version is
retained and the World stays up; a missing blob asserting the same; determinism across two resolutions of
one version.

## Definition of done

CLAUDE.md §8, plus: the "keep serving the last good version" behavior is tested, since it is the property
that stops a Builder's mistake from being an outage.

## Open questions

- `[NEEDS BRIAN]` The player-facing story for relocation. ADR-0004 flags this: "you are shunted
  somewhere" versus something with in-world justification is a design call, and players will see it.
- `[ASSUMPTION]` One fallback Room per Zone, declared in the Zone Definition. A world-level fallback would
  be simpler and worse — it would teleport players across the map.
- `[ASSUMPTION]` Reload is whole-pack, not per-Zone. Per-Zone reload is a possible optimization once pack
  size makes it matter.
