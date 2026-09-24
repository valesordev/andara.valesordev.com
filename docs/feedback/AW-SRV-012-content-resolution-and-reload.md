# AW-SRV-012 — content resolution and reload: handoff and deviations

Spec: `docs/stories/AW-SRV-012-content-resolution-and-reload.md`
Branch: `impl/aw-srv-012-content-resolution`
Raised: 2026-09-24, implementation lane.

This story's scope straddles the lane boundary. Six items in it are architecture-owned
under CLAUDE.md §2, and four Acceptance Criteria depend on three of them. This file is the
handoff; a pointer to it is in the story's Open questions.

Implementation is landing the half that depends on none of it: the resolver, the blob
cache and the retained-version rule — **AC-1, AC-4, AC-5, AC-6, AC-7, AC-8, AC-11**.

---

## 1. `docs/specs/slo/content-freshness.md` — architecture (blocks Definition of Done)

The story's Definition of Done requires it by name, and the Observability section ties the
`ContentLoadFailing` alert to it. CLAUDE.md §7 requires the SLO doc *before* the alert, so
the alert waits on it too. `docs/specs/` is architecture's and implementation may not write
there.

What the implementation gives it to measure, so the doc does not have to guess:

| Signal | Meaning for the SLO |
|---|---|
| `andara_content_active_version{pack}` | what is live now, per pack |
| `andara_content_load_duration_seconds{phase}` | `resolve`, `validate`, `build`, `swap` |
| `andara_content_load_failures_total{reason}` | `format_version`, `core_version`, `validation`, `blob_missing`, `fallback_missing`, `pack_mismatch` |
| `andara_content_cache_hits_total{outcome}` | `hit`, `miss` |

Freshness is the gap between an Active Pointer moving and the World serving that version.
The resolver debounces `content.reload_debounce` (2 s) before it starts, so any objective
under about 5 s is measuring the debounce rather than the system.

Note `pack_mismatch` is a **sixth** failure reason, not in the story's metric list; AC-11
was added on 2026-09-18 after the Observability section was written.

## 2. `docs/runbooks/content-load-failing.md` — architecture (blocks Definition of Done)

Also required by the Definition of Done. The lane table (CLAUDE.md line 47) puts runbooks
with architecture. The error taxonomy the runbook needs is implemented exactly as the story
specifies it: `ErrFormatVersion`, `ErrCoreVersion`, `ErrBlobMissing`, `ErrValidation`,
`ErrFallbackMissing`, `ErrPackMismatch`.

## 3. Three protocol additions — architecture (blocks AC-2, AC-3, AC-9, AC-10)

All under `docs/specs/protocol/`, which is architecture's.

| Where | Addition | Number | Note |
|---|---|---|---|
| `content/v1/zone.proto` | `string fallback_room` on `ZoneDefinition` | **6** | free, as the sketch says |
| `log/v1/log.proto` | `ContentSwap content_swap` on `LoggedCommand` | **17** | **the sketch says 16, which is taken** |
| `game/v1/event.proto` | `EntityRelocated` in the payload oneof | **19** | the sketch gives no number |

**The sketch's `content_swap = 16` collides.** `LoggedCommand` field 16 is
`unbind_character`, 15 is `bind_character`, and 13/14 are held for `AW-SRV-028`'s
`HandoffAck`/`HandoffRejected`. The next free number is 17, and log.proto's own comment says
verbs take numbers "from 17 upward". In `event.proto` the payload oneof runs to 18
(`Resync`), so `EntityRelocated` is 19.

Until these exist:

- **AC-2** — no `ContentSwap` Command, so there is nothing to put the swap in the log and
  nothing for replay to assert `world_digest` against.
- **AC-3** — no `EntityRelocated` Event and no `fallback_room` to relocate to.
- **AC-9** — `andara_content_reload_stall_seconds` measures the in-tick swap, which does not
  exist yet.
- **AC-10** — `fallback_missing` cannot fire on a field that is not in the message.

These four are the half of the story its own Context calls the interesting one. They are not
being skipped; they are waiting on the contract.

## 4. The corpus move — architecture

The story's first Open question assigns this story the job of moving
`docs/specs/content-language/v1/corpus/pending/fallback/` and `.../fallback-missing/` into
`corpus/valid/` and `corpus/invalid/semantic/` when field 6 lands. That tree is under
`docs/specs/content-language/`, which CLAUDE.md §2 states is architecture's as of 2026-09-24
— the same paragraph `AW-CLI-006` prompted. The move should travel with the proto change.

`content/lang` already parses `fallback <room>` and carries it through to the compiler, so
nothing on the implementation side blocks the move except the field.

## 5. `content reload` names an Admin RPC that does not exist — architecture

AC-8 ends: "…at which point it loads on the next pointer event or a `content reload` Admin
call." `andara/admin/v1/admin.proto` declares `GetServerInfo` and the account, invite, role
and agent RPCs; there is no `Reload`, and no service is declared in `content.proto` at all.

Implementation is building AC-8 on **the pointer-event path alone**, which satisfies the
acceptance criterion as written ("on the next pointer event *or*…"). A held pack is
re-evaluated whenever any pointer moves, including `andara.core`'s, so a Builder pack held
for core skew loads as soon as core catches up without a second publish. If the explicit
Admin call is wanted, it is a new RPC and architecture's to add.

## 6. `[NEEDS BRIAN]` relocation wording — still open

Carried forward unchanged. It does not block this contract; it blocks the client text once
`EntityRelocated` exists.

---

## 7. `content.source` default: the story's table disagrees with the deployed default

The story's Configuration table gives `content.source` a default of `dir`. Both
`server/config/config.go` (`DefaultContentSource = "kafka"`) and
`deploy/helm/andara/keys.yaml` (`default: kafka`, attributed to `AW-SRV-001`) say `kafka`;
`deploy/compose/docker-compose.yaml` sets `dir` explicitly.

Implementation has **not** changed the default — `keys.yaml` is architecture's and is the
authority. The note is only that the story's table is stale, and that flipping the default
is a live question now rather than a documentation nit: until this story, `kafka` meant a
refused boot with a finding naming `AW-SRV-012`, so the wrong default was harmless. After
it, the default boot reaches for a broker. Architecture should confirm `kafka` is intended.

## 8. Config keys added

Per the story's Configuration table, unchanged: `content.packs`
(`ANDARA_CONTENT_PACKS`, default `andara.core`, `*` for all), `content.cache_dir`
(`/var/cache/andara/blobs`), `content.max_blob_bytes` (8 MiB), `content.reload_debounce`
(2 s). These need matching entries in `deploy/helm/andara/keys.yaml`, which is
architecture's; implementation has added them to `server/config` only.
