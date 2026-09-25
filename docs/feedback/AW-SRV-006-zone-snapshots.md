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

## 5. The key format could not satisfy AC-5 — resolved: the key now carries the tick

**Corrected 2026-09-22.** An earlier draft of this section called the overwrite below "harmless
(the write is atomic)". That was wrong, and the rest of this section replaces it. Raised on PR #46
by an automated reviewer; verified and reproduced here.

**Finding.** `{zone_id}/{state_version}/{offset}` keys an object by offset. A Zone that received no
Command since the last round is at the same offset, so the next round writes **the same key** and
overwrites it. While every round succeeds that is merely surprising. When one does not, it is a
durability failure:

1. Round N writes every Zone at tick 100. Complete.
2. Round N+1 at tick 700. Zone A is idle — same offset, same key — and its `Put` succeeds,
   advancing that object to tick 700. Zone B's `Put` fails.
3. The store now holds A at tick 700 and B at tick 100. **No tick has a complete set of Zone
   objects.** Round N is no longer reconstructible; round N+1 never was.

AC-5 says "the previous round remains the newest complete one". After a partial write there is no
complete round at all — and a partial write is exactly the circumstance AC-5 is about.

**Not an implementation bug.** Three things the story specifies are jointly unsatisfiable: an
offset-only key, idle Zones (which repeat offsets by definition), and a promise that the previous
round survives a partial write. Any two hold; the third cannot. Atomicity of a single `Put` — which
both stores do provide — is a claim about one object, not about a round.

**Reproduced**, and committed as `TestPartialRoundDestroysThePreviousOne_KnownGap` in
`server/tickloop/snapshot_test.go`. It asserts today's behaviour so the gap is executable rather
than a paragraph, and it carries the instruction to invert it when the format changes. Observed:
survivors at tick 142, the failed Zone at tick 42.

**Resolved 2026-09-22 (architecture review of PR #46): the key carries the tick.**
`{zone_id}/{state_version}/{tick}/{offset}`, both numbers zero-padded to 20 digits so lexical order
is tick order. A round's objects are immutable, so a partial failure cannot touch an earlier round.
Implemented on this branch; `TestPartialRoundDestroysThePreviousOne_KnownGap` became
`TestPartialRoundLeavesThePreviousOneComplete`, which is AC-5's second clause and which fails
against the old key.

It also resolves this section's other half at no extra cost. `{zone}/{version}/{tick}` is now a
prefix, so AW-SRV-007's `ListRounds` groups by a listing instead of a `Get` on every candidate to
read the tick out of its envelope — which is what the earlier draft of this section handed forward
as an inconvenience for that story to absorb.

The alternative considered and not taken was staging and promoting: keep the key, write every Zone
to a staging key, promote only when all succeed. It preserves the key format at the cost of a new
method on `sim.WorldStore` and a second write per object — a rename on `fs`, a server-side copy on
`s3`. The key change is smaller and fixes the round-grouping problem as well.

**On the ordering.** Tick leading offset is right — a Zone's offset is monotonic in its tick, so
the two orders differ only where the offset ties, which is precisely the idle Zone this section is
about, and leading with the tick makes "newest" mean recency rather than progress.

**Corrected 2026-09-23.** The amendment puts the tick *second*, directly under the Zone:
`{zone_id}/{tick}/{state_version}/{offset}`, not after `state_version` as the review body's prose
suggested and as this branch first implemented. Every normative document agrees — the story, the
glossary, ADR-0002, AW-SRV-007 and AW-INF-007 — and the reason is in the story: `{zone_id}/{tick}/`
is the prefix holding one Zone's part of a round, so `ListRounds` groups a round *without knowing
which `state_version` wrote it*. With the version ahead of the tick that grouping would need one
listing per version. `sim.SnapshotRoundPrefix` is that prefix, exported rather than assembled at
each call site, and `TestRoundPrefixGroupsAZoneAcrossStateVersions` pins the property.

This was worth catching late: the branch's own tests passed either way, because they built the key
and asserted against it through the same function. A self-consistent wrong key is invisible to a
round trip; only the contract catches it.

Both stores parse the key and order by tick descending rather than sorting strings, because a
lexical sort would still be wrong for a tree holding two `state_version`s.

**Related:** §4. Part of what makes an idle Zone's object non-reusable across rounds is that it
carries process-wide `prng_state` and `next_event_id`, which change even when the Zone does not. If
those moved out of the per-Zone body, an idle Zone's object would be genuinely identical between
rounds and the overwrite would be a no-op.

## 6. The Definition-of-done line on a "real" `state_version` bump — retired, not inherited

DoD requires "`state_version` migration is tested across at least one real bump." There is no
real bump available: the fields AW-SRV-014 and AW-SRV-022 would have bumped are present in
version 1, per §1. The migration machinery, the registry-completeness check that fails
`make check` on a bump without a migration, and the synthetic 1→2→3-with-4-refused chain from
the test plan are all implemented and tested.

**Resolved 2026-09-22 (architecture): the line retires rather than moving.** This section proposed
carrying it to AW-SRV-015. Architecture's review first accepted that and then corrected itself:
under `sim.StateVersion`'s rule — which the same review settled — AW-SRV-015 adds
`linkdead_since_tick` and populates `linkdead_deadline_tick`, both additive fields protobuf
absorbs, so AW-SRV-015 does not bump either. There is no *scheduled* bump at all, which is a
stronger reason than "not yet". The registry-completeness check is what the DoD line was reaching
for: a real bump cannot ship untested, because `make check` fails on a bump without a migration.

## 7. AC-1 holds, but only because hashing moved off the tick — and the margin is thin

**Measured**, `server/simtest` sizing fixture (16 Zones, 2,000 Rooms, 10,000 Entities, 500
Characters), AMD Ryzen 9 3900X:

| What the tick does at the boundary | Worst of 5 rounds | Budget |
|---|---|---|
| Copy **and hash** each Zone | **24.7 ms** | 5 ms |
| Copy only; hash off-tick | **2.7 ms** uncontended, 4.8 ms under load | 5 ms |

The first row was the obvious reading of the contract sketch, whose `Snapshot` carries
`StateHash` as a field alongside the body — a field is naturally filled at construction, and
`SnapshotAll` is the constructor. A CPU profile put 18% of the round in `sim.writeEscaped`:
the per-Zone hash walks every Entity, Component, and field and escapes each into a canonical
record, which is five times the cost of the copy itself.

The story's own rule settles it — "the only work inside the tick is the state copy; encoding
and upload run off-tick" — and hashing is encoding. `Snapshot.StateHash()` is therefore a
method that computes off-tick and caches, not a field. This is safe precisely because of the
copy-on-write design: the body is immutable after the boundary, so the hash of the copy is the
hash of the Zone at that tick whenever it is taken.

**Suggested amendment.** The contract sketch should show `StateHash()` as a method, or say in
words that the hash is computed off-tick. As written it invites the 24.7 ms implementation, and
the only thing that catches it is having built AC-1's fixture first.

**The margin is worth architecture's attention.** 2.7 ms of a 5 ms stall budget, and 4.8 ms
when the machine is otherwise busy, is not much headroom on a fixture whose Entity count is
itself an `[ASSUMPTION]`. Two consequences follow, neither of them this story's to decide:

- Entity count is the term that moves. Doubling to 20,000 Entities puts the copy over the
  budget, and the story's stated fallback — staggering Zones across boundaries — reintroduces
  the cross-Zone consistency problem the single cut exists to avoid.
- The copy is also the rebalance stall when ADR-0001's sharding activates, which the story's
  Data/state impact already names as a second consumer of the same number.

No change made here beyond moving the hash: the story's assumption holds at the fixture's
documented scale, and the fixture and its numbers are committed so that a later change to World
scale is a visible change to a failing test rather than a silent drift.

## 7b. The 25,000-Entity fixture, measured — the extrapolation held

The amended story asks for this in as many words: *"Measure it and replace this line with the real
numbers."* Done. `server/simtest/sizing.go` is at 25,000 Entities and
`snapshot.max_stall_ms` at `15`.

Measured on the same machine as the 10,000 figures (AMD Ryzen 9 3900X), worst of 5 rounds,
`make build` binary, `go test` without `-race` unless stated:

| Condition | Extrapolated | **Measured** |
|---|---|---|
| Uncontended (benchmark, 60 rounds over 3 runs) | ~6.8 ms | **7.4 – 8.7 ms** |
| Worst-of-5, package running alongside (5 runs) | ~12 ms | **8.1 – 9.6 ms** |
| Worst-of-5, under `-race` | — | 34.7 ms |

**The extrapolation was right for the uncontended case and pessimistic for the loaded one.** The
loaded figure came in at roughly three-quarters of what a linear extrapolation from the 10,000-Entity
loaded number predicted, not the 2.5× the Entity count would suggest — the copy is allocation-bound
and the earlier loaded measurement was taken against a busier machine, so the two "under load"
numbers are not measuring the same load. The honest reading is that both loaded figures are noisy and
the uncontended one is the number to reason from: **it tracks Entity count near-linearly, 2.7 ms at
10,000 and ~8 ms at 25,000**, which is 2.9× for 2.5× the Entities.

Against the amended budget: 8 ms of 15 ms, and 8 ms of ADR-0008's 50 ms tick budget, which is the
constraint that actually binds. The story's rule — "roughly twice the measured loaded number" — puts
15 ms almost exactly where the measurement lands, so the budget needs no revision.

The `-race` figure is why the AC-1 assertion scales its threshold by build rather than skipping under
`-race`: `make test` runs `-race` and is the only Go test target in `make check`, so a `!race` test
would never run in CI. See `stallfactor_race_test.go`.

## 8. The `s3` store adds a third-party dependency — `minio-go/v7`, Brian's call

`snapshot.store=s3` needs an S3 client and `go.mod` had none. Raised rather than decided,
because a new direct dependency is a standing architectural commitment rather than a detail
of this story.

**Resolved 2026-09-22 (Brian): `github.com/minio/minio-go/v7`**, over `aws-sdk-go-v2`. Lighter
(a handful of transitive modules against roughly fifteen), Apache-2.0 so `REUSE.toml` and
`make license-check` need no new entry, native to the MinIO the story's own assumption puts in
the local stack, and it speaks to AWS S3 unchanged. Credentials come from the environment and
then the IAM chain, so the cluster supplies a role or a web-identity token without
configuration; the static pair exists for MinIO locally.

`go get` also pulled cobra and pflag forward as a side effect. Both were pinned back: an
unrelated dependency bump riding along in a story's diff is a change nobody reviewed.

## 9. `andara-cli snapshot list` reads the store directly, not `Admin`

**Finding.** The story's test plan and `docs/runbooks/snapshot-stale.md` both call
`andara-cli snapshot list --zone <zone>`, but AW-SRV-006 defines no Admin RPC. AW-SRV-007's
interface contract does: `andara-cli snapshot list` and `snapshot verify` over
`Admin.ListSnapshotRounds`, with `ListSnapshotRoundsResponse` and friends.

**Decision taken.** This story's `snapshot list` reads the configured store directly, the way
`andara-cli sim repl` reads content from disk — no protocol change, no field numbers spent.
Defining that RPC here would be the implementation lane writing AW-SRV-007's contract, which is
the one thing the lane split exists to prevent.

The two commands are not redundant when AW-SRV-007 lands, and the overlap is worth keeping:
the Admin path answers *what does the running server see*, this one answers *what is actually
in the bucket*, and a runbook wants the second precisely when the first disagrees with it — or
when no server is running, which is the state a recovery starts from. If architecture would
rather have one command, the natural shape is `--local` on the AW-SRV-007 command rather than
two names.

## 10. Notes for review

- **AC-2 is met at the encoder, not across rounds.** "Identical Zone state snapshotted twice
  is byte-identical" holds for the encoding: the same `Snapshot` encoded twice, and two
  `Snapshot`s of identical state, produce identical bytes — the timestamp is stamped at the
  boundary rather than read at encode time, and every `repeated` is sorted on the way out. Two
  *rounds* over an idle Zone do differ, in `tick` and `taken_at_unix_nano`, by design; the live
  run showed exactly that (tick 31 then 62, same `state_hash`). The hash, which is what
  AW-SRV-007 compares, is stable.
- **The `[ASSUMPTION]`s**: three of four are resolved. Copy-on-write holds (§7, measured).
  World scale is committed as `server/simtest/sizing.go` and asserted by a test. Discovery via
  `WorldStore.List` is implemented, with §5's caveat for AW-SRV-007. MinIO in the local stack
  is **not** resolved and cannot be from this lane — the story itself defers it ("AW-INF-002
  gains a service; recorded there as a follow-up"). The `s3` store is exercised against a real
  MinIO instead, by `server/store/s3_test.go`, which skips unless `ANDARA_S3_TEST_ENDPOINT`
  names one; it will start running in CI the day AW-INF-002 lands the service.
- **The Definition-of-done line "the sizing fixture's numbers are recorded in the story"** is
  literally an edit to `docs/stories/`, which this lane may not make. §7's table has the
  numbers; architecture needs to copy them across.

## 11. A round is skipped when the tick's boundary was not published

Raised on PR #46 and fixed on this branch. `Publisher.Publish` failing was logged and swallowed,
and the round ran anyway — producing a snapshot for a tick with no `TickCompleted`. AC-8 requires
the envelope's tick to be one a boundary was emitted for, and AW-SRV-007 verifies a round through
that record, so such a snapshot cannot be verified. The worse half is the reporting: rounds kept
recording success, so the metrics showed healthy snapshots throughout a boundary outage and
`SnapshotStale` stayed silent through exactly the failure it exists to surface.

The loop now gates the round on the publish succeeding. Every error path out of
`KafkaPublisher.Publish` leaves the tick without a boundary — an Events send that fails returns
before the boundary is attempted, and `ErrBoundaryLost` means the process has stopped publishing
them — so the gate is on the error, not on its kind. It is per tick rather than a latch: rounds
resume when boundaries do. Skipping costs one cadence of an RTO optimisation, which is the cheap
side of the trade.

`sim.source=memory` has no publish failure mode, so a development process still snapshots.

## 12. Follow-ups this story unblocks but does not carry

- **`make measure-tick` against the sizing fixture.** `deploy/helm/andara/measurements.yaml` is
  a placeholder whose header says to flip it "once AW-SRV-006 defines" the sizing fixture, and
  `make helm-test` warns on every run until then. The fixture now exists, but as Go
  (`server/simtest/sizing.go`), while `scripts/measure_tick.sh` boots a server against a
  *content directory* — and the 10,000 Entities are runtime state that content does not carry,
  so a naive run would measure another idle floor. Closing it needs a generated content form of
  the fixture and a way to populate Entities in a running server. That is AW-INF-003 AC-10's
  line, not this story's acceptance criteria, so it is left open rather than half-done.
- **`TestRebind` in `server/egress` is flaky** — about one run in five, on `main`, independent
  of this branch. Unrelated to snapshots, but it is in `make check`, so it is an intermittent
  red build.

---

## 13. §8 review (2026-09-24, architecture) — for implementation

The story stays `review`. The full record is in the story under "§8 pass (2026-09-24)". What
implementation owes, in the order it matters:

1. **AC-3: the superset hash.** `StateHash()` must be SHA-256 over `ZoneCanonicalBytes(body)`
   followed by a snapshot record of `tick`, `prng_state` and `next_event_id`, exactly as the
   amended criterion and `snapshot.proto`'s comment say. `store/migrate.go`'s verification must use
   the same function. Add a test that corrupts each of the three in an encoded object and asserts
   the read fails its hash. Replace the Go-struct field counts with a tripwire over the
   `ZoneState`/`EntityState` **proto** descriptors: every field is covered by
   `ZoneCanonicalBytes` or the snapshot record, or the build fails.
2. **AC-8: acknowledgement, not enqueue.** Encode and `Put` wait for the boundary's
   `TickCompleted` to be acknowledged (`OnBoundaryAcked`/`OnBoundaryLost` already exist in
   `server/boot/tick.go`; route them to the round). Lost → abandon, `reason=boundary`. Neither
   before `snapshot.upload_timeout` → abandon, `reason=timeout`. Pre-create `boundary`. The test
   plan's two asynchronous cases get tests.
3. **`server/README.md`:** `max_stall_ms` default `15` (drop "measured 2.7 ms"), the key order
   `{zone_id}/{tick}/{state_version}/{offset}`, and `s3` described as the cluster's store and
   versitygw locally.

Delivered on an `impl/aw-srv-006-…` branch; the story returns to §8 when it merges.
