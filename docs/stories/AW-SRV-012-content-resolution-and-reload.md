---
id: AW-SRV-012
title: Content resolution from the store and reload at a tick boundary
epic: EPIC-05
component: server
type: feature
status: in-progress
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
   the Character is moved to `fallback_room`, `EntityRelocated{zone_id, entity_name, from_room_id,
   to_room_id, reason="room_removed"}` is emitted scoped to the fallback Room and the moved Entity
   *(amended 2026-09-24: a `Scope` holds one Room, and the removed Room's only audience is the
   Entities being relocated out of it)*, and `andara_content_relocations_total{zone}` increments.
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
   until `andara.core@4` is active — at which point it loads on the next pointer event, including
   core's own. *(Amended 2026-09-24, architecture: "or a `content reload` Admin call" is withdrawn.
   No such RPC exists, and none is needed; feedback §5.)*
9. **Given** a swap **when** its tick runs **then** `andara_content_reload_stall_seconds` records the
   in-tick cost and it stays under `sim.tick_budget_ms / 2`.
10. **Given** a fallback Room that the new version also removed **when** validation runs **then** it is a
    finding (`fallback_missing`) and the version is rejected — the one Room a Zone may not delete.
11. **Given** pack `town@9` whose manifest lists a blob at `templates/andara.core.Npc.json` **when**
    resolved **then** the version is rejected with `pack_mismatch` naming the blob, the name's pack
    (`andara.core`), and the publishing pack (`town`); no Template from that version is registered,
    and `andara.core`'s own `Npc` is untouched. *(Added 2026-09-18 from `AW-SRV-022`.)*

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
// CONTRACT SKETCH — as landed 2026-09-24 in docs/specs/protocol (the protos are normative)
// zone.proto ZoneDefinition
string fallback_room = 6;         // RoomID in this Zone; empty or absent is `fallback_missing`
// log.proto LoggedCommand oneof — 16 is unbind_character; 13/14 are AW-SRV-028's
ContentSwap content_swap = 17;    // pack_id = 1, version = 2, world_digest = 3
// event.proto EventEnvelope.payload oneof — 18 is Resync
EntityRelocated entity_relocated = 19;  // zone_id, entity_name, from_room_id, to_room_id, reason
```

**Amended 2026-09-24 (architecture, with implementation under way; also in the feedback file §3):**
- **Numbers.** The sketch's `content_swap = 16` collided with `unbind_character`, so it is 17.
  `EntityRelocated` is 19.
- **A swap is World-scoped.** A pack's Zones can sit on many Partitions, and a per-Zone swap would
  let one tick see two versions, which AC-2 forbids. The `LoggedCommand` carries an empty
  `zone_id` and is produced to Partition 0. Its Apply runs **after every other record of its
  tick**, so every Command of tick *T* sees the old version and every Command of *T+1* the new
  one. Two swaps in one tick apply in offset order. Relocation in every affected Zone happens in
  that one Apply, which ADR-0001's single process permits. The sharding story owns the
  cross-process form.
- **`EntityRelocated` follows the Event house style.** Flat `zone_id` and Room IDs (there is no
  `RoomRef`), a display name rather than an Entity ID (IDs do not reach clients), and a string
  `reason` (`room_removed`), as `Resync` and `SubscriberDropped` carry theirs.
- **`world_digest`** is SHA-256 over the built topology, as `server/sim` defines it. Document the
  function in `server/README.md`. A replay mismatch halts recovery, as a State Hash mismatch does.

`ContentSwap.world_digest` lets replay assert it rebuilt the same topology from the same version.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `content.source` | `ANDARA_CONTENT_SOURCE` | `kafka` | `dir` \| `kafka` (exists). *Corrected 2026-09-24: `kafka` is the default in `keys.yaml` and `server/config`, and stays so; each environment sets it explicitly (feedback §7).* |
| `content.packs` | `ANDARA_CONTENT_PACKS` | `andara.core` | packs to follow; `*` for all pointers |
| `content.cache_dir` | `ANDARA_CONTENT_CACHE_DIR` | `/var/cache/andara/blobs` | hash-keyed, immutable |
| `content.max_blob_bytes` | `ANDARA_CONTENT_MAX_BLOB_BYTES` | `8388608` | 8 MiB; larger is rejected at publish too |
| `content.reload_debounce` | `ANDARA_CONTENT_RELOAD_DEBOUNCE` | `2s` | coalesce a burst of pointer moves |

### Error taxonomy

`ErrFormatVersion{Have, Want}`, `ErrCoreVersion{Compiled, Active}`, `ErrBlobMissing{Hash, Path}`,
`ErrValidation{Findings}` (from `AW-SRV-001`), `ErrFallbackMissing{Zone, Room}`,
`ErrPackMismatch{Blob, NamePack, PublishedPack}` (AC-11; finding code `pack_mismatch`). All are load
rejections; none are boot failures once one version has loaded.

**Two rules, stated 2026-09-24 by architecture (feedback §5, §9b):**
- **A store fault is retried; a refusal is held.** A load that fails `store_unavailable` is
  retried with capped exponential backoff (1 s to 30 s) until it succeeds or the pointer moves
  again. Every other reason holds the version until the next pointer move. Without the retry, a
  broker blip during a load would leave the World stale until someone published again, and there
  is no reload command to recover it by hand.
- **A core rollback that would strand an active pack is refused.** `andara.core` stays at its
  current version, counted `reason="core_version"`, and the `error` line names every pack that
  holds it. The operator rolls those packs back first. There is no override: the loader has no
  way to unload a pack, and dropping packs out of the World silently is the worse surprise.
  `AW-SRV-013`'s activation path should refuse the same move at activation time, so an operator
  learns it before the pointer moves (feedback §9b, for PM). A boot with no loadable version exits
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
- `andara_content_load_phase_duration_seconds{phase}` — histogram (`resolve`, `validate`, `build`,
  `swap`). *(Renamed 2026-09-24, architecture: `AW-SRV-001` already publishes
  `andara_content_load_duration_seconds` with no labels, and both are kept; feedback §9.)*
- `andara_content_load_failures_total{reason}` — counter over the ten reasons the implementation
  draws: `format_version`, `core_version`, `validation`, `blob_missing`, `blob_corrupt`,
  `blob_too_large`, `fallback_missing`, `pack_mismatch`, `manifest_missing`, `store_unavailable`.
- `andara_content_pending_seconds{pack}` — gauge. Seconds since the pack's Active Pointer moved to
  a version that is neither serving nor refused for a Builder reason (`validation`,
  `fallback_missing`, `pack_mismatch`, `blob_too_large`); 0 otherwise. A newer move while one is
  pending keeps the older start. *(Added 2026-09-24, architecture: the SLI and alert of
  `docs/specs/slo/content-freshness.md`.)*
- `andara_content_reload_stall_seconds` — histogram, the in-tick swap cost.
- `andara_content_relocations_total{zone}` — counter.
- `andara_content_cache_hits_total{outcome}` — counter.

### Logs
- `info` per load: `pack`, `version`, `core_version`, `blobs`, `duration_ms`; `error` per rejection with
  findings; `warn` per relocation with `entity_id`, `from`, `to`.

### Traces
- `content.resolve` → `content.validate` → `content.build`; `content.swap` inside `sim.tick`.

### Alerts
- `ContentLoadFailing` on `max by (namespace, pack) (andara_content_pending_seconds) > 300` for 5 m,
  severity ticket. *(Amended 2026-09-24, architecture.)* The first sketch,
  `increase(andara_content_load_failures_total[15m]) > 0`, would ticket an operator for a Builder's
  typo. SLO `docs/specs/slo/content-freshness.md`, runbook `docs/runbooks/content-load-failing.md`,
  and the rule in `alerts.yaml` with promtool tests are all written by architecture and landed.
  They wait only on the gauge.

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

## As built (2026-09-24)

PR #63 (`7973192`, `60721ce`, `fae3dd2`) landed the half of this story that does not depend on the
protocol: the Kafka resolver, the hash-keyed blob cache, and the retained-version rule. AC-1, AC-4–8
and AC-11 are met at the `Loader`; AC-2, AC-3, AC-9 and AC-10 wait on `ContentSwap`, `EntityRelocated`
and `ZoneDefinition.fallback_room`. `Follow`/`Watch` are not yet started from `server/boot`, so a
running server does not react to a pointer move until AC-2 lands. The story stays `in-progress`
until then. Per-AC state, deviations and the architecture-owned items are in
`docs/feedback/AW-SRV-012-content-resolution-and-reload.md`.

## Open questions

- **Inherited from `AW-CLI-005` (2026-09-22):** the Content Language keyword for `fallback_room` is
  `fallback <room>`, one per Zone, and it is already in the grammar and the spec — but the field is
  not in `zone.proto`, so its two corpus cases wait in
  `docs/specs/content-language/v1/corpus/pending/fallback/` and `.../fallback-missing/`. **This story
  moves them** into `corpus/valid/` and `corpus/invalid/semantic/` when it lands field 6, and
  `make content-conformance` stops skipping them. The finding code is `fallback_missing`, matching
  this story's own AC-10. Worth knowing before starting: until the field exists, every Zone authored
  in the Content Language is one a server requiring `fallback_room` would refuse — which is why the
  keyword was specified now rather than deferred to a v2 of the language.

- **Inherited from `AW-SRV-022` (2026-09-18), contract-bearing:** (1) Templates arrive as one blob
  per declaration at `templates/<name>.json`, carrying their own `format_version` (field 8); there
  is no pack-level container. (2) `TemplateRef.Pack()` is derived from the name, and the dir loader
  has no pack context to hold it against — when Templates arrive from the broker, a blob whose
  name-pack differs from the pack it was published in **must be rejected**, or a Builder pack can
  publish `andara.core.Npc.json` and, depending on load order, collide with or stand in for core.
  AC-11 and `ErrPackMismatch` are that check.

- `[NEEDS BRIAN]` The player-facing wording for relocation. `EntityRelocated` carries the reason; the
  text is rendered by the client from it, so the words do not affect this contract.
- `[ASSUMPTION]` One fallback Room per Zone, declared in the Zone Definition, required. A World-level
  fallback would teleport players across the map.
- `[ASSUMPTION]` Whole-pack reload, debounced 2 s.

- **Handoff to architecture (raised 2026-09-24, implementation lane):** six items inside this story's
  scope are architecture-owned under CLAUDE.md §2 and implementation cannot land them. Written up in
  `docs/feedback/AW-SRV-012-content-resolution-and-reload.md`:
  1. **`docs/specs/slo/content-freshness.md`** — required by this story's Definition of Done, and
     `docs/specs/` is architecture's. The `ContentLoadFailing` alert in Observability is tied to it,
     and CLAUDE.md §7 requires the SLO doc *before* the alert.
  2. **`docs/runbooks/content-load-failing.md`** — also required by the Definition of Done. Runbooks
     are architecture's under the lane table.
  3. **Three protocol additions**, all under `docs/specs/protocol/`. `ZoneDefinition.fallback_room`
     at **6**, which is free. `LoggedCommand.content_swap` — the Interface Contract sketch says
     **16, but 16 is already `unbind_character`** and 13/14 are held for `AW-SRV-028`, so the next
     free number is **17**. `EntityRelocated` in `event.proto`'s payload oneof at **19**; the sketch
     gives no number and 18 is `Resync`. Until these land, **AC-2, AC-3, AC-9 and AC-10 cannot be
     implemented**: there is no Command to record the swap in the log, no Event to carry the
     relocation, and no field for `fallback_missing` to find missing.
  4. **The corpus move** this story's own first Open question assigns it —
     `docs/specs/content-language/v1/corpus/pending/fallback/` and `.../fallback-missing/` into
     `corpus/valid/` and `corpus/invalid/semantic/` — is under `docs/specs/content-language/`, which
     is architecture's as of 2026-09-24.
  5. **`content reload`.** AC-8 ends "…or a `content reload` Admin call". No such RPC exists in
     `andara/admin/v1/admin.proto` (it has `GetServerInfo` and the account, invite and agent RPCs).
     Either it is a new RPC for architecture to add, or AC-8 should rest on the pointer event alone.
  6. **The relocation wording** is still open — the decision bullet above, unchanged. It does not
     block this contract, but it blocks the client text once `EntityRelocated` exists. (Stated
     without the marker on purpose: `make status` scrapes the marker, and repeating it here would
     list one decision twice.)

  Implementation is landing the half that depends on none of them — the resolver, the blob cache and
  the retained-version rule, so AC-1 and AC-4 through AC-8 and AC-11 — on
  `impl/aw-srv-012-content-resolution`. The swap-and-relocate half follows once the protocol lands.
