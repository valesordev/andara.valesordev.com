---
id: AW-SRV-042
title: An empty content store waits, unready — and andara-cli --tls-server-name
epic: EPIC-05
component: server
type: feature
status: done
size: M
depends_on: [AW-SRV-012, AW-SRV-013, AW-CLI-001]
blocks: [AW-INF-021, AW-CLI-011]
lane: implementation
risk: medium
---

## Context

`dev` can't be seeded. With `content.source=kafka`, a server whose store and World log hold no
Zones exits `1` with "no content in effect" (`AW-SRV-012`) before it serves Admin. Admin is the only
way to publish the first content, so nothing can be published. SRE found the deadlock while building
`AW-INF-021` (#277). Every new environment starts in that state once, and so does `AW-INF-021` AC-7's
rebuild from nothing.

Architecture ruled on 2026-09-30 (#289; `docs/feedback/AW-INF-021-dev-content-store.md`, "Architecture's
ruling: an empty store waits, unready"). A store-backed World that has never had content is a normal
first state, not a configuration error. In that state the server waits, unready, serving Admin and
refusing play, until the first Zones arrive. The seed reaches the pod over a port-forward. So the CLI
also needs to verify TLS against a name other than the one it dials. SRE builds the chart and
`content-seed` halves in `AW-INF-021` (#288). This story is the server and CLI halves, and it's on
SPRINT-03's critical path, ahead of `AW-INF-021`'s merge.

## User story

As an operator, I want a fresh environment's server to come up and wait for its first content, so
that I can seed it over Admin instead of being locked out by a server that exits before it serves.

## Scope

### In scope
- **Server (`server/boot`):**
  - the waiting-for-content state, and leaving it on the first swap that brings Zones;
  - `/startedz`, and `/readyz` reporting 503 while the server waits;
  - Game's `no_content_in_effect` refusal;
  - the state's two log lines.
- **CLI (`admin/cli`):** `server.tls_server_name`, set by `--tls-server-name`, `ANDARA_TLS_SERVER_NAME`,
  or config, and listed by `config show`.
- **Docs:** `server/README.md`'s health table and config keys, and `admin/README.md`'s config table.

### Out of scope
- The chart's `startupProbe` move to `/startedz`, `make content-seed`, and the
  `AndaraServerUnavailable` runbook line. Those are SRE's, in `AW-INF-021` (#288).
- Any change to `content.source=dir`, or to a World that has had content. Both keep today's exit
  (ACs 3 and 4).
- A new metric. `andara_content_zones_loaded` is already 0 while the server waits. SRE decides in
  `AW-INF-021` whether it wants an explicit gauge.

## Acceptance criteria

1. **Given** `content.source=kafka`, an empty store and an empty World log **when** the server boots
   **then** it stays up, and all of the following hold:
   - `/startedz` returns 200 and `/readyz` returns 503;
   - `Game.OpenSession` fails `UNAVAILABLE`, with `ErrorInfo` domain `andara.game` and reason
     `no_content_in_effect`;
   - `Admin.ListVersions` succeeds;
   - the `warn` line `waiting for content: …` appears exactly once.
2. **Given** that waiting server **when** a Zone-bearing pack is published, approved and activated over
   Admin **then** `/readyz` returns 200 with no restart, the `info` line `content in effect: leaving the
   wait` appears with `zones` and `pack@version`, and `OpenSession` succeeds.
3. **Given** a World log in which a Content Swap with `zone_count > 0` has *applied*, whose store now
   refuses every version **when** the server boots **then** it exits `1` with "no content in effect",
   unchanged (`AW-SRV-012`). **Given** a log whose only swap with `zone_count > 0` was *refused* by a
   record-decidable reason (`stale_base` or `misrouted`) **then** the server waits, live and after a
   restart, with the classification made from the records alone (no store read).
4. **Given** `content.source=dir` with no Zones **when** the server boots **then** it exits `1`,
   unchanged (`AW-SRV-001` AC-9).
5. **Given** a port-forward to a pod whose certificate names `andara-0.andara.<ns>.svc` **when**
   `andara-cli --tls-server-name andara-0.andara.<ns>.svc` dials it through `localhost` **then** the TLS
   handshake verifies against `server.tls_ca` and the call succeeds. **Given** the same dial without the
   flag **then** verification fails, and the CLI exits `3` (server unreachable, `AW-CLI-001`).
6. **Given** a waiting server **when** it restarts **then** it waits again, with no exit loop. **Given** a
   server restarted after its first swap with Zones **then** it recovers Ready.
7. **Given** a waiting server draining on SIGTERM **then** `/readyz` reports drain as today, and
   `/startedz` stays 200 until exit.
8. **Given** a waiting server **when** a Zone-bearing version whose World lacks `character.spawn_room`
   is activated **then** `ActivateVersion` fails `FAILED_PRECONDITION`, reason `spawn_room_removed`,
   naming the Room, the pointer doesn't move, and the server still waits. **Given** a version that
   holds the spawn Room **then** the wait ends (AC-2).
9. **Given** any swap the Loader produces **then** its `zone_count` equals the number of Zones in the
   World after it (0 for `andara.core`'s Templates-only swap), and replay of the log reproduces it.

## Interface contract

**When the server waits.** With `content.source=kafka`, the server waits for content when two things
hold: no Content Swap with `zone_count > 0` has *applied* in the recovered World log, and no followed
pack's Active Pointer loads Zones. A refused swap (a deterministic no-op) doesn't count. Every other
no-content case keeps today's exit `1`.

**What's decidable without the content, and why it's enough** *(added 2026-10-01 on Codex's P1 on
#295)*. The Engine refuses a swap for one of four reasons (`server/sim/content.go`, `prepareSwaps`).
Two are decided from the record alone: `misrouted` (partition and `zone_id`) and `stale_base`
(`base_digest` against the digest in effect). Two are decided only after `Content.Prepare` rebuilds
the topology: `zone_removed` and `fallback_missing`. For a World that has never had Zones, which is
the only case the wait rule needs to classify, those two can't refuse a well-formed swap:
- `zone_removed` needs a Zone in effect to remove. Against a World with no Zones it never fires. If
  it has fired, an earlier swap with Zones applied, and the World has had content.
- `fallback_missing` is the Engine's backstop. The Loader refuses the same finding before it produces
  a swap: `ActivateVersion` runs the Loader's evaluation (`AW-SRV-013` AC-14), and so does
  `FollowContent`. A produced swap refused for it means the Loader and the Engine disagree. That's a
  defect, not a state to wait in.

So the server classifies by the record alone, walking the swaps in log order. The digest in effect
starts empty. A swap whose record passes routing and whose `base_digest` matches counts as applied,
and the digest in effect becomes its `world_digest`. Otherwise it's refused, and the digest in effect
is unchanged. The World has had Zones if any swap counted as applied has `zone_count > 0`. **The
guarantee is exact for every log a correct Loader can write.** In the defect case, a
`fallback_missing` refusal of a produced first swap, the classification says "had content", and the
server exits `1`. That's a loud failure for a defect, which is the outcome wanted, not a silent
wait.

**How the server knows a swap had Zones** (decided 2026-10-01, Open questions item 1, option (a)).
`ContentSwap` gains `uint32 zone_count = 5` (`log.proto`, pinned in this review):

```
// CONTRACT SKETCH — not an implementation (the field is pinned in log.proto)
message ContentSwap {
  ...
  uint32 zone_count = 5;   // Zones in the whole World after this swap; the Loader sets it
}
```

It has the same scope as `world_digest`, the whole World and not this pack. Zones are never removed
(`zone_removed`), so across applied swaps it never decreases. A swap written before the field reads 0.

**While it waits:**
- Recovery has finished, `andara.core` is in effect (`AW-SRV-013` AC-15), and the gRPC listener serves.
- Admin is fully served: the content RPCs, accounts, and `server info` (no Zone content listed).
- `Game.OpenSession` is refused `UNAVAILABLE`, with `ErrorInfo{domain: "andara.game", reason:
  "no_content_in_effect"}`. Clients may retry it.
- No Zone ticks, because there's no Zone.

**It leaves the wait** when the first swap with `zone_count > 0` applies, after an Active Pointer
move, through the existing `FollowContent` → `ReconcileContent` path, unchanged. It never re-enters
the wait in that process or after a restart, because an applied swap with Zones is now in the log.
If that swap is refused, it still waits.

**The first content must hold the spawn Room.** While waiting, `ActivateVersion` refuses a version
whose resulting World lacks `character.spawn_room`: `FAILED_PRECONDITION`, reason
`spawn_room_removed`, with the Room as its subject (AC-8). The reason's meaning widens from "removes
the spawn Room the World in effect has" to "the World after this move would lack
`character.spawn_room`" (`AW-SRV-013` AC-14). Once Ready, the two are the same, because boot's
`CheckSpawnInEffect` holds it, so `AW-SRV-013`'s reason set doesn't change.

| Path (on `http.port`) | 200 when |
|---|---|
| `/livez` | unchanged |
| `/startedz` | **new.** Recovery has finished and the gRPC listener is serving, whether or not the server waits. It stays 200 until exit |
| `/readyz` | unchanged, plus 503 while waiting for content |

**CLI:**

| Flag | Env | Config key | Default |
|---|---|---|---|
| `--tls-server-name` | `ANDARA_TLS_SERVER_NAME` | `server.tls_server_name` | empty, meaning the host from `server.address` |

- Precedence follows `AW-CLI-001`, as for `server.tls_ca`.
- The flag sets only the TLS verification name. It doesn't change the dial target, or the key that
  credentials are stored under (`server.address`).
- `config show` lists it with its source.

## Data / state impact

`ContentSwap` gains `zone_count = 5`. It's additive, and older swaps read it as 0, so a log written
before the field, with a refusing store, waits rather than exits. That's the safe direction. `dev`'s
switch runs `make world-reset` in the same roll (`AW-INF-021`), so its log carries the field from
genesis. `prod` runs nothing. Compose and kind stacks are disposable. Rollback: an older binary ignores
the field. A log the new binary wrote replays under the old one, which reads only fields 1–4.
Waiting is derived at boot from the log and the store, and nothing
persists it. A rollout to an existing `dev` that already holds a swap with Zones behaves as it does
today.

## Observability requirements

*(SRE observability review, 2026-10-01: the refused-OpenSession counter named, no waiting gauge,
trace_id on the wait lines.)*

- **Metrics:**
  - `andara_sessions_total{outcome}` gains `rejected_no_content`, a bounded value pre-seeded at 0
    beside `rejected_auth` and `rejected_version`. It counts each `OpenSession` refused
    `no_content_in_effect`.
  - No waiting gauge. While the server waits, `andara_content_zones_loaded` is `0` on a started pod
    (`/startedz` 200, `/readyz` 503). That and the `warn` line identify the wait.
- **Logs:**
  - `warn` `waiting for content: no Zones in effect; publish and activate a pack`, once on entering
    the wait. Fields: `content_source`, `packs`, `core_version`, `trace_id`.
  - `info` `content in effect: leaving the wait`, once. Fields: `zones`, the triggering
    `pack@version`, `trace_id` (the swap record's, `content.load`'s trace).
  - A refused `OpenSession` logs at `debug` with `trace_id`, `reason=no_content_in_effect`, and the
    `session_id` if assigned, else the remote address, as the other `OpenSession` refusals do.
- **Traces:** none new. The leaving line's `trace_id` comes from the swap record that ends the wait
  (`SwapApplied.TraceParent`), meaning the Loader's `content.load` trace, which links to the
  activation once `AW-SRV-045` ships. *(Amended 2026-10-01: this said "the leaving
  `ReconcileContent`'s span". The wait ends on a `FollowContent` swap, as built.)*
- **Alerts:** none new. `AndaraServerUnavailable` fires while a fresh environment waits, which is
  true. Its runbook row (`server-unavailable.md`, first fix `make content-seed ENV=<env>`) and the
  startup probe's move to `/startedz` are SRE's, in `AW-INF-021`.

### SRE amendment, 2026-10-02: the waiting state's `no_zones_found` (#299)

Recorded after `done`. Implementation makes the code change (SPRINT-04, implementation item 9).
Recorded in `docs/feedback/AW-SRV-042-empty-store-waits.md`.

**What happens today.** On a store-backed source (`content.source=kafka`) with no Zones in effect,
every `LoadContent` logs two lines (`server/boot/boot.go`, `server/content/load.go`):
- the finding `no_zones_found` at `error`, counted in
  `andara_content_validation_errors_total{code="no_zones_found"}`;
- a `warn`, `the content the Active Pointers name does not load; recovering what the log recorded`.

The server does this once per boot, before `ReconcileContent` decides. The projector's
`WaitForContent` reloads through `LoadContent` on a backoff from 1 s up to 60 s, so it repeats
both lines, plus an `info` `templates loaded` per pack, on every reload. That was 10 `error` lines
in 44 s in `AW-INF-021`'s AC-7 run. The state is the expected one on every fresh environment, and
expected `error` and `warn` lines train operators to ignore those levels.

**The case this amendment covers: an empty store.** All four hold, on a store-backed source:
1. not `--validate-only`;
2. the store was read: no `malformed` from an unreadable store;
3. **no rejections**. `Candidates` returned none. A pointer naming a version with no manifest is a
   rejection whose own finding is also coded `no_zones_found` (`server/content/errors.go`), and it
   is **not** this case;
4. the only fatal finding is the one `loadFindings` appends for zero Zones. That's computed once,
   after the load, build and template findings are all collected, and advisory warnings don't
   count.

Anything else is unchanged: a refused version or an unreachable store is a real fault and is
reported at load as today.

**The contract, for the empty-store case:**
- **`LoadContent` holds the finding.** It logs neither the `error` line nor the "recovering what
  the log recorded" `warn`, and it doesn't count. `content.validate`'s `error_count` excludes the
  held finding. The finding stays held until the decision settles it.
- **The decision settles it:**
  - **Waits:** dropped, with no line and no count. Of the lines this amendment governs, the wait
    line is the only one. Other existing Loader `warn`s, such as `configured content pack has no
    Active Pointer` for an explicit `content.packs` list, are unchanged.
  - **Serves** (`ReconcileContent` returns `0` with Zones in effect from the World log): the
    finding isn't logged or counted, but the "recovering what the log recorded" `warn` is logged
    once, with `content.reconcile`'s `trace_id` and `error_count` excluding the held finding (`0`
    in the empty-store case). The log holds Zones that no followed pack's
    pointer names. That's usually a store fault (lost pointer topics), so the line is kept.
  - **Exits `1` before the decision settles it**, by any path: AC-3's "a World log in which a
    Content Swap with `zone_count > 0` has applied", a failed log read, a failed reconcile, or a
    tick-loop start failure. The finding is logged once, with its existing fields, and counted
    once, before the process exits. Its `trace_id` is the active span's (`content.reconcile` for
    reconcile's exits), or `content.load`'s where the exit runs outside any span.
  - **Ends on a signal before the decision** (exit `0`): dropped.
  - **The projector's decision** is `WaitForContent` entering the wait. An exit before that, such
    as a failed pointer watch (exit `1`), logs the finding as an exit does.
  - Once the decision has settled it (dropped on a wait or a serve), a later exit doesn't revive
    it.
- **Reloads during a wait** (the projector's `WaitForContent`; the server waits on swaps, not
  reloads): a reload in the empty-store case logs one line, at `debug`, `content reload: no Zones
  in effect yet`, with `next_retry` (the backoff before the next reload) and `trace_id`
  (`content.wait`'s). It logs nothing else: no `warn`, no `error`, and `templates loaded` drops to
  `debug`. A reload that finds anything else, such as an activated pack that fails validation or
  an unreachable store, logs and counts as today.
- **Unchanged:** the directory source, which has no pointers to wait on, and `--validate-only`,
  which exits inside `LoadContent`. Both still log `no_zones_found` at `error`, count it, and
  exit `1`.
- **Cardinality is unchanged.** `code` keeps its value set, and only when the series moves changes.
- **Nothing keys on it.** No rule, dashboard, runbook, SLO doc, script, workflow, or smoke test
  references `content_validation_errors_total`, `no_zones_found`, or either line. That was checked
  across `deploy/`, `docs/runbooks/`, `docs/specs/slo/`, `scripts/`, `.github/`, and `internal/`
  on 2026-10-02. `ContentLoadFailing` reads `andara_content_pending_seconds`.

**Verification at item 9's §8: the integration tests alone** (PM's decision, 2026-10-02). The
assertions are on the finding's code, not on log level, since an exit logs `error` lines of its own.
Each case runs the real path through `LoadContent` on the local stack's Redpanda, not the
`OverLoader` shortcut, which skips it. Item 9 names the tests.
- **Server, hermetic setup:** `storeRuntime` (`server/boot/projector_wait_integration_test.go`), plus
  `rt.replay = worldLog(...)` and `startMemoryLoop`, as
  `TestReconcileContent_WaitsOnlyForAWorldThatNeverHadZones` does. Not `sim.source=kafka`, which
  reads the shared `andara.commands.v1`.
  - **Waits:** no line with `code=no_zones_found`, no "recovering what the log recorded", and
    `ValidationErrors.WithLabelValues("no_zones_found")` reads `0`.
  - **Serves from the log, with no pointer:** use a fresh Active Pointer topic over the same blobs
    and versions, with town published but not activated, and a Zone-bearing swap for town@1 pushed
    onto `rt.memSource` before `ReconcileContent`. The swap has an empty base and no `zone_id`, and
    its `world_digest` comes from `rt.Content.Prepare` and `sim.ContentDigest`, as
    `TestStartTickLoop_APostRuleMismatchBeforeGenesisIsNotPreRule` builds it. A wrong digest halts
    the tick instead of being refused. No `no_zones_found` line and a count of `0`;
    exactly one "recovering what the log recorded".
  - **Exits in reconcile (AC-3):** exactly one line with `code=no_zones_found`, and a count of
    `1`.
  - **A pointer naming a version with no manifest:** logs and counts at load as today. Between
    `LoadContent`'s start and its return, there are exactly two lines with `code=no_zones_found`:
    the rejection's own, and the one carrying `loadFindings`' detail, "no Zones were found in
    kafka…". A held appended finding would show as one. Reconcile's Loader then logs the rejection
    again as `content finding`, unchanged, which counts `LoadFailures`, not this series. The count
    reads `2` over the whole boot.
- **Projector:** `TestProjectorBoot_ReadOnlyAndWaitsForZones`, extended. Over the projector's whole
  log, from its first line (its boot `LoadContent` included) to `content in effect: leaving the
  wait`:
  - no line with `code=no_zones_found`, and no "recovering what the log recorded";
  - exactly one wait line;
  - at least one `content reload: no Zones in effect yet`. That's unconditional: the test already
    asserts the leaving line's `pack` is `town@1`, so the core move's reload ran and didn't leave.
    That assertion's existing race with town's `MovePointer` is known, and this one shares it;
  - the count is `0`.

  It asserts nothing else by level. Other lines, such as `templates loaded` or a transient
  `content pointer watch: fetch failed, retrying`, aren't this amendment's.
- **Projector, timer reloads:** a separate test with a short `waitRetry` asserts two or more
  `content reload: no Zones in effect yet` lines carrying `next_retry` and `trace_id`. It doesn't
  assert the leaving line's `pack`, which a timer reload can race.

**Live: not yet observed.** No `dev` start on an empty store is scheduled after item 9. Whoever next
starts `dev` from an empty store checks these, and records it in this story's §8 record:
- the `andara-server` and `andara-projector-state` logs hold no `no_zones_found` and no "recovering
  what the log recorded";
- `up` is `1` and `andara_content_zones_loaded` is present at `0` on both jobs (the positive
  control);
- then `andara_content_validation_errors_total{code="no_zones_found"}` is absent or `0`.

## Test plan

- **Unit:**
  - the boot decision table: kafka with an empty log; kafka with an applied swap with Zones and a
    refusing store; kafka whose only Zone-bearing swap was refused; dir with no Zones (ACs 1, 3, 4);
  - the AC-1 test asserts `andara_sessions_total{outcome="rejected_no_content"}` present at 0, then
    rising by one for the refused `OpenSession` (SRE review);
  - the spawn-Room refusal while waiting (AC-8), and `zone_count` on produced swaps (AC-9);
  - the health endpoints in each state, including drain (AC-7);
  - the `OpenSession` refusal's `ErrorInfo`;
  - the CLI's flag, env and config precedence, and `config show`.
- **Integration**, against the local stack (Redpanda):
  - boot on an empty store, then publish, approve and activate, and assert the server becomes Ready
    with no restart (AC-2);
  - restart while waiting, and restart after the swap (AC-6);
  - a TLS dial through a forwarded port with a server-name mismatch, with and without the flag (AC-5).
- **Manual/operator:** `make content-seed ENV=dev` on a fresh `dev` (SRE's target, `AW-INF-021`) runs
  through this path end to end.

## Definition of done

CLAUDE.md §8, plus: `server/README.md` documents `/startedz` and the probe guidance, and
`admin/README.md` documents `server.tls_server_name`.

## Open questions

1. **Resolved 2026-10-01 (architecture): option (a), `ContentSwap.zone_count = 5`.** Option (b)
   would count a Templates-only Builder pack as content, and a restart would then exit with no Zones,
   which is the same deadlock. Option (c) misses every swap after the last round.
2. **Resolved 2026-10-01:** `AW-INF-021` depends on this story, as of this review's commit.

## Contract review (architecture, 2026-10-01)

SRE's observability review is in `docs/feedback/AW-SRV-042-empty-store-waits.md`, and its replacement
section is pasted as written. Amended here:
1. **`zone_count` pinned** in `log.proto`, and `gen/` regenerated. The wait condition, AC-3 and the
   leaving rule now read *applied* swaps with `zone_count > 0` (Codex on #289 and #292).
2. **AC-8, the spawn Room,** from Codex's review of #289. It's numbered after PM's AC-7 (drain).
3. **AC-9:** `zone_count` is set and replays.
4. **AC-5's failure is exit `3`**, per `AW-CLI-001`, not "its connection error".
5. **Data / state impact** states the field's migration and rollback, as CLAUDE.md §6 requires.
6. **Decidability** (Codex on #295): the classification walks the records alone. That's exact for
   every log a correct Loader writes, because `zone_removed` can't fire on a World without Zones,
   and `fallback_missing` on a produced swap is a Loader/Engine defect that exits loudly. Stated in
   the Interface contract. AC-3 names the record-decidable refusals.

The story is `ready`. It's SPRINT-03 implementation item 11, the last code on the M3 demo's path.

## Implementation record (2026-10-01)

On `impl/aw-srv-042-empty-store-waits`.
- **The decision:** `boot.worldHadZones` walks the World Partition's swap records alone and
  decides each as `prepareSwaps` does for `misrouted` and `stale_base`.
- **Entering:** `ReconcileContent` waits when a store-backed World has never had Zones.
- **Leaving:** `contentApplied` ends the wait on the first swap with Zones that holds the spawn
  Room. `MarkStarted` and `/startedz` separate started from ready.
- **Elsewhere:** the Gateway refuses `OpenSession`; the Loader sets `zone_count` and widens the
  spawn rule; the CLI resolves `server.tls_server_name`.

Decisions are in `docs/feedback/AW-SRV-042-empty-store-waits.md`, "Implementation, 2026-10-01".

| AC | Covered by | Result |
|----|------------|--------|
| 1 | `boot` `TestReconcileContent_WaitsOnlyForAWorldThatNeverHadZones` (the real `ReconcileContent` over an empty in-memory store, through `content.OverLoader`: exit 0, waiting, unready, the `warn` line once). `TestWait_StartedUnreadyThenReady` (`/startedz` 200 and `/readyz` 503 while waiting). `gateway` `TestOpenSession_RefusedWhileWaitingForContent` (`UNAVAILABLE`, `andara.game` / `no_content_in_effect`, `rejected_no_content` 0 → 1, the debug line, Admin served). `smoke` `TestLive_EmptyStoreWaits` on a fresh kafka stack | pass in process; the stack run is SRE's (feedback, For SRE) |
| 2 | `boot` `TestReconcileContent_TheFirstZonesThroughTheLoopEndTheWait`: the store gains `andara.core` and a town holding the spawn Room, and a reconcile brings them in through the tick loop. Its `contentApplied` ends the wait, ready with no restart, and the `info` line carries `zones`. `TestWait_StartedUnreadyThenReady` checks the line's `pack@version` and `trace_id`. Mutation-checked: without the hook in `contentApplied`, it never leaves | pass |
| 3 | `boot` `TestWorldHadZones_FromTheRecordsAlone` (ten cases, including a stale-only log, a misrouted-only log, and a refused swap then a good one). `TestWorldHadZones_AgreesWithTheEngine`: a real Engine over applied, stale, misrouted and good swaps, with the walk agreeing after every record. `TestReconcileContent_…`: an applied Zone swap exits 1 with "no content in effect", and a refused-only one waits. Mutation-checked: without the stale-base rule, four cases fail | pass |
| 4 | `TestReconcileContent_…` "a directory with no Zones: exits" | pass |
| 5 | `admin/cli` `TestTLSServerName_VerifiesAgainstTheNamedHost`: a gateway whose certificate names only `andara-0.andara.test.svc` (`testpki.NewFor`), dialed at `127.0.0.1`. With the name, by flag, env or config, login succeeds, and the credential is keyed by the dialed address. Without it, exit 3. `config show` reports flag over env over file. Mutation-checked: without `ServerName`, it fails | pass |
| 6 | `TestReconcileContent_…`: two boots over the same empty log both wait. One over a log with an applied Zone swap doesn't wait; with Zones in the store it recovers Ready, the ordinary path | pass |
| 7 | `TestWait_StartedUnreadyThenReady` and `TestWait_LeavingBeforeStartAndDuringDrain`: draining, `/readyz` 503 and `/startedz` 200, and content arriving mid-drain doesn't make it ready | pass |
| 8 | `content` `TestActivateVersion_TheFirstZonesMustHoldTheSpawnRoom`: with only `andara.core` in effect, a first town without `purgatory/start` is refused `FAILED_PRECONDITION` `spawn_room_removed` (`purgatory/start`), and the pointer and World are unchanged. One that holds it activates. Mutation-checked: with the old "had it" rule, it fails | pass |
| 9 | `content` `TestLoader_SwapsCarryTheWorldsZoneCount`: core, docks, town produce `zone_count` 0, 1, 2, and a fresh Engine replaying the records holds exactly that many Zones after each | pass |

`make check` passes.

**Not done here, and why:**
- **The stack run of AC-1, AC-2 and AC-6** needs SRE's kafka-content compose (#288), since topic
  names are constants (feedback, For SRE). The smoke test for it is in this PR.

## §8 review (architecture, 2026-10-01): stays `review` on SRE's record

Against `main` after #298. #298's checks are all green. Every test in the implementation record
re-ran green in this review: `boot`, `gateway`, `content` and `admin/cli`. Each AC is covered as the
record states, and the mutation checks are implementation's: the leave hook, the stale-base rule,
the old spawn rule, and `ServerName`.

**Observed live on `dev`** (`AW-INF-021`'s rollout record, 2026-10-01).
- `world-reset` left an empty World and a store with no Zone pack. The server reported
  `started and waiting for content`, so AC-1's wait happened, on a real kafka-content environment.
- `make content-seed` published and activated `town@1` through a port-forward verified as
  `andara-0.andara.andara-dev.svc`. That's AC-5's `--tls-server-name`, in use.
- `andara-0` went from started-not-ready to Ready with 0 restarts. That's AC-2's "no restart".

**The contract decisions made in building are accepted** (comment on #298), and the story text
changes in two places:
- The leaving line's `trace_id` comes from the swap record (`SwapApplied.TraceParent`), meaning
  `content.load`'s trace, not "the leaving `ReconcileContent`'s span". Per the 2026-10-01 ruling in
  `AW-INF-021`'s feedback, that trace *links* to the activation once
  `ActiveVersion.trace_parent` ships. It isn't the activation's trace. The Observability text reads
  that way from here on.
- The publish gate keeps its spawn rule (`moving=false`), and a move to a World with no Zones is
  exempt from AC-8.

**What closes it:** SRE's §8 instrumentation record:
- `andara_sessions_total{outcome="rejected_no_content"}` from 0 to 1 on a refused `OpenSession`;
- the `warn` and `info` wait lines with their fields and `trace_id`;
- the refusal's `debug` line;
- and SRE's stack run of `TestLive_EmptyStoreWaits` (AC-1, AC-2 and AC-6 end to end), on the
  kafka-content compose stack, as the feedback file asks.

Architecture then moves the story to `done` without another pass.

## §8 instrumentation check (SRE, 2026-10-01): satisfied

On the compose stack built from `main` at `0cb713c`, with `ANDARA_LOG_LEVEL=debug` and fresh
volumes. The World topics were recreated, and the server ran on `ANDARA_CONTENT_SOURCE=kafka`,
`ANDARA_CONTENT_PACKS=*`, with an empty store and log.

| Signal | Backend | Observed |
|--------|---------|----------|
| `andara_sessions_total{outcome="rejected_no_content"}` | the server's `/metrics` | `0` while waiting (pre-seeded), `1` after `TestLive_EmptyStoreWaits` (`ANDARA_SMOKE_EXPECT_EMPTY_STORE=1`), which passes |
| `andara_content_zones_loaded` | `/metrics` | `0` while started (`/startedz` 200, `/readyz` 503) |
| `warn` `waiting for content: no Zones in effect; publish and activate a pack` | local Loki | once, with `content_source=kafka`, `packs=*`, `core_version=1`, `trace_id` |
| `debug` `session rejected: no content in effect` | local Loki | `reason=no_content_in_effect`, `remote_addr`, `client_name=stack-smoke/empty-store`, `trace_id`. It carries no `session_id`, because it's refused before one is assigned, so `remote_addr` stands in, as SRE's review specified |
| `info` `content in effect: leaving the wait` | local Loki | after `make content-seed` (address mode): `zones=4`, `pack=town@1`, `trace_id`, and `/readyz` 200 with no restart |

**On a real cluster:** the wait → seed → Ready path ran on `dev` in `AW-INF-021`'s rollout. That's
`world-reset`'s `started and waiting for content`, then `content-seed`, then Ready in about 4 s with
0 restarts, and the server's lines in Grafana Cloud. AC-6, a restart while waiting (waits again,
no exit loop) and a restart after the first swap (recovers Ready), ran on the same kafka-content
compose stack against #298's build, together with AC-1 and AC-2 end to end (SRE's comment on #298).
The leaving line's `trace_id` is the swap record's, `content.load`'s trace, as architecture's review
above reads it.

The instrumentation item is **satisfied**. #299 (the Loader's `no_zones_found` `error` beside the
wait line) is separate, and doesn't touch these signals.

## §8 close (architecture, 2026-10-01): `done`

SRE's instrumentation record (above) is accepted. It was observed on an empty-store, kafka-content
compose stack:
- `rejected_no_content` went from 0 to 1;
- the `warn` and `info` wait lines carried their fields and `trace_id`;
- the refusal's `debug` line carried `reason`, `remote_addr` and `trace_id`, with no `session_id`,
  because it's refused before one exists, as the story allows;
- `TestLive_EmptyStoreWaits` passes, which covers AC-1, AC-2 and AC-6 end to end.

`dev`'s own wait, seed and Ready path is in `AW-INF-021`'s record. Every checklist item holds.
