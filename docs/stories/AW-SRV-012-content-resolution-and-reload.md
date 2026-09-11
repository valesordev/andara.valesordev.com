---
id: AW-SRV-012
title: Content resolution from the store and reload at a tick boundary
epic: EPIC-05
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-001, AW-SRV-021, AW-SRV-022]
blocks: [AW-SRV-013]
lane: implementation
risk: high
---

## Context

ADR-0004 put content in the data layer: immutable blobs keyed by hash, immutable version manifests keyed
by `packID@version`, and one mutable Active Pointer per pack. This story is the server's read side —
resolving the active version into `ZoneDefinition` values, handing them to `AW-SRV-001`'s validator, and
swapping the World when the pointer moves.

The interesting problem is not the load. It is what happens to Entities standing in a Room that the new
version deleted. ADR-0004 decided: relocate to the Zone's fallback Room with an Event, because refusing
the load would let one Builder's mistake block every other Builder. `AW-SRV-021` and `AW-SRV-022` are new
dependencies: Components and Templates (ADR-0010) arrive in content, and the resolver has to load the
flattened Templates the compiler publishes into `AW-SRV-022`'s registry.

## User story

As a builder, I want my published Zone to go live without a deploy, so that world-building is not gated
on an engineering release.

## Scope

### In scope
- `content.source=kafka` (`AW-SRV-001` left the seam): read every Active Pointer, resolve each manifest,
  fetch blobs by hash, decode `ZoneDefinition` and `TemplateDefinition` blobs, and build the World.
- Watching `andara.content.active.v1`; on a pointer move, resolve, validate, build the new immutable
  topology off-tick, and swap at the next boundary via a `ContentSwap` Command so the swap Tick is in
  the log and replay is exact.
- Relocation of Entities in removed Rooms to `ZoneDefinition.fallback_room`, as `EntityRelocated`
  Events inside the same Apply.
- Rejection with the previous version retained: `format_version` skew, validation findings, missing
  blob, core-version skew (`ContentVersion.core_version` newer than the running `andara.core`).
- Blob cache on disk keyed by hash, since blobs are immutable.
- `andara_build_info` gains `content_version` per pack.

### Out of scope
- Publishing — `AW-SRV-013`. Authoring — `AW-CLI-003`/`AW-CLI-005`.
- Validation logic — `AW-SRV-001`/`AW-SRV-021`, reused unchanged.
- Per-Zone reload. Whole-pack reload only; per-Zone is an optimization for when pack size demands it.

## Acceptance criteria

1. **Given** Active Pointers for `andara.core@3` and `pack.town@7` **when** the server starts with
   `content.source=kafka` **then** it resolves both manifests, fetches every blob, validates, and serves
   them; `andara_content_active_version{pack}` reports `3` and `7`.
2. **Given** a running World **when** `pack.town` moves to `8` **then** a `ContentSwap{pack, version}`
   Command is produced, the swap applies inside one tick, and no tick observes a mix of `7` and `8`.
3. **Given** a Character in a Room that `8` removes **when** the swap applies **then** in the same tick
   the Character is moved to `fallback_room`, `EntityRelocated{entity, from, to, reason=ROOM_REMOVED}`
   is emitted with both Rooms' scope, and `andara_content_relocations_total{zone}` increments.
4. **Given** `format_version` newer than the binary supports **when** the pointer moves **then** the
   load is rejected, `7` continues to be served, an `error` line names both versions, and
   `andara_content_load_failures_total{reason="format_version"}` increments. The World stays up.
5. **Given** a version that fails referential or component validation **when** the pointer moves **then**
   the same holds, with findings logged per `AW-SRV-001`'s `ValidationError` taxonomy.
6. **Given** a manifest referencing a blob absent from `andara.content.blobs.v1` **when** resolution
   runs **then** it fails naming the hash and path, and the previous version is retained.
7. **Given** the same version resolved twice, cold and from cache **when** the topologies are compared
   **then** they are byte-identical (`AW-SRV-001` AC-10).
8. **Given** `pack.town@8` compiled against `andara.core@4` while `andara.core@3` is active **when** the
   pointer moves **then** it is rejected with `reason="core_version"` naming both, and stays rejected
   until `andara.core@4` is active — at which point it loads on the next pointer event or a `content
   reload` Admin call.
9. **Given** a swap **when** its tick runs **then** `andara_content_reload_stall_seconds` records the
   in-tick cost and it stays under `sim.tick_budget_ms / 2`.
10. **Given** a fallback Room that the new version also removed **when** validation runs **then** it is a
    finding (`fallback_missing`) and the version is rejected — the one Room a Zone may not delete.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package content   // server/content, outside sim

type Resolver interface {
    // Resolve reads the Active Pointer for pack, its manifest, and every blob, and returns
    // decoded definitions. Pure with respect to (pack, version); cached by blob hash.
    Resolve(ctx context.Context, pack string, version uint64) (Resolved, error)
    Watch(ctx context.Context) (<-chan PointerMove, error)     // from andara.content.active.v1
}
type Resolved struct {
    Pack, Version   string, uint64
    CoreVersion     uint64
    Zones           []*contentv1.ZoneDefinition
    Templates       []*contentv1.TemplateDefinition   // flattened by the compiler (ADR-0010 §9)
    Source          []BlobRef                          // the language source, retained, never loaded
}
// Loader wires Resolver → sim.BuildWorld → Engine.Swap and owns the retained-version rule.
func (l *Loader) Apply(r Resolved) error   // validates, builds, produces ContentSwap; never blocks the tick
```

```protobuf
// CONTRACT SKETCH — additions
// zone.proto ZoneDefinition: next free number
string fallback_room = 6;         // RoomID in this Zone; required; validated present
// log.proto LoggedCommand oneof
ContentSwap content_swap = 16;    // pack_id, version, world_digest (sha256 of the built topology)
// event.proto payload oneof
EntityRelocated { string entity_id = 1; RoomRef from = 2; RoomRef to = 3; RelocateReason reason = 4; }
```

`ContentSwap.world_digest` lets replay assert it rebuilt the same topology from the same version.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `content.source` | `ANDARA_CONTENT_SOURCE` | `dir` | `dir` \| `kafka` (exists) |
| `content.packs` | `ANDARA_CONTENT_PACKS` | `andara.core` | packs to follow; `*` for all pointers |
| `content.cache_dir` | `ANDARA_CONTENT_CACHE_DIR` | `/var/cache/andara/blobs` | hash-keyed, immutable |
| `content.max_blob_bytes` | `ANDARA_CONTENT_MAX_BLOB_BYTES` | `8388608` | 8 MiB; larger is rejected at publish too |
| `content.reload_debounce` | `ANDARA_CONTENT_RELOAD_DEBOUNCE` | `2s` | coalesce a burst of pointer moves |

### Error taxonomy

`ErrFormatVersion{Have, Want}`, `ErrCoreVersion{Compiled, Active}`, `ErrBlobMissing{Hash, Path}`,
`ErrValidation{Findings}` (from `AW-SRV-001`), `ErrFallbackMissing{Zone, Room}`. All are load
rejections; none are boot failures once one version has loaded. A boot with no loadable version exits
`1` naming the reason — there is nothing to retain.

## Data / state impact

Content version is part of what identifies a running World: `andara_build_info` and
`Admin.GetServerInfo` carry it per pack. `ContentSwap` in the log means the World's history records
which content it was running at every tick, which is what makes a replay across a content change exact.

Relocation is a World mutation caused by a Builder — it happens inside `Apply(ContentSwap)`, in the log,
never out of band.

## Observability requirements

### Metrics
- `andara_content_active_version{pack}` — gauge; pack count is bounded by content.
- `andara_content_load_duration_seconds{phase}` — histogram (`resolve`, `validate`, `build`, `swap`).
- `andara_content_load_failures_total{reason}` — counter (`format_version`, `core_version`, `validation`,
  `blob_missing`, `fallback_missing`).
- `andara_content_reload_stall_seconds` — histogram, the in-tick swap cost.
- `andara_content_relocations_total{zone}` — counter.
- `andara_content_cache_hits_total{outcome}` — counter.

### Logs
- `info` per load: `pack`, `version`, `core_version`, `blobs`, `duration_ms`; `error` per rejection with
  findings; `warn` per relocation with `entity_id`, `from`, `to`.

### Traces
- `content.resolve` → `content.validate` → `content.build`; `content.swap` inside `sim.tick`.

### Alerts
- `ContentLoadFailing` on `increase(andara_content_load_failures_total[15m]) > 0` for 15 m: the World is
  not showing what Builders published. Tied to an EPIC-05 content-freshness SLO defined in
  `docs/specs/slo/content-freshness.md`, written in this story; runbook
  `docs/runbooks/content-load-failing.md` ships here.

## Test plan

- **Unit:** resolver against fixture topics (missing blob, wrong hash, skew); retained-version rule.
- **Integration:** against a throwaway Redpanda — boot from pointers (AC-1); pointer move with a removed
  Room asserting relocation in the same tick (AC-2, AC-3); each rejection reason (AC-4–6, 8, 10);
  cache determinism (AC-7); replay across a swap asserting `world_digest` and identical State Hash.
- **Manual/operator:**
  ```
  make up && andara-server
  andara-cli content activate pack.town --version 8       # via AW-CLI-003
  andara-cli server info                                  # expect: pack.town@8 within 2 s
  # in play: standing in a removed Room → "You find yourself in <fallback>"
  ```

## Definition of done

CLAUDE.md §8, plus: the replay-across-swap test; `docs/specs/slo/content-freshness.md` and the runbook.

## Open questions

- `[NEEDS BRIAN]` The player-facing wording for relocation. `EntityRelocated` carries the reason; the
  text is rendered by the client from it, so the words do not affect this contract.
- `[ASSUMPTION]` One fallback Room per Zone, declared in the Zone Definition, required. A World-level
  fallback would teleport players across the map.
- `[ASSUMPTION]` Whole-pack reload, debounced 2 s.
