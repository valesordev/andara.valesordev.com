---
id: AW-SRV-007
title: Recovery from snapshot and log tail, verified in CI
epic: EPIC-04
component: server
type: feature
status: review
size: M
depends_on: [AW-SRV-006, AW-SRV-026, AW-SRV-028, AW-SRV-015, AW-SRV-043]
blocks: [AW-INF-007, AW-SRV-032, AW-INF-011, AW-INF-032, AW-INF-009, AW-SRV-047, AW-SRV-048]
lane: implementation
risk: high
---

## Context

ADR-0002 is explicit that a recovery path not exercised in CI is a recovery path that does not work.
This story makes `kill -9` a tested operation: load the newest complete snapshot round, resume consuming
from its offsets, and assert the resulting State Hash equals what it was before the kill.

The determinism guarantees from `AW-SRV-002` are what make this possible, and this is where they stop
being a design principle and become a production dependency. ADR-0002 §4's Tick Boundary Records are
what make replay *exact* rather than approximately exact — replay reads the recorded offset ranges
rather than re-deciding them.

**Amended at SPRINT-04's contract review (architecture, 2026-10-02), after `ready` and before
implementation started.** The changes, each answered in `docs/feedback/AW-SRV-007-recovery-scale.md`:
- `AW-SRV-043` joins `depends_on`; its restore mismatch is exit `6`.
- The incomplete-round exit moves from `5` to `7`, because `AW-SRV-026` holds the server's `5`.
- The hash mismatch moves from `2` to `8`. Go exits `2` on an unrecovered panic or a runtime fatal
  error, so on the cluster, where only the exit code outlives the process, `2` can't name a hash
  mismatch. `2` is never assigned.
- AC-5 is scoped to `grpc.listen`, with SRE's linger as AC-14 and `recovery.mismatch_linger` as a key.
- AC-7 states its scale and a bound that the measured replay rate can meet.
- AC-8 measures the RPO where the ack is held, and `andara_acknowledged_commands_lost_total` is
  withdrawn.
- AC-11 also covers `sim_seed`.
- AC-12 adds the seek.
- AC-13 covers exit `6`.
- The Admin RPCs are pinned in `admin.proto`.
- The operator test is `make stack-recover`.

**Decided 2026-09-11 (Brian): on a State Hash mismatch the server refuses to start.** It does not
search backwards for a round that verifies. A server that picks its own history is a server that can
silently serve the wrong World; a server that stops is an incident with a human in it. The operator's
tools for that incident are in this story's contract.

## User story

As an operator, I want a killed server to return to a playable World within RTO with a verifiable state
match, so that a crash is an interruption rather than an incident.

## Scope

### In scope
- Recovery at boot: select the newest complete snapshot round, load every Zone, seek each Partition to
  the round's offsets, replay to the log head using recorded `TickCompleted` boundaries, verify the
  State Hash against the last boundary, then and only then report ready.
- Refusal, exit code, and log line on: hash mismatch, log gap, unreadable `state_version`, incomplete
  round with no older complete one.
- `andara-server recover --verify [--round <tick>]` — recover and compare without accepting connections.
- `andara-cli snapshot list` and `andara-cli snapshot verify` over `Admin`, so an operator can choose a
  round without a shell on the pod.
- Re-apply idempotency across the checkpoint window, per `AW-SRV-002` AC-10.
- Recovery-time measurement by phase, published as a metric and as a CI artifact.
- A cold start with no snapshot at all replays from offset zero — the M1 behavior, kept.

### Out of scope
- Snapshot writing — `AW-SRV-006`.
- Deploy lifecycle, pre-stop snapshot, and the rollback runbook — `AW-INF-007`.
- Probes. This story exposes readiness; `AW-INF-003` wires it.
- What players see. Recovery is silent to a connected client beyond a dropped stream; `AW-SRV-015`
  rebinds them and `AW-INF-007` owns any notice.
- Marking a Character linkdead who was in a `Transit` record at the kill, once its handoff lands. The boot
  sweep takes the bodies present at boot; the ones in transit land afterwards and stay unmarked, as today. It
  is an ordering protocol between the Roster and the sim, requested from PM as its own story in
  `docs/feedback/AW-SRV-007-transit-orphan-mark.md` (it was AC-17 here until 2026-10-05).

## Acceptance criteria

1. **Given** a running World at tick `T` with a known State Hash **when** the process is killed with
   `SIGKILL` and restarted **then** the recovered World's State Hash at tick `T` is identical, and the
   server reports ready only after that comparison.
2. **Given** a round whose offsets precede the earliest available log offset on any owned Partition
   **when** recovery runs **then** it exits `3` with an `error` line naming the Partition, the round's
   offset, and the log's earliest offset. A World is never recovered from an incomplete history.
3. **Given** recorded `TickCompleted` boundaries **when** replay runs with different fetch batching
   than the original **then** the boundaries are taken from the records and the State Hash sequence is
   identical at every boundary.
4. **Given** a round with a missing or hash-invalid Zone object **when** rounds are listed **then** it
   is `incomplete` and recovery selects the newest `complete` one instead, when no round is named.
   A named one exits `7` (AC-15).
5. **Given** the recovered hash differs from the `TickCompleted` hash at the same tick **when**
   replay reaches a boundary whose hash differs (the first such boundary; replay stops there)
   **then** the server exits `8`, `andara_recovery_state_hash_match` is `0`,
   and the `error` line names the tick, both hashes, and the round used. It never binds
   `grpc.listen`, so no player or Admin connection is ever accepted. With
   `recovery.mismatch_linger` above `0s`, operator HTTP serves for that long first (AC-14).
   *(Reworded 2026-10-02: "never accepts a connection" also forbade the operator HTTP that AC-14
   needs. The checkable property is the port: while the process runs, including any linger, a
   dial to `grpc.listen` is refused.)*
6. **Given** any change to `server/sim` or `server/store` **when** CI runs **then** the
   kill-and-recover integration test executes and gates the merge.
7. **Given** the sizing fixture (`server/simtest/sizing.go`: 25,000 Entities, 2,000 Rooms,
   16 Zones, 500 Characters) and a tail of 600 ticks, one `snapshot.interval` at the 100 ms tick
   (ADR-0008) **when** recovery runs in CI **then** `recovery-timing.json` is published with
   per-phase durations, the tail length in ticks, the Entity count, and peak RSS. The `total`
   phase is under 90 s, which leaves 30 s of M2's 120 s for detection and process start.
   *(Amended 2026-10-02. The old bound, `replay` under 5 s on a 60 s tail, was set before anything
   was measured. `AW-SRV-019` measured tail replay at about 65 ms per tick at this scale, so about
   39 s for 600 ticks. A World can't replay faster than its ticks cost to apply.)*
8. **Given** the kill-and-recover test's clients, which record the `SubmitResponse` partition and
   `accepted_offset` of every Command acknowledged to them before the kill **when** recovery
   completes **then** every recorded offset is below the replayed head of its Partition, and the
   record at that offset on `andara.commands.v1` is the Command that was acknowledged.
   *(Amended 2026-10-02. The ack lives in the client, not in the killed process, so a counter in
   the restarted server has nothing to compare with. It would read `0` by construction, and an AC
   on it couldn't fail. `andara_acknowledged_commands_lost_total` is withdrawn. The RPO is
   asserted here and by `AW-INF-032`, whose tail move is an acknowledged Command that only the log
   holds.)*
9. **Given** no snapshot round exists **when** recovery runs **then** it replays from offset zero on
   every Partition and reaches ready with a hash matching the last `TickCompleted`.
10. **Given** `recover --verify --round 4200`, naming a complete round, **when** it runs **then** it loads that round, replays to
    head, prints `match` or `mismatch` with both hashes, exits `0` or `8`, and never binds `grpc.listen`.
    *(`2` became `8` on 2026-10-02; see the exit table.)*
11. **Given** a round whose Zone objects carry disagreeing `prng_state` or `next_event_id` **when** the
    round is loaded **then** it is refused, naming the Zones and both values; the round is not
    selectable and, when no round is named, recovery falls back to the newest round that agrees (a
    named one exits `7`, AC-15). The same holds for
    disagreeing `sim_seed` (`AW-SRV-043`, added 2026-10-02).
    **Added 2026-09-22, from `AW-SRV-006`'s implementation (feedback §4).** `sim.WorldState` holds one
    PRNG and one `NextEventID` for the World, not one per Zone, but `ZoneState` carries both — so every
    object in a round repeats the same two values. A round is one cut at one tick, so they agree by
    construction, and the repetition is what lets a Zone be restored alone with the PRNG state replay
    needs. Which means a round where they *disagree* is a round assembled from two different cuts, and
    restoring it would seed replay with a generator that never produced the recorded history. This
    criterion also fixes which copy wins when they agree: any of them, because they are equal — the
    check is the contract, not a tie-break.
12. **Given** 864,000 ticks of history ahead of the round, which is `AW-SRV-019` AC-6's 24 h
    fixture, and a 10-tick tail **when** recovery runs **then** the round's own `TickCompleted` is
    found by binary search over the boundary Partition, not by reading the history. The run's
    `load` + `seek` + `replay` time is within 1 s of the same run with no history, and its peak
    RSS is within 10% of that run's. *(Added 2026-10-02, feedback item 2.)*
13. **Given** a round that `AW-SRV-043`'s `RestoreEngine` refuses, as `ErrRestoreMismatch` or
    `ErrSeedMismatch`, or one that doesn't restore onto the content in effect
    (`sim.ContentDigestError`, or `sim.ErrRoundZoneUnknown`, below), or whose recorded content can't be
    resolved before restore because it names a pack version the source doesn't have (`sim.ErrRoundContent`, AC-16) **when** boot recovery runs
    **then** the server exits `6`. Nothing is
    replayed and no other round is tried. `andara_recovery_state_hash_match` is `0`,
    `andara_recovery_failures_total{reason="restore"}` is `1`, and one `error` line
    `recovery restore mismatch` carries `round_tick`, `reason` (`hash`, `seed` or `content`), and
    `recorded_hash`/`restored_hash`, `recorded_seed`/`configured_seed`, or, for `content`,
    `pack`, `recorded_digest` and `built_digest` for a digest mismatch, `zone_id` for an unknown
    Zone, and `pack_versions` for a pack version the source doesn't have (AC-16). It never binds
    `grpc.listen`, and AC-14's linger applies. *(Added 2026-10-02, feedback item 3.)*
14. **Given** `recovery.mismatch_linger` of `60s` **when** boot recovery ends in exit `8` or `6`
    **then**, for 60 s before exiting, the server serves `/metrics` and `/livez` with `200` and
    `/readyz` and `/startedz` with `503`, logs one `error` line
    `holding /metrics for the hash mismatch to be scraped` with `for`, and never binds
    `grpc.listen`. A `SIGTERM` or `SIGINT` during the linger ends it at once with the same exit
    code. **Given** `0s`, the default, it exits at once. `recover --verify` never lingers.
    *(Added 2026-10-02, SRE's amendment, feedback item 5.)*
15. **Given** a named round that isn't complete, named by `recover --verify --round T` or by
    `recovery.pin_round=T` at boot **when** recovery runs **then** it exits `7`, restores nothing,
    and tries no other round. `andara_recovery_failures_total{reason="round"}` is `1`, and one
    `error` line carries `round_tick` and `cause`. The causes map `ListRounds`' reasons (a fifth, `content`, exits `6` when selected: AC-16):
    - `missing`: no object for a Zone, or one that vanished after listing; a tick with no object
      at all is `missing` with every owned Zone;
    - `duplicate`: two objects for one Zone;
    - `hash`: an object that's hash-invalid, undecodable (including its PRNG), unmigratable (no
      migration step, or `state_version` 0), or whose envelope names another Zone or tick;
    - `disagree`: objects whose PRNG, EventID, `sim_seed`, content in effect, or Partition offsets
      differ.

    **A named tick resolves to one round.** After a rollback, the same tick can hold a group at a
    newer `state_version` and one at an older version (the key format above). The name resolves to
    the highest `state_version` at `T` that this binary can read. AC-15 then applies to that group.
    Exit `4` happens only when every group at `T` is newer than the binary. Exit `7` doesn't set `andara_recovery_state_hash_match` and doesn't linger. **Given** the same round through
    `Admin.VerifySnapshotRound` **then** the RPC returns `FAILED_PRECONDITION` (`NOT_FOUND` when no
    object exists at the tick), and `andara-cli snapshot verify` exits `1`.
    *(Added in PR #356 review, 2026-10-03.)*
16. **Given** a round written at content version V and a content swap applied after it that adds a
    Zone, **when** the server is killed and recovers **then** the round is judged complete against the Zones
    of V, not of the content in effect at boot: it is selected, restored on V, and the swap is replayed from
    the log. (A swap can't remove a Zone: `AW-SRV-012` refuses it, so V's Zones are always a subset of any
    later content's, and the added Zone is what makes a good round `missing` against the current content.)
    - **Which Zones.** Discovery lists the current content's Zones, a superset of V's for swap-only change.
      **When the current content has no Zones** (the boot's serve-from-the-log path, `server/README.md`:
      "Zones no pointer names"), discovery lists none, finds no round, and recovery does what it does today: it replays the log,
      bounded by retention (exit `3`); `recovery.require_snapshot` and `recovery.pin_round` exit `7` with
      no Zones named; `snapshot list` shows no rounds. `WorldStore.List` is per Zone and can't
      enumerate Zones; lifting that limit needs a Zone enumeration on the store, which this story doesn't add.
      V is the `content` of the first hash-valid envelope in Zone order; envelopes that disagree on it are
      `disagree`, as AC-11 has it. A round with no hash-valid envelope, and a tick with no object at all, are
      judged against the current content's Zones (`missing` names them). The owned set is V's Zones that this
      process's Partitions own (all 64 today, ADR-0002). A V Zone with no object is `missing`.
    - **Cause `content`** is new beside AC-15's four. It covers a round whose recorded content names a
      pack version the source doesn't have (the versions are in `Reason`), and an object for a listed Zone that V doesn't list
      (the `zone_id` is in `Reason`). It ranks below `duplicate`, `hash` and `disagree`, and above the
      missing-Zone problem: when V names a version the source doesn't have there is no Zone list to compute it against. A vanished
      object keeps its own cause, `missing`, as today. The cause lives on `store.Round` (`Cause`, `CauseZones`)
      and isn't on the wire: `snapshot list` shows only `complete=false` for it, with no proto change.
      **When it is selected**, by boot's newest round, `--round T` or `recovery.pin_round`, it exits `6` with
      `reason=content`, counted under `reason="restore"`, and no other round is tried (AC-13).
      `NewestComplete` and `RoundAt` both translate cause `content` into `sim.ErrRoundZoneUnknown` (the
      unknown Zone) or a new typed `sim.ErrRoundContent{Tick, Versions}` (a pack version the source doesn't have), never into
      `ErrRoundIncomplete`, and `NewestComplete` returns it on meeting such a round rather than skipping it.
      `Classify` maps both to `6`. `Admin.VerifySnapshotRound` and `snapshot verify --round T` return
      `VERIFY_OUTCOME_CONTENT_MISMATCH` for it, as they already do for `ErrRoundZoneUnknown`; the response
      carries no cause field and the log line carries the versions or the Zone.
      **Logging.** Both errors are raised before `RestoreEngine`, in round selection and in the `Prepare`
      call, so they are logged as `recovery restore mismatch` (`logRestoreMismatch`, with `zone_id` or
      `pack_versions`), not as `recovery refused` (`logRefusal`), and `andara_recovery_state_hash_match`
      is `0` (AC-13). **Only a version the source doesn't have** is cause `content`: `ContentSource.Prepare` returns a new
      sentinel `sim.ErrContentVersionUnknown{Pack, Version}` for it, and the rebuild's wrapped `Prepare`
      failure becomes `ErrRoundContent{Tick, Versions}` only when it wraps that. `ErrNoContentSource`, a
      transient source or registry failure and a missing config stay exit `1`, `reason=store`: retryable or
      operator errors, not a state mismatch, and they must not set the gauge `RecoveryStateMismatch` pages
      on. The tick for the log line comes from the error's `Tick`, and `selectRound`'s caller routes both
      typed errors to `logRestoreMismatch`. **This moves a version the source doesn't have from exit `1`**
      (`reason=store`) to exit `6`.
    *(Added 2026-10-05, architecture, at §8: the owned-Zone question in the implementation's requests.)*

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package store   // outside sim; uses sim.WorldStore and sim.Engine

type Round struct {
    Tick         sim.Tick
    StateVersion uint32
    Zones        []ZoneSnapshotRef       // one per owned Zone; sorted
    Complete     bool                    // exactly one hash-valid object per owned Zone; PRNG, EventID, seed agree
}

// ListRounds groups WorldStore keys by tick and marks completeness against each
// round's own owned Zones (AC-16: the Zones of the content the round records,
// resolved through a function the caller supplies, so `store` doesn't import
// `sim.ContentSource`; resolved once per versions set). Discovery lists the
// current content's Zones. Newest first.
//
// The grouping is a prefix scan: the key is
// {zone_id}/{tick}/{state_version}/{offset} (AW-SRV-006, amended 2026-09-22),
// so one Zone's part of a round is the prefix {zone_id}/{tick}/ and a round
// needs no Get per candidate to discover it. Tick precedes state_version so
// that lexical order is tick order across a rollback, which writes newer
// ticks at an older version. Completeness is still a per-object hash check, so
// verifying a round does read every object it names — discovery is what the
// key format makes cheap, not verification.
func ListRounds(ctx context.Context, ws sim.WorldStore, listed []sim.ZoneID, zonesAt ZonesAt) ([]Round, error)
// `NewestComplete` and `RoundAt` take the same two arguments in place of `owned`;
// `StateVersionOf` takes `listed` alone. `recovery.Options.Owned` becomes `Listed` plus `ZonesAt`.

// ZonesAt resolves a round's recorded content (pack ID to version) to its Zones. The server builds
// it on `Options.Prepare`, taking the Zone IDs from the Topology's World, and caches it per versions
// set. A `ZonesAt` error that wraps `sim.ErrContentVersionUnknown` is cause `content`; any other error propagates unchanged and exits `1`, `reason=store`. Nil or empty versions resolve to the current content
// and are not cause `content`.
type ZonesAt func(versions map[string]uint64) ([]sim.ZoneID, error)

// Recover is the whole boot-time path. It returns a ready Engine or a typed error.
func Recover(ctx context.Context, opts RecoverOptions) (*sim.Engine, Report, error)

type RecoverOptions struct {
    Round   *sim.Tick   // nil: newest complete
    Verify  bool        // true: compare and return; never serve
}
type Report struct {
    Phases   map[string]time.Duration   // load, seek, replay, verify
    Round    Round
    Replayed uint64                     // ticks
    Match    bool
}
```

### Sequence

```
select round ─▶ load Zones ─▶ migrate state_version ─▶ seek Partitions ─▶ replay boundaries ─▶ verify ─▶ ready
     │              │                │                       │                  │               │
  none: offset 0  ErrRound       ErrStateVersion         ErrLogGap        ErrOffsetGap    ErrHashMismatch
                  ErrRestoreMismatch, ErrSeedMismatch (AW-SRV-043, at the round's tick)
```

**Seek (added 2026-10-02).** The projector's `BoundaryReader.SeekAfter` (`AW-SRV-019`) moves to a
package both share (`server/tickloop`), unchanged in behavior. Recovery positions the boundary
reader at the round's own tick (`SeekAfter(round − 1)`), reads that `TickCompleted` as
`AW-SRV-043`'s `RecordedHash`, and replays from tick + 1. `Recover` streams boundaries as it
replays and never loads the topic. That settles the open question inherited from PR #32. A
cold start with no round reads from offset 0, as AC-9 says.

Ready means: hash verified, the loop's schedule lag (`andara_simulation_lag_seconds`) under
`sim.tick_budget_ms × 10`, and the first live tick completed. *(Amended 2026-10-05: it said "consumer
lag", which is per-Partition offsets and has no duration. The schedule is anchored at the loop's start, so
after a long recovery this checks that the first ticks keep pace; it doesn't say the commands backlog is
drained. Commands accepted before the crash are applied in order at `max_per_tick`, visible on
`andara_tick_deferred_records`, and are delayed, not lost.)* `/readyz` (`AW-INF-003`) reads this flag; nothing else sets it.

### Exit codes

| Code | Condition |
|-----:|-----------|
| `0` | recovered and serving, or `--verify` matched |
| `1` | configuration or store error before recovery began, including a content-source failure that isn't `ErrContentVersionUnknown` (AC-16) |
| `2` | *never assigned:* Go's own exit for an unrecovered panic or a runtime fatal error |
| `3` | `ErrLogGap` — retention shorter than the snapshot age |
| `4` | `ErrStateVersion` — binary older than the snapshot |
| `5` | *not recovery's:* `AW-SRV-026`'s `ExitBoundaryLost`, a running server that lost a Tick Boundary Record |
| `6` | `ErrRestoreMismatch` or `ErrSeedMismatch` (`AW-SRV-043`): the round doesn't reproduce its own tick |
| `7` | `ErrRoundIncomplete`: no complete round with `recovery.require_snapshot=true`, or a named round that isn't complete, except cause `content`, which is `6` (AC-16) *(was `5` until 2026-10-02)* |
| `8` | `ErrHashMismatch` — the alerting condition *(was `2` until 2026-10-02)* |

Exit `6` also covers the round refusing to restore onto the content in effect:
`sim.ContentDigestError`, a round carrying a Zone its own content doesn't list (`sim.RestoreEngine`), or
a round whose recorded content names a pack version the source doesn't have (AC-16).
That's the inherited `AW-SRV-012` line's "halts like a State Hash mismatch", and it logs
`reason=content`, counted under `andara_recovery_failures_total{reason="restore"}`.
`andara_restore_total` keeps `AW-SRV-043`'s three outcomes and doesn't count it.

A round holding one Zone twice is not a content disagreement. It's a malformed round, so `ListRounds`
marks it `incomplete`, as AC-4 does for a missing Zone, and recovery never selects it on its own.

**A named round that isn't complete** (a duplicate Zone, a missing or hash-invalid Zone, or
disagreeing PRNG, EventID or seed) is refused as `ErrRoundIncomplete`. It's never restored, and no
other round is substituted for it. A round is named in three ways:
- `recover --verify --round T`, the one-shot, exits `7`;
- `recovery.pin_round` (`AW-INF-007`'s one-boot override for `make rollback ROUND=T`) makes boot
  recovery exit `7`;
- `snapshot verify --round T` gets `FAILED_PRECONDITION` from `Admin.VerifySnapshotRound`, and
  `andara-cli` exits `1`.

Each server exit counts `andara_recovery_failures_total{reason="round"}`, with one `error` line
naming the round's tick and why it's incomplete. *(Amended in PR #356 review, 2026-10-03. The
earlier text said "named with `--round` … boot recovery exits `1`". Boot recovery takes no
`--round`, and the one-shot's exit was unstated.)*

Exits `8` and `6` set `andara_recovery_state_hash_match` to `0` and linger under
`recovery.mismatch_linger` (AC-14). A one-shot `recover --verify` exits `8` for every mismatch, as
its output is `match` or `mismatch`, and it prints which with `reason`.

**References to "exit `2`" elsewhere.** The SRE amendment below, the runbooks, and the feedback file
were written before the renumbering. Read their recovery "exit `2`" as `8`.

### CLI

| Command | RPC | Output |
|---------|-----|--------|
| `andara-server recover --verify [--round T]` | — | `match`/`mismatch`, both hashes, phase timings; exit per table |
| `andara-cli snapshot list [--zone Z] [--local]` | `Admin.ListSnapshotRounds`, or none with `--local` | table: tick, state_version, zones, complete, age |
| `andara-cli snapshot verify --round T` | `Admin.VerifySnapshotRound` | `match`/`mismatch` with `outcome`, the compared tick and both hashes (or seeds); runs server-side against a scratch Engine, never the live one. Exits per `AW-CLI-001`: `0` match; `1` a mismatch or a server refusal (`NOT_FOUND`, `FAILED_PRECONDITION`), told apart by `outcome` on stdout for a mismatch and by the error envelope's `code` for a refusal; `3` connect; `4` timeout, including `DEADLINE_EXCEEDED` |

Both Admin RPCs are OPERATOR only. **Pinned 2026-10-02** in
`docs/specs/protocol/andara/admin/v1/admin.proto`. The pinned form adds `VerifyOutcome`,
`compared_tick`, and the two seeds to the sketch below, so that `AW-SRV-043`'s refusals are
distinguishable, and it states the gRPC status for each refusal. The file wins where it and the
sketch differ.

**`--local` reads the configured store directly and issues no RPC. Added 2026-09-22, from
`AW-SRV-006`'s implementation (feedback §9).** That story's test plan called `andara-cli snapshot
list` before this story's `Admin` RPC existed, and it was implemented against the store rather than by
defining this story's contract from the implementation lane — which was the right call. The two
readings are not redundant and both are worth keeping: the `Admin` path answers *what does the running
server see*, `--local` answers *what is actually in the bucket*. A runbook wants the second precisely
when the first disagrees with it, or when no server is running — which is the state a recovery starts
from. One command with a flag rather than two command names, so an operator learns one.

```protobuf
// CONTRACT SKETCH — added to andara/admin/v1/admin.proto
rpc ListSnapshotRounds(ListSnapshotRoundsRequest) returns (ListSnapshotRoundsResponse);
rpc VerifySnapshotRound(VerifySnapshotRoundRequest) returns (VerifySnapshotRoundResponse);
message ListSnapshotRoundsRequest { string zone_id = 1; uint32 limit = 2; }
message SnapshotRound { uint64 tick = 1; uint32 state_version = 2; repeated string zone_ids = 3; bool complete = 4; int64 taken_at_unix_nano = 5; }
message ListSnapshotRoundsResponse { repeated SnapshotRound rounds = 1; }
message VerifySnapshotRoundRequest { uint64 tick = 1; }
message VerifySnapshotRoundResponse { bool match = 1; bytes expected_hash = 2; bytes actual_hash = 3; }
```

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `recovery.require_snapshot` | `ANDARA_RECOVERY_REQUIRE_SNAPSHOT` | `false` | `true` in prod once M2 lands: a cold replay is a retention bug, not a boot |
| `recovery.replay_batch` | `ANDARA_RECOVERY_REPLAY_BATCH` | `4096` | fetch size during replay; AC-3 proves it does not matter |
| `recovery.verify_timeout` | `ANDARA_RECOVERY_VERIFY_TIMEOUT` | `600s` | bounds `--verify` |
| `recovery.pin_round` | `ANDARA_RECOVERY_PIN_ROUND` | `0` | `0` means unset: recover from the newest complete round. `T > 0` names round `T` (AC-15). Read on every boot; the server never clears it. Setting and clearing it is `AW-INF-007`'s `make rollback`. Already declared in the chart's `keys.yaml` as `int`, default `0`, min `0` *(contract stated here in PR #356's review, 2026-10-03)* |
| `recovery.mismatch_linger` | `ANDARA_RECOVERY_MISMATCH_LINGER` | `0s` | AC-14. Boot recovery only. Compose sets `60s`; the chart leaves `0s`, where it would only slow `AndaraServerCrashLooping` *(added 2026-10-02)* |

### Error taxonomy

`ErrHashMismatch{Tick, Expected, Actual, Round}`, `ErrRestoreMismatch` and `ErrSeedMismatch`
(`AW-SRV-043`), `sim.ContentDigestError` (exists), `sim.ErrRoundZoneUnknown{Tick, Zone}` and
`sim.ErrRoundZoneDuplicate{Tick, Zone}` (new typed errors for `RestoreEngine`'s two untyped
round-Zone refusals, added by this story in `server/sim/restore.go`; `ErrRoundZoneUnknown` exits
`6` with `reason=content`, and `ErrRoundZoneDuplicate` is the incomplete round above, exit `7` with
`cause=duplicate`, a backstop
behind `ListRounds`), `ErrLogGap{Partition, Need, Have}`,
`ErrStateVersion` (from `AW-SRV-006`), `sim.ErrRoundIncomplete{Tick, Cause, Zones}` (`AW-SRV-006`'s `{Tick, Missing}`, widened in PR
#356's review so that it names AC-15's causes and the Zones involved; for `disagree`, AC-11's two
values ride on the log line; the snapshot writer in `tickloop` and `store.ListRounds`' existing
callers set `cause=missing` where they set `Missing` today), `ErrOffsetGap` (from
`AW-SRV-002`, re-raised during replay). `Admin.VerifySnapshotRound` runs one verify at a time and refuses
a second with `FAILED_PRECONDITION`, ErrorInfo reason `verify_busy` (a reason inside the pinned
`admin.proto`'s statuses; it doesn't queue, ruled 2026-10-05); `andara-cli snapshot verify` exits `1`.

## Data / state impact

No new persisted state. Consumer group offsets are **not** trusted at boot: recovery seeks explicitly
to the round's offsets and re-applies the window, which `AW-SRV-002` AC-10 makes idempotent. The
consumer group's committed offset is only a hint for lag metrics until the first live tick.

Log retention on `andara.commands.v1` and `andara.events.v1` must exceed the age of the oldest round an
operator might select, plus margin. `AW-INF-005` owns the number; AC-2 turns a wrong one into exit `3`
rather than a quietly wrong World.

## Observability requirements

### Metrics
- `andara_recovery_duration_seconds` — histogram, label `phase` (`load`, `seek`, `replay`, `verify`,
  `total`). Bounded enum.
- `andara_recovery_replayed_ticks` — gauge.
- `andara_recovery_state_hash_match` — gauge, 0 or 1. Set once per recovery.
- `andara_recovery_failures_total` — counter, label `reason` (`hash`, `restore`, `gap`, `version`,
  `round`, `store`). `restore` is exit `6` (added 2026-10-02).
- ~~`andara_acknowledged_commands_lost_total`~~ — **withdrawn 2026-10-02** (AC-8). A restarted
  process holds no acks to compare with the log, so it would read `0` by construction.
  `docs/specs/slo/recovery.md`'s SLI line names it, and SRE amends that SLO doc (feedback item 6).
- `andara_recovery_round_tick` — gauge, the round used.

### Logs
- `info` at start with round tick and offsets; `info` at ready with phase durations.
- `error` on every refusal, one line, with the fields the exit-code table names.
- Required fields: `ts`, `level`, `msg`, `service`, `env`, `tick`, `partition`, `trace_id`.

### Traces
- `recovery.run` root; children `recovery.load_snapshot` (per Zone, `zone_id`, `bytes`),
  `recovery.seek`, `recovery.replay` (`ticks`, `records`), `recovery.verify`.

### Alerts
- `RecoveryStateMismatch` on `andara_recovery_state_hash_match == 0`. Pages. Runbook
  `docs/runbooks/recovery-state-mismatch.md` ships here and its mitigation is
  `andara-cli snapshot list` → `andara-server recover --verify --round` → deploy pinned to that round
  via `AW-INF-007`.

### SRE amendment, 2026-10-02 (after `ready`; implementation hasn't started)

Recorded in `docs/feedback/AW-SRV-007-recovery-scale.md`, item 5.

- **`RecoveryStateMismatch` can't fire as written.** `andara_recovery_state_hash_match` is set to
  `0` by a process that exits `2` at once. The restart sets nothing until its own recovery ends,
  so no scrape ever reads the `0`, and the one alert that pages is dead configuration.
  - **Compose: a linger fixes it.** Today the server serves nothing until recovery has finished
    (`cmd/andara-server/main.go`: the tick loop starts before `ListenAndServe`). So on a boot
    recovery's exit `2`, the server **starts** serving for 60 s before it exits:
    - `/metrics` and `/livez` answer `200`;
    - `/readyz` and `/startedz` answer `503`;
    - one `error` line, `holding /metrics for the hash mismatch to be scraped`, with `for`.

    That's the projector's `haltLinger` (`cmd/andara-projector/main.go`). Compose scrapes its static
    target every 5 s whether or not the server is Ready.
  - **The gauge is never pre-seeded.** `andara_recovery_state_hash_match` has no sample until
    recovery sets it. If HTTP comes up earlier for any reason, a pre-seeded `0` would fire
    `RecoveryStateMismatch` on every normal boot.
    - The repo registers gauges at construction (`server/telemetry/telemetry.go`), and a
      registered `Gauge` exposes `0` at once. So this gauge is registered on first set.
    - SRE proposes one Test-plan line: `testutil.CollectAndCount(reg,
      "andara_recovery_state_hash_match")` is `0` before recovery sets it and `1` after. The Test
      plan is architecture's to amend.
  - **The linger is a config key, off by default.** `recovery.mismatch_linger`
    (`ANDARA_RECOVERY_MISMATCH_LINGER`) defaults to `0s`, and compose sets `60s`. It applies on
    exit `2`, and on `AW-SRV-043`'s restore-mismatch exit if architecture accepts the proposal
    below.
    - On the cluster the linger buys nothing (next bullet). There it would add 60 s to every
      crash-loop cycle and delay `AndaraServerCrashLooping`, which pages, so the chart leaves it
      at `0s`.
    - The key is a contract addition, for architecture to confirm or rule otherwise.
  - **AC-5 needs scoping.** AC-5 says the server "never accepts a connection" on exit `2`, and
    the linger serves operator HTTP. SRE proposes that architecture reword AC-5 to "never binds
    `grpc.listen`", AC-10's wording, and allow operator HTTP (`/metrics`, `/livez`, `/readyz`,
    `/startedz`) while `recovery.mismatch_linger` is above `0s`. Until it does, the two conflict
    under compose.
  - **The linger is for boot recovery only.** `andara-server recover --verify` (AC-10) also exits
    `2` on a mismatch. It's a one-shot that nothing scrapes, so it exits at once.
  - **The cluster: the linger doesn't reach it.** The annotation scrape keeps only Ready pods
    (`deploy/helm/andara/files/alerts.yaml`, `AndaraServerUnavailable`'s comment). A refused
    recovery was never Ready. So on `dev` and `prod` the gauge's `0` is never scraped, linger or
    not.
    - What pages there today is `AndaraServerUnavailable` (`for: 2m`): the refusal leaves no
      Ready server, and that's the player's symptom. `server-unavailable.md` sends exit `2` to
      `recovery-state-mismatch.md`.
    - `RecoveryStateMismatch` on the cluster needs a signal that outlives the process and doesn't
      depend on readiness. Two candidates: kube-state-metrics' last-terminated exit code for the
      `server` container, or a Loki rule on the `error` line.
    - That work is routed to PM in `docs/feedback/AW-INF-009-recovery-state-mismatch-cluster.md`.
      PM's decision (2026-10-02): `AW-INF-009` carries it in SPRINT-05, and architecture amends
      that story's contract. It doesn't hold this one.
  - **The rule.** It fires with `for: 0m` and carries `keep_firing_for: 15m`. *(Corrected 2026-10-05
    from SRE's note: compose does have `restart: on-failure` since `AW-SRV-026`, so a refused recovery loops:
    each cycle recovers, mismatches, lingers 60 s and exits, and the target is stale between lingers.
    `keep_firing_for` bridges those gaps and holds the page 15 minutes after the loop is stopped. The first
    version of this bullet said compose had no restart policy.)*
  - **§8.** `RecoveryStateMismatch` is observed in the local stack's Prometheus:
    - **firing**, against a compose server recovering from a deliberately corrupted round;
    - **never in `ALERTS`**, at neither `alertstate`, through a normal compose recovery
      (`make stack-recover`).

    The §8 record verifies the alert against compose. It names "fires on the cluster" as **not
    yet observed**, and `AW-INF-009` inherits that as a Definition-of-done line. That's PM's
    decision of 2026-10-02, under CLAUDE.md §8's deferral rule.
    - SRE's note: this rule's expression can't fire on the cluster, so `AW-INF-009` observes a new
      expression. Architecture confirms the framing (see the `AW-INF-009` feedback file).
    - **Ordering.** This story's §8 follows `AW-INF-032`, which depends on this story and supplies
      `make stack-recover`.
    - **The corrupt-round run has no target yet.** It's a §9 gap, and SRE adds a target for it
      (for example `make stack-recover CORRUPT=1`) in its §8 ops commit. Until then, no §8 record
      cites it.
  - **SRE's files.** The compose `60s`, the chart default and values schema, and the rule's
    `for`/`keep_firing_for` are in `deploy/`. SRE ships them in its ops commit at §8, as it did for
    `AW-SRV-019`. Implementation doesn't edit `deploy/`.
- **If `AW-SRV-043` joins `depends_on`:**
  - `andara_recovery_failures_total{reason}` gains `restore`, which covers its hash and seed
    mismatches.
  - SRE proposes that the restore-mismatch exit also sets `andara_recovery_state_hash_match` to
    `0` and lingers like exit `2`. A round that doesn't reproduce its own hash is a World that
    can't recover to the right state, and the operator's response is the same:
    `recovery-state-mismatch.md`, choose an older round. Without that, nothing alerts on it but
    `AndaraServerUnavailable`. It's architecture's to accept.
  - `recovery.run` gains the child `restore.verify`, after `recovery.load_snapshot`.
  - `andara_restore_total{caller="recovery"}` and `{caller="verify"}` are `AW-SRV-043`'s
    instruments, but this story wires and pre-seeds them, since it owns both callers.
    `Admin.VerifySnapshotRound` and the one-shot `andara-server recover --verify` both count as
    `verify`.
- **The local live observation is `AW-INF-032`'s.** *(Superseded 2026-10-02 by the Definition of
  done's first line, which decides the inheritance and drops the withdrawn counter.)*

## Test plan

- **Unit:** `ListRounds` grouping and completeness against fixtures with a missing Zone and a
  hash-invalid Zone; exit-code mapping per error, including `6` and `7`; `Report` phase accounting.
  `testutil.CollectAndCount(reg, "andara_recovery_state_hash_match")` is `0` before recovery sets
  the gauge and `1` after (SRE's line, added 2026-10-02). The linger (AC-14) on a stepped clock:
  the HTTP answers during it, the exit code after it, and a signal ending it.
- **Integration (CI, gates merges):** kill-and-recover against a throwaway Redpanda and the filesystem
  store — run the sizing fixture 90 s, `SIGKILL`, restart, assert AC-1; vary `recovery.replay_batch`
  across three values asserting AC-3; truncate the log below the round's offset asserting exit `3`;
  corrupt one Zone object asserting AC-4; flip one byte in `prng_state` on every object of a round
  and re-sign each envelope, asserting exit `6` (AC-13); rewrite one tail `TickCompleted.state_hash`
  after the round, asserting exit `8` (AC-5) with the error line's `tick` naming that tick, and, in
  a separate case, `snapshot verify`'s `compared_tick` naming it; a round with one Zone twice,
  listed `incomplete`; that round and a hash-invalid one each named with `recover --verify --round`
  and with `recovery.pin_round`, asserting exit `7`, `failures_total{reason="round"}` at `1`, its
  `cause`, and no `recovery.load_snapshot` span for any other tick, then the same through
  `Admin.VerifySnapshotRound`, asserting `FAILED_PRECONDITION` (AC-15); `--round` and `pin_round`
  at a tick with no objects, asserting exit `7`, `cause=missing` with every owned Zone, and
  `NOT_FOUND` from the RPC; AC-11's disagreeing round named with `--round`, asserting
  `cause=disagree`; a newer-`state_version` round named with `--round`, asserting exit `4`; a tick
  holding a partial group at `state_version` v+1 and a complete one at v, named with `--round`,
  asserting the v round loads; a round restored
  onto content with a different `content_digest`, asserting exit `6` with `reason=content` (AC-13)
  and `snapshot verify` returning `VERIFY_OUTCOME_CONTENT_MISMATCH`; no-snapshot cold start
  asserting AC-9. Publish
  `recovery-timing.json` (AC-7). The AC-7 run kills just before the next round would complete, or
  sets a longer `snapshot.interval` on the fixture, so that the tail is 600 ticks. The assertion
  reads `tail_ticks` from the JSON and fails below 600.
  *(Amended 2026-10-02. A `prng_state` flip is caught at the round's tick now, by `AW-SRV-043`, so
  it can't reach exit `8`.)* Its clients record their acks
  and check them after recovery (AC-8). A re-signed corrupt Zone body asserts exit `6` (AC-13). The
  24 h history run (AC-12) is skipped unless its variable is set, as `AW-SRV-019`'s AC-6 run is,
  and its result is recorded in the implementation record. Every mismatch exit test dials
  `grpc.listen` while the process runs and is refused (AC-5).
- **AC-16 (added 2026-10-05):** *Unit* (`server/store`): a round whose content lists a Zone with no
  object is `missing`; an object for a listed Zone V doesn't list is cause `content`, `zone_id` in `Reason`;
  content that names a pack version the source doesn't have is cause `content` with the versions in `Reason`, and a `ZonesAt` error that doesn't wrap `ErrContentVersionUnknown` is not (exit `1`); a tick with no object names
  the current content's Zones (`snapshotadmin.go`'s `verifyResponse` takes `len(Listed)` for its `owned`
  argument, and its `NOT_FOUND` check relies on that full list); `Classify` maps `ErrRoundContent` to `6`;
  `RoundAt` and `NewestComplete` each return the typed error for cause `content` and never
  `ErrRoundIncomplete`. The recovery log line for each is `recovery restore mismatch` with `zone_id` or
  `pack_versions`, and `andara_recovery_state_hash_match` reads `0`. *Integration:* write a round at V, swap to V+1 with an added Zone,
  kill, recover: the round is selected and the swap replays; a round whose pack version is gone from the
  content source exits `6` with `pack_versions`, and `snapshot list` shows it `complete=false`; a source that
  fails with `ErrNoContentSource` or an I/O error exits `1`, not `6`, and leaves the gauge unset. With no Zones in the current content, recovery replays the log, `require_snapshot` exits `7`, and `snapshot list` shows no rounds (`server/store`).
- **Manual/operator:** `make stack-recover` (`AW-INF-032`), which kills the compose server mid-play
  and asserts the M2 gate. Then, on the recovered stack:
  ```
  andara-cli snapshot list                         # expect: rounds newest first, complete=true
  andara-server recover --verify --round <tick>    # expect: match, exit 0
  ```
  *(Replaced 2026-10-02: the hand-typed `andara-server &` and `kill -9 %1` were a §9 defect.)*

## Definition of done

CLAUDE.md §8, plus:
- **The recovery test includes a handoff in flight** (moved here from `AW-SRV-028`'s Definition of done, since
  this story depends on it): a `Transit` record at the recovered hash, retried on the first live tick, with the
  Character arriving exactly once.
- **The live observation is `AW-INF-032`'s run (added 2026-10-02, feedback item 4).** This story's
  §8 record cites `make stack-recover`'s reads of `andara_recovery_state_hash_match`,
  `andara_recovery_duration_seconds{phase}`, `andara_recovery_round_tick`,
  `andara_restore_total{caller="recovery"}`, and the `recovery.run` trace resolved in Tempo. So
  this story's §8 follows `AW-INF-032`'s merge. `RecoveryStateMismatch` is verified against compose
  only: firing on a corrupt round, and never in `ALERTS` through a normal recovery. "Fires on the
  cluster" is not yet observed, and `AW-INF-009` inherits it.
- The kill-and-recover test gates merges on every `server/sim` and `server/store` change.
- `docs/runbooks/recovery-state-mismatch.md` exists and resolves its alert with `andara-cli` commands
  only.
- `recovery-timing.json` is a CI artifact and its `replay` phase is compared against the previous
  run in the job summary.
- **Inherited from `AW-SRV-043`'s §8 review (2026-10-04):** `andara_restore_total{caller="verify"}`
  is observed live. This story's §8 shows it at `ok` 1 from the running server's scrape after one
  `snapshot verify --round T` against the local stack's real round (`Admin.VerifySnapshotRound`), and
  `recover --verify`'s outcome read on the in-process registry, since a one-shot exits before a scrape.
  Both server callers are wired here, and only `caller="recovery"` had a line.
- **Inherited from `AW-SRV-006`'s §8 pass (2026-09-24):** this story is the first to drive a real
  snapshot failure through the server. Its §8 shows, from the running server's own registry,
  `andara_snapshot_failures_total{reason="encode"|"timeout"|"stall"}` moving, and
  `{reason="boundary"}` once `AW-SRV-006` AC-8 lands. `store` and `rounds_total{incomplete}` were
  observed at 006's pass. *(2026-09-26, 006's second §8 pass: `timeout` was observed from the
  compose server, on a round whose boundary ack didn't arrive while the broker was paused. It's
  off this list. `encode`, `stall` and `boundary` remain.)*
- **Inherited from `AW-SRV-012` (2026-09-25, architecture):** restore from a snapshot resolves
  `SnapshotEnvelope.content` (the per-pack versions in effect at its tick), rebuilds the topology,
  and checks `content_digest` *before* loading any Zone body. A mismatch halts recovery like a
  State Hash mismatch. Recovery never re-derives content from the Active Pointers.
- **Inherited from `AW-SRV-015` (2026-09-26, architecture's contract review; was its AC-9):**
  **given** a full restart completing within RTO **when** clients reconnect **then** every
  Character that was playing rebinds and none despawns, exercised in CI on top of this story's
  kill-and-recover test with 50 Sessions. Also: a Character linkdead at the kill recovers from a
  *snapshot* with its four linkdead fields as they were (015 AC-6 proves full-log replay only), and
  a body the crash left present with no Session is marked linkdead at recovery rather than left
  present forever (the boot sweep, over `Entities`; a Character in a `Transit` record at the kill lands
  unmarked until the story requested in `docs/feedback/AW-SRV-007-transit-orphan-mark.md` is built). `AW-SRV-015` joined `depends_on` with this line: the linkdead state and the
  reconnect it exercises are 015's.

## Open questions

- **`ListRounds` and the load-Zones phase land in `AW-SRV-019` (2026-09-24, Brian's call).** The state
  projector bootstraps from the newest complete round and cannot wait for this story's dependencies. It
  implements `store.ListRounds` and `store.Round` exactly as this contract states them, plus loading a
  round into a `sim.Engine`. This story reuses both unchanged and keeps the rest of its sequence. If
  implementing either turns up a defect in the sketch above, the fix is an amendment here, not a
  divergence there.

- **Resolved 2026-10-02 (architecture), feedback items 1 and 2.** The 120 s is measured at the
  sizing fixture's 25,000 Entities (AC-7), and recovery takes the seek (AC-12, "Seek" above).
  **Not resolved here:** at about 65 ms per tick, a 600-tick tail replays in about 39 s. That fits
  M2's 120 s. It doesn't leave room for Phase 1's 60 s once restart is counted. That's a replay-cost
  question for the story that lowers the RTO, sent to PM in the feedback file.
- **Inherited from the review of PR #32 (2026-09-19), measured by implementation:** `tickloop.Recover`
  reads every `TickCompleted` on the topic into memory before replaying — on a local log of ~180k
  ticks, RSS sat at ~4 GB for the first 40 s after boot before settling at 270 MB. Recovery memory is
  linear in log length until a snapshot bounds the replay window. This story's contract must have
  `Recover` stream boundaries from the round's tick forward rather than load the topic, and its
  RTO measurement must state peak RSS alongside seconds.

- **Inherited from `AW-SRV-002` (2026-09-18):** `tickloop.Recover` already replays recorded
  boundaries at boot and refuses a missing one as `ErrBoundaryGap` — a tick applied but never
  published. Once one boundary is lost the process publishes no more and keeps ticking; the next
  restart recovers to the last delivered boundary. **Decided 2026-09-18 (Brian): exit into exact
  recovery** — `AW-SRV-026` wires it (exit `5`), and this story inherits a process that never runs
  past a lost boundary; `ErrBoundaryGap` joins `ErrLogGap` and `ErrOffsetGap` here as the backstop.
  Zone faults quarantine the Zone, not the Partition (`AW-SRV-027`); whether a panic should instead
  crash-and-recover, now that recovery is exact, is still this story's to weigh.

- **Resolved 2026-09-11 (Brian): refuse on hash mismatch.** No automatic search for an older round.
- **RTO is decided**: 120 s p99 at M2, 60 s p99 at Phase 1 exit (`docs/specs/slo/recovery.md`). The
  dominant terms are Kubernetes detection and pod startup; optimizing this means `AW-INF-003`'s probes
  and image pre-pull, not a faster simulation.
- **`linkdead_grace > RTO` is a standing invariant** (ADR-0006), asserted at startup by `AW-SRV-015`.
- `[NEEDS BRIAN]` What players see during recovery — carried by `AW-INF-007`. Does not affect this
  contract: recovery is silent on the wire.
- `[ASSUMPTION]` `recovery.require_snapshot` defaults `false` so M1 keeps booting; `AW-INF-003`'s prod
  values set it `true`.

- **Carried from `AW-SRV-026`'s §8 close (2026-10-03).** `make stack-boundary-lost` reads the log back
  by requiring a recovery that replayed the whole log (`ticks_replayed == tick`). Once this story
  recovers from a snapshot, that check fails loudly. This story replaces the read-back (and tells SRE
  in its feedback file), so `make stack-boundary-lost` keeps passing on a snapshot recovery.

## Verification record — 2026-10-04 (implementation; `review` until the §8 checklist passes)

Branch `impl/aw-srv-007-recovery`. Requests to SRE and the two questions for architecture are in
`docs/feedback/AW-SRV-007-implementation-requests.md`. Integration tests ran on the local compose Redpanda with
the filesystem store (`ANDARA_KAFKA_BROKERS=localhost:9092`, `-tags integration`).

| AC | Test | What it asserts |
|----|------|-----------------|
| 1 | `TestRecoveryAgainstTheBroker/AC-1` | A real `SIGKILL` of a process taking a round every 400 ms: recovery restores a round, replays a tail, and its State Hash and tick are the last surviving boundary's |
| 2 | `…/AC-2` | Records deleted below a round's offset on one Partition: exit `3`, `LogGapError` naming the Partition, the round's offset and the log's earliest. The oldest round is used: the newest has consumed the whole log, and a log can't begin past its own end |
| 3 | `…/AC-3`; `TestReplayBatchDoesNotChangeTheHashes` | Batch 1, 7, 4096: the same hash at every replayed tick |
| 4 | `…/AC-4`; `TestANamedRoundThatIsNotCompleteIsExit7…` | A hash-invalid object: the newest round is incomplete, recovery uses the next; named, exit `7` `cause=hash` |
| 5 | `…/AC-5`; `TestAHashMismatchAtATailBoundaryIsExit8NamingTheTick`; `TestStartTickLoop_ARewrittenBoundaryIsExit8WithTheGaugeAtZero` | A republished events topic with one tail hash flipped: exit `8` at that tick and the round used, gauge `0`, `failures{hash}` `1`. Through boot: the refusal is a `*recovery.Failure` that lingers, and nothing past the tick loop is built, so `grpc.listen` is never bound |
| 6 | the integration tests above | They run under `make test-integration`; **the CI job that gates `server/sim` and `server/store` changes is SRE's (feedback)** |
| 7 | `TestRecoveryTimingAtSizingScale` | 25,000 Entities, 2,000 Rooms, 16 Zones, 500 Characters, a round at tick 777 and a tail of 602 ticks: `total` 46.3 s (`load` 0.24, `seek` 0.05, `replay` 45.98, `verify` 0.07), peak RSS 97 MB (the test process, which also read the boundaries). Writes `recovery-timing.json` |
| 8 | `…/AC-1` | Every acknowledged Command's `(partition, offset)` is below the replayed head of its Partition, and the record there is byte-equal to the one acknowledged. The test waits for every ack to be applied before the kill |
| 9 | `…/AC-9`; `TestColdStartReplaysFromZero`; `TestRequireSnapshotRefusesAColdStartWithExit7` | No round: replay from offset zero to the same hash; with `require_snapshot`, exit `7` |
| 10 | `andara-server recover --verify [--round T]` (`cmd/andara-server/recover.go`); `TestRecover_RefusesWhatItCannotRun`, `TestPrintVerify_…` | Flags, output and exit mapping (a restore mismatch is a `mismatch` and exits `8`). **Not run end to end here**: it needs a round a real server wrote, which `AW-INF-032`'s stack run provides |
| 11 | `store` tests (earlier commits), `TestRoundAt…`; `…/AC-13` | A round whose objects disagree is `incomplete` with `cause=disagree` |
| 12 | `TestSeekIsBoundedByTheRoundNotTheHistory` | 864,000 ticks of history, round at tick 864,000, 10-tick tail, the sizing fixture, on the local compose Redpanda: with no history `load+seek+replay` 1,035 ms, with history 1,304 ms (+269 ms, under the 1 s bound; `seek` 5 ms → 279 ms); peak RSS 94.0 MB vs 92.4 MB (-2%). The test found two real costs, fixed here in `tickloop/reader.go`: the reader's client began prefetching the history from offset 0 while the search was still running, and a client was built per probe |
| 13 | `…/AC-13`; `TestARestoreThatDoesNotReproduceItsTickIsExit6` | Every object of the newest round with a flipped, re-signed `prng_state`: exit `6`, nothing replayed, no other round tried, gauge `0`, `failures{restore}` `1`, `restore_total{recovery,hash_mismatch}` `1` |
| 14 | `TestHoldMismatch_ServesOperatorHTTPThenReturns` | `/metrics` and `/livez` `200`, `/readyz` and `/startedz` `503`, the clock ends it, a signal ends it at once; `Lingers` is true for `8` and `6` only |
| 15 | `store` `RoundAt` tests; `TestVerifyResponse_RefusalsNameTheirStatus`; `TestSnapshotAdmin_IsOperatorOnly`; `TestSnapshotVerify_MatchAndMismatchExits` | A named incomplete round is exit `7` and nothing else is tried; the RPC maps no-object to `NOT_FOUND` and an incomplete round to `FAILED_PRECONDITION`; the CLI exits `1` |

**Definition of done.** `TestRecoveryWithAHandoffInFlight`: a `Transit` record at the recovered hash from a round,
retried by a second process on its first live ticks, alice arriving exactly once at sequence 1.
`TestFiftyCharactersSurviveAKill` (AW-SRV-015): 50 Characters recover from a round, the one linkdead at the kill keeps its
four fields, the other 49 are found by `PresentCharacters`, and after their marks and 50 reconnects every body is present and
none despawned. `Roster.MarkOrphans` produces those marks at boot (`TestRoster_MarkOrphans…`). The `Engine` was driven
in-process for the marks and reconnects; the Gateway path is `AW-SRV-014`'s tests.

**Mutation checks**, each in a scratch worktree: restore exit `6`→`8`; a named round falling back to the newest; the
hash-match gauge registered at construction; replay chasing a moving log; a restore not counted under `caller=verify`; a
mismatch reporting no round; a restore mismatch not setting the gauge; exit `7` setting it; readiness ignoring the first
live tick, and the lag; exit `7` lingering; the linger ignoring a signal; a verify using the live instruments; no-object
being `FAILED_PRECONDITION`; `PresentCharacters` including linkdead bodies, or non-Characters; `MarkOrphans` in the wrong
Zone, and unbinding with a grace; the reader not positioning at the start; the last replayed tick dropped (AC-1). Three
survived at first (`caller=verify`, scratch options, reader positioning) and now fail a test.

**Beyond the contract's surface.** `tickloop.BoundaryReader.HeadTick`, `Loop.Live`, `Engine.PresentCharacters`,
`Roster.MarkOrphans`, `store.Round.TakenAt`, the gateway's `SnapshotAdmin` seam, and `snapshot list` over RPC with
`--local` for the old per-Zone object listing (existing tests now pass `--local`). Replay ends at the log's head as it is when
replay begins: a log still being written (a verify against a running server) has an end that moves.

**Deviations and questions for architecture:** the owned-Zone set is the loaded content's Zones; "consumer lag under ten
tick budgets" is read as the loop's schedule lag; and recovery now marks Characters a crash left standing linkdead.

**Architecture's rulings on the questions above (2026-10-05)** are in
`docs/feedback/AW-SRV-007-implementation-requests.md`. One changes the contract and needs implementation:
AC-16 (the owned set comes from the round's own content). The "schedule lag" reading and `verify_busy`
stand as built. The Character in transit at the kill (briefly AC-17) is moved out to a story for PM.

**Outstanding before `done`:**
- **AC-16**, new on 2026-10-05; the owned set is the loaded content's Zones today (not the round's).
- SRE: the Helm key `recovery.mismatch_linger` (PR open on main) and the regenerated values schema, the `stack-boundary-lost`
  read-back (`round_tick + ticks_replayed == tick`), compose's `60s`, the `RecoveryStateMismatch` rule, the runbook,
  and the CI job. **`make check` fails only at `values-schema-check` until that key merges.**
- `AW-INF-032`'s live observation and `recover --verify` / `snapshot verify` against a real round, so
  `andara_restore_total{caller="verify"}` is observed live (the inherited AW-SRV-043 line).
- Not observed: the inherited `AW-SRV-006` failure reasons (`encode`, `stall`, `boundary`) driven through the server.

## §8 instrumentation check — 2026-10-05 (SRE, `sre/aw-srv-007-verify`)

Against the local stack on `main` at `3dedb00` (`make up` rebuilt the server from that tree), and the `stack`
workflow on #421 (run 37318364410, the re-run that passed, and 37315058628 before it). The live observation is
`AW-INF-032`'s `make stack-recover`, as the Definition of done says. Every number below was read from the running
server's `/metrics`, Prometheus, Tempo or Loki, not from the test registry, except where a row says so.

### Metrics

| Series | Observed | Where |
|---|---|---|
| `andara_recovery_state_hash_match` | `1` after a normal recovery; `0` during a refused recovery's linger, and absent before either | `make stack-recover`, `make stack-recover-mismatch`; Prometheus reads the `1` too |
| `andara_recovery_round_tick` | `3270926`, equal to the round `R` the script waited for (`3.270926e+06` on the wire; the script compares integers) | `make stack-recover` (asserts it) |
| `andara_recovery_replayed_ticks` | `45` | same run |
| `andara_recovery_duration_seconds{phase}` | all five phases `count` 1: `load` 0.822 s, `seek` 0.018, `replay` 0.012, `verify` 0.0001, `total` 0.852 | same run |
| `andara_recovery_failures_total{reason="restore"}` | `1` during the refusal's linger (exit `6`, `reason=seed`) | `make stack-recover-mismatch` now asserts it |
| `andara_recovery_failures_total{reason}` for `hash`, `gap`, `round`, `store`, `version` | exposed at `0`, **not driven on a running server** | see "Not observed" |
| `andara_restore_total{caller="recovery",outcome="ok"}` | `1` | `make stack-recover` (asserts at least 1) |
| `andara_restore_total{caller="verify",outcome="ok"}` | `0` before, `1` after one `andara-cli snapshot verify --round 3271572` (`outcome=match`, `compared_tick=3271915`) | the inherited `AW-SRV-043` line |

`recover --verify`'s outcome is on the in-process registry only (a one-shot exits before a scrape), as the
inherited line allows; implementation's tests read it there.

### Logs, read back from Loki (`{service_name="andara-server"}`)

- `recovery starting from a snapshot round` (`info`): `tick`, `trace_id`, `zones`, `offsets`.
- `recovery complete` (`info`): `tick`, `round_tick`, `replayed_ticks`, `load_ms`, `seek_ms`, `replay_ms`,
  `verify_ms`, `total_ms`, `state_hash`, `trace_id`.
- `recovered from the log` (`info`): `tick`, `round_tick`, `ticks_replayed`.
- `recovery restore mismatch` (`error`, one line): `round_tick`, `reason`, `trace_id`, and for `seed` the
  `recorded_seed` and `configured_seed`. `holding /metrics for the hash mismatch to be scraped` (`error`): `for`.
  **Not read back from Loki:** `recovery refused` (the `error` line for exits `3`, `4` and `7`), since no run here
  refused that way; its fields are `server/recovery/logs.go`'s and the integration suite's.
- **Deviations from the required-fields list** (`ts`, `level`, `msg`, `service`, `env`, `tick`, `partition`,
  `trace_id`): `partition` is on none of them (a recovery spans every Partition, and `offsets` carries them);
  `recovered from the log` has no `trace_id`. For architecture to amend the list or implementation to add them.
- **A uint64 above 2^63 reaches Loki rounded** (a seed reads `16406829232824263000` in Loki and
  `16406829232824261652` on stderr): issue #423. The runbook now says to read `recorded_seed` from the pod's own log.

### Traces, resolved in Tempo by the printed `trace_id` (`208bce564d3327bc4ea4a4a00bd1e40e`)

`recovery.run` (root) with children `recovery.load_snapshot` ×4 (`zone_id`, and `key`), `recovery.seek`,
`restore.verify` (`round_tick`, `outcome=ok`), `recovery.replay` (`ticks` 45, `records` 2) and `recovery.verify`.
**Deviation:** `recovery.load_snapshot` carries `key`, not the `bytes` the §7 names (`server/recovery/recover.go:186`;
the README documents `zone_id` only). Either the contract drops `bytes` or implementation adds it.

### Alerts

- **`RecoveryStateMismatch` fired** on compose against a refused recovery (a seed mismatch, exit `6`: see
  `docs/feedback/AW-SRV-007-recovery-scale.md` for why not a byte-flipped round), and **outlived the process**: with
  the restart loop stopped, the target stale after 25 s and the alert still firing (`keep_firing_for`). Locally several
  times, and in CI (run 37318364410: `RecoveryStateMismatch — fired on a refused recovery, outlived the process,
  cleared by the right seed — passes`).
- **Absent from `ALERTS`, at either `alertstate`, through a normal recovery**, anchored on Prometheus scraping the
  recovered `1` (locally with the alert clear, and in CI: `RecoveryStateMismatch is absent from ALERTS through the
  recovery (Prometheus scraped the 1)`).
- **Not observed: firing on the cluster.** The expression can't fire there (Ready-only scrape). `AW-INF-009`
  inherits it, as the Definition of done says.
- The runbook exists, and its commands are `andara-cli` for listing (`--local`, since a refused recovery serves no
  Admin endpoint) and `andara-server recover --verify` for the one-shot. The Definition of done says "`andara-cli`
  commands only": `snapshot verify` is `andara-cli`, but it needs a serving server, so after a refusal the one-shot
  is the server binary. For architecture to reword the line or accept the one-shot.

### Definition of done, line by line

- **Live observation of `AW-INF-032`'s run:** done, above.
- **The kill-and-recover test gates merges:** it did not. `make test-integration` never listed `./server/recovery/`,
  so the package skipped in CI (`ANDARA_KAFKA_BROKERS` unset). Added in #421: the package's tests, 8.5 s against the stack's broker.
- **`recovery-timing.json` is a CI artifact, `replay` compared with the previous run:** the job exists
  (`.github/workflows/recovery-timing.yaml`, #421). Locally: `total` 46.79 s (bound 90 s), `replay` 46.48 s, tail 603
  ticks from round 781, peak RSS 93 MB, 157 s for the test. **In CI** (`recovery-timing` workflow, run
  37322596095 on `3dedb00`): passed, `total` 46.05 s, `replay` 45.75 s, tail 601 ticks from round 701, peak RSS 103 MB,
  the `recovery-timing` artifact uploaded, and the summary step ran ("no previous run", as the first run must). **Not
  observed: the comparison table.** Two later runs of the same commit (a dispatch and its re-run) failed in the test
  with `timed out after 8m0s waiting for a round, and a tail of 600 ticks past it`: 600 ticks cost about as much as the
  fixture's 60 s snapshot interval on a runner, so the tail often never builds (issue #424, implementation's test).
  The previous-run download did work on those runs (`previous run 37322596095`), so the comparison needs one passing
  run after the first. This line stays open until it has one.
- **Inherited from `AW-SRV-043`:** `caller="verify"` observed live, above.
- **Inherited from `AW-SRV-006`:** `andara_snapshot_failures_total{reason="encode"|"stall"|"boundary"}` are **not
  observed**. None was driven on a running server. `boundary` needs a failed boundary publish (a lost boundary also
  exits the server `5`, so `make stack-boundary-lost` restarts it and the counter starts again; whether the round in
  flight counted `boundary` before the exit wasn't looked at); `encode` needs an encoder fault; `stall` needs a copy over
  `max_stall_ms`, which the marks sizing measurement (`AW-INF-035`) is the first thing that could produce at scale.
  Architecture names where each is inherited.
- **Moved to its own story (architecture, §8 rulings):** the in-transit orphan mark.

### The implementation record's "Outstanding before `done`" list, 2026-10-05

- **SRE's items** (the Helm key and values schema, the `stack-boundary-lost` read-back, compose's `60s`, the rule, the
  runbook, the CI job): all merged (#415, #417, #418, #421). `make check` on `main` passes `values-schema-check`.
- **`AW-INF-032`'s live observation, and `snapshot verify` against a real round:** done, above.
- **`AW-SRV-006`'s failure reasons:** not observed, above.
- **AC-16** (the owned set is the loaded content's Zones, not the round's): not SRE's, and not checked here.

### Not observed on a running server

`andara_recovery_failures_total` for `hash`, `gap`, `round`, `store` and `version`: each needs a fault in the log or
the store, so they are exercised on the metric objects by the integration suite
(`TestExitCodeMapsEveryError` and the per-exit tests, which the §8 deferral rule allows), with the series exposed at
`0` by the scrape above. No story inherits a live observation: a recovery fault in the log or the store isn't a
deployment path. `AW-INF-009`'s dev run of exit `6` is the first on a cluster.
