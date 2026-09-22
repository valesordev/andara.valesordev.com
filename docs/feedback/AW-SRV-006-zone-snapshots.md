<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# Feedback — AW-SRV-006 Zone snapshots keyed to partition offsets

Implementation findings that deviate from, or decide something left open by, the story's
interface contract. Raised for architecture review; the implementation proceeds under the
assumptions recorded here so the branch is reviewable against a stated position rather than
against a guess.

Story: `docs/stories/AW-SRV-006-zone-snapshots.md` (`status: ready`, read 2026-09-22)

---

## 1. The `ZoneState` body sketch is stale: it predates AW-SRV-014 and AW-SRV-022

**Finding.** The contract sketch reserves `EntityState` fields 5–9 for "dormant,
dormant_since_tick (AW-SRV-014); linkdead_since_tick (AW-SRV-015); template, content_version
(AW-SRV-022)" and says "Each bumps state_version by one." AW-SRV-014 and AW-SRV-022 have both
merged. `sim.EntityState` (`server/sim/entity.go`) already carries `Template`,
`ContentVersion`, `Name`, `Dormant`, and `DormantSince`, and
`sim.EntityCanonicalBytes` already hashes every one of them. `Name` and
`ZoneState.Faulted`/`FaultedTick` appear nowhere in the sketch at all, and both are covered by
`WorldState.CanonicalBytes`.

**Why it matters.** A snapshot body that omits a field the State Hash covers cannot reproduce
that hash on restore. AW-SRV-007 AC-5 exits `2` on exactly that mismatch, so the omission would
surface as an unrecoverable World rather than as a decode error.

**Decision taken.** The body carries everything the Zone's canonical bytes cover, at
`state_version` 1:

- `EntityState` gains `name` and, for the faulted flag, `ZoneState` gains `faulted` and
  `faulted_tick`. Field numbers are assigned on this branch because the sketch did not assign
  them and the story is `ready`; they are the first thing to confirm at review.
- `state_version` stays `1`. Nothing is "bumped": no v1 snapshot has ever been written, so
  these fields are present from the first object rather than added to an existing format.
  `sim.StateVersion` is already `1` and its own doc comment says the version moves only when the
  *meaning* of state changes, "never for an additive field protobuf absorbs" — which contradicts
  the sketch's "each bumps state_version by one". Architecture's call which of the two rules
  stands; the code follows `sim.StateVersion`'s.
- `linkdead_deadline_tick` (sketch field 4) and `linkdead_since_tick` are **not** written:
  AW-SRV-015 has not landed and `sim.EntityState` has no such field. The numbers are left unused
  rather than assigned to something else.

**Resolution of the story's first Open question** (inherited from AW-SRV-003, whether the body
reuses `andara.log.v1.Entity`): it does not. `log.v1.Entity` is the Entity *in transit* — it
deliberately omits position, because an `Arrive` names the target Room — and a snapshot must
carry `room`. Extending `Entity` with snapshot-only fields would put state that means nothing
on the wire into a record the cross-Zone path encodes on every handoff. `andara.state.v1.EntityState`
is a separate message, and a round-trip test covers it against `sim.EntityState` the way the
Open question asks.

## 2. `state_hash` is per-Zone (AC-3), and is derived from the existing world encoding

**Finding.** `sim.WorldState.Hash()` is global. AC-3 requires "a `state_hash` equal to the hash
the sim would compute for that Zone at that tick"; `snapshot.proto`'s comment on the same field
says "comparable to the TickCompleted record for the same tick", and `TickCompleted.state_hash`
is the global hash. The two readings conflict.

**Decision taken.** Per-Zone, per AC-3, and AW-SRV-007 AC-4 agrees: it lists "a round with a
missing or **hash-invalid** Zone object", which is a per-object check. AW-SRV-007 AC-5's
comparison against `TickCompleted` is a different assertion made after replay, on the
recovered World, and stays global.

To keep the two provably consistent rather than merely similar,
`WorldState.CanonicalBytes` is refactored to compose from a per-Zone
`zoneCanonicalBytes`, and the per-Zone hash is SHA-256 over that same section. No new encoding
is invented, and the global bytes are byte-for-byte what they were before this story — the
existing determinism tests hold unchanged.

**Suggested amendment.** `snapshot.proto`'s comment on `state_hash` should say per-Zone.

## 3. `ZoneState.deferred` (sketch field 5) is written but always empty

**Finding.** The sketch describes field 5 as "records deferred by `sim.max_per_tick`, in order".
The deferred backlog is not sim state: it lives in `tickloop.Source`'s per-Partition buffers
(`server/tickloop/seams.go`), and `Engine.SnapshotAll()` — which the contract says the loop calls
at a boundary and nowhere else — cannot see it.

**Why it is safe to leave empty.** `Engine.Step` advances `s.Offsets[p]` only past records it
actually applied (`server/sim/engine.go`), and `res.Unapplied` is handed back to
`Source.Requeue`. A deferred record therefore sits *at or after* the offset the envelope
records, so AW-SRV-007 replaying the log tail from the envelope's offsets reads it from Kafka in
its original order. Persisting it in the body would duplicate it, and a snapshot that replays a
Command twice is worse than one that replays it once.

**Decision taken.** The field is defined as specified, so the number is spent and the shape is
on record, and is never populated. If architecture wants the backlog checkpointed, it needs a
seam from the loop into `SnapshotAll` and a dedup rule in AW-SRV-007 — that is a contract change,
not an implementation detail.

## 4. `prng_state` and `next_event_id` are global, carried per-Zone

`sim.WorldState` holds one RNG and one `NextEventID` for the process, not one per Zone. The
sketch places both inside the per-Zone body, so every object in a round carries the same two
values. That is what is implemented — a round is one cut at one tick, so the copies agree by
construction, and a Zone restored alone still has the PRNG state replay needs.

It does mean AW-SRV-007 must decide which Zone's copy wins when a round is restored, and must
refuse a round whose copies disagree. Flagged for that story; this one writes them consistently.

## 5. The key has no tick, and an idle Zone repeats its key

`{zone_id}/{state_version}/{offset}` is offset-keyed. A Zone that received no Command since the
last round is at the same offset, so the next round writes the same key — an overwrite, which is
harmless (the write is atomic, so AC-5 holds) but means the object count is not the round count.

More consequentially for AW-SRV-007: **a key does not identify a round.** Grouping the Zones of
one round requires reading each envelope's `tick`, because a busy Zone and an idle one at the
same boundary have unrelated offsets. `WorldStore.List` returns keys only, so round selection is
a `List` plus one `Get` per candidate. That is still bounded and still beats a backwards log
scan, but it is not the one-call discovery the story's third Open question implies. Noted for
AW-SRV-007; no change made here.

## 6. The Definition-of-done line on a "real" `state_version` bump is inherited

DoD requires "`state_version` migration is tested across at least one real bump." There is no
real bump available: the fields AW-SRV-014 and AW-SRV-022 would have bumped are present in
version 1, per §1. The migration machinery, the registry-completeness check that fails
`make check` on a bump without a migration, and the synthetic 1→2→3-with-4-refused chain from
the test plan are all implemented and tested. The line is carried forward to the first story
that genuinely changes what state *means* — AW-SRV-015 is the likely candidate, since
`linkdead_since_tick` is listed in the sketch and is not in version 1.

## 7. The `s3` store adds a third-party dependency

`snapshot.store=s3` needs an S3 client; `go.mod` has none today. Recorded here because a new
direct dependency is a standing architectural commitment (`LICENSES/`, `REUSE.toml`, and
`make license-check` all take an entry) rather than a detail of this story, and because the
story's own MinIO assumption already defers a local-stack change to AW-INF-002.
