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
| `andara_content_load_failures_total{reason}` | `format_version`, `core_version`, `validation`, `blob_missing`, `blob_corrupt`, `blob_too_large`, `fallback_missing`, `pack_mismatch`, `manifest_missing`, `store_unavailable` |
| `andara_content_cache_hits_total{outcome}` | `hit`, `miss` |

Freshness is the gap between an Active Pointer moving and the World serving that version.
The resolver debounces `content.reload_debounce` (2 s) before it starts, so any objective
under about 5 s is measuring the debounce rather than the system.

The story's metric list names five reasons. The implementation has **ten**, and the SLO
should be written against the distinction they draw rather than against the list:

- `pack_mismatch` — AC-11 was added on 2026-09-18, after the Observability section was written.
- `manifest_missing` — an Active Pointer naming a version the versions topic does not carry.
- `blob_corrupt` — a blob record whose body does not hash to the key it is stored under. The
  store is content-addressed, so this is the one invariant a reader can check for itself.
- `blob_too_large` — over `content.max_blob_bytes`, measured against the bytes actually read
  rather than against the `size_bytes` the publisher wrote into the manifest.
- `store_unavailable` — **the important one for the SLO.** A broker restart or a leader move is
  not a Builder's mistake. Everything content can get wrong has its own type, so anything else
  is the store failing to answer, and it is counted separately rather than as `validation`.
  Without that split, `ContentLoadFailing` would page an operator about a Builder who did
  nothing wrong, and a content-freshness objective would be measuring broker availability.
  The two also get different log lines, for the same reason.

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

## 8. Config keys added — nothing needed from architecture

`content.packs`, `content.cache_dir`, `content.max_blob_bytes` and
`content.reload_debounce` are implemented in `server/config` with the defaults the story's
Configuration table gives.

No handoff here, and worth recording why: architecture had **already groomed all four into
`deploy/helm/andara/keys.yaml`** (lines 134–137, attributed to `AW-SRV-012`), with defaults
identical to the story's table. They were among the keys `make values-schema` reports as
"groomed but not read by the server yet" and left out of the generated schema. Implementing
them moved them from pending into `values.schema.json` and `_env.tpl`, both of which are
generated and are regenerated in this branch.

That is the workflow working as intended, and it double-checks the implementation: the
defaults in `server/config` and the defaults in `keys.yaml` were written from the story
independently and agree. `content.max_blob_bytes` also declares `min: 1` in `keys.yaml`, so
configuration validation refuses zero rather than treating it as "no limit".

## 9. Two approved stories claim `andara_content_load_duration_seconds`

`AW-SRV-001`'s Observability section publishes it with **no labels**: "`andara_content_load_duration_seconds` — histogram. Labels: none. Cardinality: 1 series." `AW-SRV-012`'s asks for the same name **with a `phase` label** (`resolve`, `validate`, `build`, `swap`).

They cannot both be true of one metric. Adding a label to a published metric is not additive — every
query written against the unlabelled series has to change, and the Prometheus client refuses the
registration outright, which is how this was found: `make test` panicked with *"a previously
registered descriptor with the same fully-qualified name … has different label names"*.

Implementation has taken the reversible option and published the phased histogram as
**`andara_content_load_phase_duration_seconds{phase}`**, leaving `AW-SRV-001`'s series untouched.
`AW-SRV-012`'s metric list is therefore not satisfied verbatim, and this is the one place the
implementation knowingly differs from the story's Observability section.

Architecture's call, and it is a real one because the SLO in §1 will be written against whichever
name survives:

1. Keep both, as now — the unlabelled total from `AW-SRV-001`, the phased breakdown beside it. No
   query breaks; there are two metrics measuring overlapping things.
2. Retire the unlabelled one and relabel, amending `AW-SRV-001`. One metric, one breaking change,
   done deliberately and once.

Note the phase label is also incomplete until the protocol lands: `swap` is pre-seeded and never
observed, because the swap is AC-2 and AC-2 is blocked (§3).

## 9b. One rule the story does not state: a core rollback can strand a pack

The story gives the skew rule in one direction — a pack compiled against a core newer than the
one running is held (AC-8). The other direction is not in the story and arose in review.

If `andara.core@4` and a pack pinned to `andara.core@4` are both serving, moving the core
pointer back to 3 would leave the World serving a combination the loader would refuse to
assemble from scratch. The implementation refuses the **core move** and retains `core@4`,
naming the pack that holds it there, because the Loader has no way to unload a pack: every
path it has either accepts a version or retains the previous one.

That is a defensible reading of the retained-version rule, but it is a rule architecture did
not write, and it has an operational consequence worth being explicit about: **a Builder pack
can block a core rollback.** The alternative — accept the rollback and drop the incompatible
packs out of the World — trades one surprise for a worse one, but the choice is architecture's.
It may want an explicit override on the eventual `content reload` / activation path (§5).

## 10. What is in this branch, by acceptance criterion

| AC | State |
|---|---|
| AC-1 boot from Active Pointers | done — resolver, `Loader.LoadAll`, `andara_content_active_version{pack}` |
| AC-2 swap at a tick boundary | **blocked** on `ContentSwap` (§3) |
| AC-3 relocate to `fallback_room` | **blocked** on `fallback_room` and `EntityRelocated` (§3) |
| AC-4 `format_version` skew | done — refused, previous retained, both versions named |
| AC-5 validation failure | done — refused, previous retained, findings via `AW-SRV-001`'s taxonomy |
| AC-6 missing blob | done — refused naming hash and path |
| AC-7 cold and cached identical | done — verified against a real broker by deleting the blob topic between the two resolves |
| AC-8 core skew held, then released | done at the Loader — see the note below on process wiring; the `content reload` RPC does not exist (§5) |
| AC-9 `andara_content_reload_stall_seconds` | **blocked** — it measures the swap (§3) |
| AC-10 `fallback_missing` | **blocked** on `fallback_room` (§3) |
| AC-11 `pack_mismatch` | done — refused naming the blob, the name's pack and the publishing pack |

The `Loader` deliberately stops at "resolved, validated, and built": the swap itself is the seam the
`ContentSwap` Command will fill. Nothing here mutates a running World, which is why none of it needed
the tick loop.

**One thing to be precise about, because "done" could be read too generously.** `Content.Follow` and
`KafkaResolver.Watch` are implemented and tested — at the `Loader` level, and against a real broker —
but **nothing in `server/boot` starts them**. In a running server, an Active Pointer move is
therefore not yet observed. That is deliberate rather than an omission: `Loader.load` sets
`andara_content_active_version{pack}` when it accepts a version, and with no `ContentSwap` an
accepted version has nowhere to go, so a wired-up watch would report `town@8` while the World went
on serving `town@7`. A gauge that lies is worse than a feature that is visibly absent. The wiring is
one call in `Runtime`, and it belongs with AC-2 in the follow-up.

So for AC-4 through AC-8, read "done" as: the mechanism is complete, and every rule — rejection,
retention, holding, release, debounce, ordering — is exercised by a test. What is not yet true is
that a *running process* reacts to a pointer move.

---

## Architecture's answers — 2026-09-24

### §3 — landed: `fallback_room` = 6, `content_swap` = 17, `EntityRelocated` = 19

In `docs/specs/protocol/`, `gen/` regenerated, and `make proto-check` shows no breaking change.
Your numbers were right, and 16 was taken. The proto comments are normative. The story's sketch is
amended to match, and two things were decided that the sketch left open:

1. **A swap is World-scoped.** `LoggedCommand.zone_id` is empty and the record goes to Partition
   0. `ContentSwap`'s Apply runs **after every other record of its tick**, regardless of offset,
   and two swaps in one tick apply in offset order. That is what makes AC-2's "no tick observes a
   mix of 7 and 8" true when a pack's Zones sit on several Partitions. Relocation in every
   affected Zone happens inside that one Apply. The Loader produces through the same producer the
   Gateway uses, with the explicit partitioner, not the library default.
2. **`EntityRelocated` is `{zone_id, entity_name, from_room_id, to_room_id, reason}`**, scoped to the
   fallback Room plus the moved Entity. A `Scope` holds one Room. The removed Room's only
   occupants are the Entities being relocated, so no one is left out. `reason` is a string,
   `room_removed`. AC-3 is amended to say so.

`world_digest` is yours to define in `server/sim` (SHA-256 over the built topology in a canonical
order). Document it in `server/README.md`. A replay mismatch should halt recovery with a typed
error, as a State Hash mismatch does.

### §4 — the corpus move follows your compiler change, not the proto

The two cases can't leave `pending/` until `content/lang` sets field 6 and raises
`fallback_missing`. Before that, moving them fails `make content-conformance`. Their `PENDING`
notes now name the compiler change as the gate. When your PR that emits the field merges,
architecture moves `pending/fallback/` → `valid/fallback/` and `pending/fallback-missing/` →
`invalid/semantic/fallback-missing/` in the next `arch/` PR. Run `go run ./content/conformance`
against the cases locally before then. Say in the PR that they pass, and the move becomes a
rename. `testdata/content/` and `content/core` Zones need a `fallback` line before the loader
requires one. That is yours, and it rides with the same PR.

### §1 — `docs/specs/slo/content-freshness.md` written

It is written against your split, as you asked.

**What counts.** Builder reasons are excluded: `validation`, `fallback_missing`, `pack_mismatch`
and `blob_too_large`. Everything else counts, because it is the platform failing to serve
published content: `store_unavailable`, `manifest_missing`, `blob_missing`, `blob_corrupt`,
`format_version` and `core_version`.

**The SLI** is a gauge you now owe: `andara_content_pending_seconds{pack}`. It holds the seconds
since a pointer move that is neither serving nor refused for a Builder reason. A newer move while
one is pending keeps the older start. A good minute is ≤ 30 s. The target of 99.5 % over 28 d is
proposed and `[NEEDS BRIAN]`.

**The alert** changes from the story's `increase(failures[15m]) > 0` to
`max by (namespace, pack) (pending_seconds) > 300` for 5 m, as a ticket. The rule and its promtool
tests are in `alerts.yaml` now. They are inert until the gauge exists, and they need nothing else
from you.

### §2 — `docs/runbooks/content-load-failing.md` written

One diagnostic row per counted reason. Its Loki query matches both of your messages on their
shared suffix, "the previous version keeps serving". Keep that suffix if you reword either.

### §5 — no `content reload` RPC; AC-8 rests on the pointer event

Your reading is accepted and written into AC-8: a held pack is re-evaluated on every pointer move,
including core's. A reload RPC would be a privileged mutating call with no job to do, once one
rule is added: **`store_unavailable` is retried**, with capped exponential backoff from 1 s to 30 s,
until it succeeds or the pointer moves again. Without that rule, a broker blip during a load
leaves the World stale until someone publishes again, and there is nothing an operator could run
to recover it. That rule is now in the story's Error taxonomy, and it is owed with the second half.

### §7 — `content.source` default stays `kafka`

It is intended. The server cannot run without Kafka anyway, because the log is there. `dir` is a
developer and fixture convenience, and compose sets it explicitly. The story's table is
corrected. Every environment sets the key explicitly in its values file, so the default is what a
bare binary does, and a bare binary pointed at no broker should fail loudly, which it does.

### §9 — keep both metrics

Option 1, as you built it: `andara_content_load_duration_seconds` (AW-SRV-001, unlabelled, the
whole load) stays, and `andara_content_load_phase_duration_seconds{phase}` sits beside it. One says
how long a load took, and the other says where the time went. Relabelling a published series is a
breaking change for any query written against it, and it buys nothing here. The story's metric list is
amended. `swap` gets observed when AC-2 lands.

### §9b — accepted: a core rollback that strands a pack is refused

Your rule stands, with no override. The loader cannot unload a pack, and dropping packs out of the
World silently is the worse surprise. Count it as `reason="core_version"`, and have the `error`
line name every pack that holds core. The runbook tells the operator to roll those back first.

**For PM:** `AW-SRV-013`'s `ActivateVersion` should refuse the same move at activation time, so the
operator learns before the pointer moves rather than from a ticket. That is a line in `AW-SRV-013`'s
contract, groomed when it enters a sprint.

### What is owed by implementation from this file, all with the second half

1. `andara_content_pending_seconds{pack}`, as specified in the story's Observability section.
2. The `store_unavailable` retry with backoff.
3. `world_digest` defined in `server/sim` and documented in `server/README.md`.
4. The field-6 compiler change, with the `fallback` line in `testdata/content/` and `content/core`,
   after which architecture moves the two corpus cases.

---

## Implementation, 2026-09-25: two blockers before the second half can start

Picked up after #79 and #82 merged. Two things stop the second half as written. Each needs an
architecture decision, and neither is one the implementation lane should make.

### A. Architecture: which content does a replay start from? (blocks AC-2, AC-3, the DoD's replay-across-swap test)

**What happens today.** `boot.LoadContent` builds the World from the **current** Active Pointers.
`StartTickLoop` then replays the log from offset 0 over that World (`tickloop.Recover`). Nothing in
the log, the Tick Boundary Record, or the snapshot says which content version any tick ran at.

**Why that breaks once `ContentSwap` exists.** Take a live swap `town@7 → @8` at tick T that
relocates Aldric out of a removed Room. On the next restart:

1. Boot loads `town@8`, because that's where the pointer is.
2. Ticks 1..T-1 replay over `@8` topology, although they ran over `@7`. Any Command that touched
   the removed Room now behaves differently.
3. At T, `ContentSwap(town@8)` finds the engine already at `@8`. No Room is removed relative to
   itself, so Aldric isn't relocated, but the recorded State Hash at T has him relocated.
4. `ErrHashMismatch` halts recovery, and the server doesn't start.

So a correct live swap would turn the next restart into an outage. The same inexactness exists
today whenever a pointer moves while the server is down, but today it's rare. This story makes
pointer moves routine. "The swap Tick is in the log and replay is exact" (Scope, Data impact) holds
only if replay starts at the content the log's first tick ran at, and that isn't recorded
anywhere.

**Options.**

1. **The log is the source of content in effect** *(recommended)*. Every version a World serves
   enters through a `ContentSwap`, including the first. On an empty log, boot produces a genesis
   swap per followed pack before it serves anything. Recovery starts from an empty topology and
   builds content only from the swaps it replays. After recovery, any pointer that differs from
   the last recorded version is a move like any other, applied through the log. No proto change:
   `pack_id`, `version` and `world_digest` suffice. Consequences:
   - the Engine must start with no Zones and gain `ZoneState`s on a swap;
   - replay must resolve every historical version, so every manifest and blob must be retained
     for as long as the log is. ADR-0004's immutable, hash-keyed topics give that today, but it
     would become a stated retention contract;
   - `AW-SRV-007`'s restore from a snapshot at tick T needs the versions in effect at T. It can
     scan swaps up to T, or the snapshot manifest can carry them (your call, and a proto change if
     the latter).
2. **Boundaries carry versions.** `TickCompleted` (or the snapshot manifest) gains the per-pack
   versions in effect, and recovery resolves those before replaying. It's explicit and cheap to
   read, but it's a `log.proto` change, and every boundary record grows by the pack map.
3. **`ContentSwap` gains `from_version`,** and recovery derives the starting versions from the
   first swap per pack. It's the smallest proto change, but a pack never swapped still starts from
   the boot pointer. That leaves the "pointer moved while down" hole open unless boot also routes
   those moves through the log, and at that point it's option 1 plus a field.

**Also needed, whichever option is chosen: the scope of `world_digest`.** `log.proto` says "SHA-256
over the built topology the Loader produced for (pack_id, version)". That can be read as that
pack's Zones or as the whole World after the swap. Replay has to match the whole World: a
per-pack digest wouldn't catch a divergence in another pack's Zones. The implementation would
define it over the whole post-swap World (every Zone and Template, canonical order). Then the
second of two swaps in one tick digests a World that includes the first. That's consistent,
because one producer writes both to Partition 0 in Loader order. It also requires that the Loader
not mark a version serving until its `ContentSwap` is acknowledged, or a failed produce leaves the
Loader and the Engine disagreeing. Please confirm the whole-World reading.

### B. Architecture: `fallback_missing` at compile time turns 26 corpus cases red (blocks AC-10 and the §4 move)

`errors.md` §6 defines `fallback_missing` as "`fallback` names a Room the Zone does not declare,
**or a Zone declares none**". No case outside `pending/` has a `fallback` line. A compiler that
raises the code as specified fails every corpus case that declares a Zone:

| Corpus dir | Cases with a Zone | Without `fallback` |
|---|---|---|
| `valid/` | 9 | 9 (expected JSON also gains `fallbackRoom`) |
| `invalid/semantic/` | 15 | 15 (each sidecar gains a finding) |
| `roundtrip/` | 1 | 1 |
| `invalid/encoding/` | 1 | 1 |

The corpus is architecture's, so the compiler change can't land green from this lane alone.
Options:

1. **A stacked pair** *(recommended)*. Implementation's PR emits field 6, raises both forms, and
   makes `decompile` write the `fallback` line so `roundtrip/` stays an identity. Architecture's PR,
   based on it, adds `fallback` to every corpus Zone, updates the expected JSON, and moves the two
   `pending/` cases. They merge together. Implementation also updates `testdata/content/`: 17 Zone
   fixtures, which are this lane's.
2. **Split the rule.** The compiler raises `fallback_missing` only for a `fallback` naming an
   undeclared Room, and "declares none" is enforced at load (AC-10's server-side check). The corpus
   stays green, except that the two `pending/` cases move. It needs `errors.md` §6 amended, and a
   Builder then learns about a missing fallback at publish rather than at compile.

### What implementation can build without these, and is holding

The `store_unavailable` retry (§5's rule) is independent of both, and so is the compiler's field-6
change under option B1. The swap, relocation, `andara_content_pending_seconds` and the boot wiring
all depend on A. Building the swap before A would ship the restart outage described above.
`world_digest` waits on A's scope question. The story stays `in-progress`, and nothing is merged
under it until A is answered.

---

## Architecture's answers — 2026-09-25

### A. Option 1: the log is the source of the content in effect

Accepted as you proposed it, with the diagnosis exactly right. A correct live swap would have made
the next restart an outage. This isn't a new principle, which is why it needs no ADR: ADR-0002 §4
records tick boundaries rather than re-deriving them, and content in effect is the same kind of
decision. Written into the story's Data / state impact and `log.proto`'s `ContentSwap` comment.
The points you left open:

1. **Genesis order:** `andara.core` first, then the other followed packs by `pack_id`. A pack is
   compiled against a core (AC-8), so core must be in effect first.
2. **`AW-SRV-007`'s restore from a snapshot:** the snapshot carries it. `SnapshotEnvelope` gains
   `repeated PackVersion content = 8` and `bytes content_digest = 9`, landed in this PR, additive,
   with `gen/` regenerated. Scanning swaps up to *T* is the unbounded read a snapshot exists to
   avoid. This story's round writes the two fields, since it is where content in effect becomes
   known, and `AW-SRV-007` inherits reading and checking them.
3. **Retention** becomes a stated contract: everything a logged swap names is kept for the life of
   the log. ADR-0004's topics already do this, so it is a constraint on any future GC, not work now.
4. **An existing log:** forward-only. Refuse a non-empty log with no swap before its first
   boundary, exit `1`, naming this story. Don't build a legacy replay path. No World before M2
   needs to survive, and the path would outlive its use.
5. **`content.source=dir`:** genesis swaps carry version 0 and the digest. A directory changed
   while the server was down then halts recovery on the digest, rather than replaying silently
   over different content. That is the honest behaviour for a developer source.
6. **"Serving" means applied:** `andara_content_active_version` and the Loader's retained version
   move when the swap applies. Neither the Loader's acceptance nor the produce ack counts. This
   also answers your worry about a failed produce leaving the Loader and the Engine disagreeing:
   the Engine is the only authority, and the Loader follows it.

Options 2 and 3 are rejected. Option 2 puts a pack map in every boundary record, 10 times a second,
to answer a question that changes a few times a day. Option 3 leaves the "moved while down" hole,
as you said.

### `world_digest`: the whole World, confirmed

It covers the whole World's content topology after the swap: every pack's Zones and Templates, in
canonical order. It covers topology only, not Entity state (the State Hash has that). Your reading
of two swaps in one tick is right. `log.proto`'s comment now says so.

### B. Option 1: the stacked pair

Accepted. A missing `fallback` is a Builder's mistake, and a Builder should learn it at compile,
which is what `errors.md` §6 says. Splitting the rule to keep the corpus green would move a Builder
error from compile to publish for the corpus's convenience.

- **Your PR:** emits field 6, raises both forms of `fallback_missing`, makes `decompile` write the
  `fallback` line (so `roundtrip/` stays an identity), and updates `testdata/content/` and
  `content/core`.
- **Position.** `errors.md` had none for the "declares none" form, because there's no literal to
  point at. It is now the Zone's `zone` keyword, landed in this PR.
- **Architecture's PR:** based on your branch, it adds a `fallback` line to every corpus Zone,
  updates the expected JSON and sidecars, and moves the two `pending/` cases. Open yours, and
  architecture opens its PR against `main` from a branch based on yours. Merging it merges both, so
  neither lane commits to the other's branch, and `main` is never red between them.

**Order of work:** land the `store_unavailable` retry and the B1 compiler change whenever they're
ready. The swap, relocation, the pending gauge and boot wiring can start now, against the rules
above.
