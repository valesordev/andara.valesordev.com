# AW-SRV-028: contract questions before durable handoff can start

Story: `AW-SRV-028` (`ready`, not started). Implementation picked it up on 2026-10-02 and stopped
at the contract, because each item below needs a decision the story doesn't record. The story
was groomed on 2026-09-18. Each item comes from a story that landed since, or whose interaction
with this one was never written down:
- `AW-SRV-012`: the fallback Room;
- `AW-SRV-014` and `AW-SRV-015`: the Character, Session binding and linkdead;
- `AW-SRV-027`: faults reject rather than freeze;
- `AW-SRV-036`: goto;
- `AW-SRV-006`: the snapshot body.

## For architecture

### 1. The wire and snapshot fields: one collides, and the snapshot's are missing

**`log.v1`.** The contract sketch adds `uint64 handoff_seq = 5` to `andara.log.v1.Entity`. Field 5 has been
`string name` since `AW-SRV-014` (`docs/specs/protocol/andara/log/v1/log.proto`, `message Entity`).
`Arrive.handoff_seq = 6` is free, and so are 13 and 14 in the `LoggedCommand` oneof, which the
proto holds for this story in a comment.

The four additions belong in `docs/specs/`, which is architecture's, so implementation can't
make them:
- `Arrive.handoff_seq`;
- `Entity.handoff_seq`;
- `HandoffAck`;
- `HandoffRejected`.

**`state.v1`.** `Transit`, `Departures` and `EntityState.HandoffSeq` are hashed Zone state (the
story's Data/state impact). The snapshot body has to carry them, or a restore can't reproduce the
hash. `zone_state.proto` says so itself: "A field the hash covers but the body omits cannot be
reproduced on restore, and recovery exits 6 on that mismatch (AW-SRV-043)"
(`docs/specs/protocol/andara/state/v1/zone_state.proto:12-17`).
Today `ZoneState` has fields 1-8 with no transit or departures, and `EntityState` has 1-12 with no
handoff sequence. AC-2 recovers `Transit` "at the recovered hash", so this is on the AC's path.

Once the `.proto` changes land, implementation runs `make proto` and commits `gen/` with the
code. **Ask:**
- the field number for `log.v1.Entity.handoff_seq` (6 is the next free);
- `state.v1.ZoneState.transit` and `departures`, with their message shapes, including whether a
  transit record embeds `log.v1.Entity` or `state.v1.EntityState`;
- `state.v1.EntityState.handoff_seq` (13 is the next free);
- the proto changes themselves.

Separately, the story says "There is no `state_version` in effect yet". That's stale:
`sim.StateVersion` is 1 (`server/sim/state.go:17-23`). The conclusion holds, since there's no
bump, but under the rule that additive fields don't move it, not for the reason the story gives.

### 2. `HandoffRejected`: `unknown_room` can't happen, and faults need a decision in both directions

**`unknown_room`.** AC-6 is "B's target Room removed **and no `fallback_room`**". Since
`AW-SRV-012`, every Zone must declare a `fallback_room` that is one of its own Rooms:
- the loader refuses content that doesn't (`ErrFallbackMissing`, `server/sim/build.go:333-355`);
- so does a swap (`fallback_missing`).

`applyArrive` already places an Entity whose Room is gone in the fallback Room, with
`EntityRelocated{room_removed}` (`server/sim/verbs.go:305-331`). A removed Zone can't trigger a
rejection either, because a swap that removes a Zone is refused (`zone_removed`). So no
rejection can carry `unknown_room`.

**`zone_faulted`.** Under today's rule (`AW-SRV-002`), a faulted Zone's Partition is frozen.
Its records are never polled (`server/tickloop/loop.go:289`), and `Step` refuses them
(`server/sim/engine.go:403-412`). B never applies the `Arrive` and never answers.

`AW-SRV-027` (`ready`, in no sprint) replaces the freeze. A faulted Zone's records are "consumed
and rejected with `zone_faulted`", and its AC-4 covers a cross-Zone `Produce` into a faulted
Zone. Under that rule the target *can* answer `zone_faulted`, which is the case
`HandoffRejected` was written for. So the behaviour depends on which rule is in effect:

| Rule in effect | What 028 as written does with an `Arrive` into faulted B |
|----------------|---------------------------------------------------------|
| `AW-SRV-002` freeze (today) | Nothing answers. A retries every `handoff_retry_ticks`, and each retry queues one more record behind the frozen offset. |
| `AW-SRV-027` reject | B consumes it and rejects it. With `HandoffRejected`, A restores the Entity at home. Without it, A retries forever. |

The fault reaches past the faulted Zone, in both directions:
- **Same Partition.** Today's freeze is per Partition, not per Zone
  (`Engine.FaultedPartitions`, `server/sim/engine.go:349-361`). An `Arrive` into a healthy Zone
  that shares a Partition with a faulted one also goes unanswered.
- **Source side.** If the source's Partition is frozen, or under 027 the source Zone is faulted,
  the `HandoffAck` is never applied. A keeps retrying every `handoff_retry_ticks`, and B re-acks
  each one, unless the per-tick retry skips a faulted Zone. `expireLinkdead` already skips
  `z.Faulted` (`server/sim/linkdead.go:127-134`), so there's a precedent.

The repo disagrees with itself on whether a restart clears a fault:
- `server/sim/state.go:32-33` and 027's `[ASSUMPTION]` say a fault lasts "until restart";
- but the snapshot carries `Faulted` (`server/sim/state.go:35,144`;
  `server/sim/snapshot_codec.go:80,170`), and nothing resets it on recovery;
- and 027's Definition of done records that replay through the faulting tick diverges today.

So, as the code stands, an Entity in transit to a faulted Zone stays stuck across restarts.

**Ask:**
- **(a)** Drop `unknown_room` from the code set, and AC-6 with it. The fallback is the answer to
  a missing Room.
- **(b)** Decide 028's behaviour toward an `Arrive` or a `HandoffAck` whose target Partition is
  frozen, or whose Zone is faulted, in either direction, under both rules. Or order `AW-SRV-027`
  before 028. Under 027, `HandoffRejected{zone_faulted}` restoring the Entity at home looks like
  the intended path. Under the freeze, the story needs to say what happens to the Entity: stuck
  until an operator acts, or the source giving up after N retries.
- **(c)** Does the per-tick retry skip a faulted source Zone?

### 3. Bind, Unbind and MarkLinkdead against an Entity in `Transit`

The story takes an Entity in transit out of `Entities` ("An Entity in transit is not in
`Entities`"), and AC-7 covers only verbs. Three Commands find their Entity only in `Entities`:
- **`BindCharacter`** looks in its own Zone (`server/sim/character.go:59`), then in every other
  Zone's `Entities`. If it finds nothing, it creates a new body at the spawn Room
  (`character.go:106-121`). A reconnect or a Character switch during transit would therefore
  create a second body, and the `Arrive` would then place the original as well: two Entities
  with one ID.
- **`UnbindCharacter`** (`character.go:149`) and **`MarkLinkdead`** (`server/sim/linkdead.go:77`)
  would miss it. A Session that drops mid-handoff would leave the body neither unbound nor
  linkdead.

**Ask:** what each of the three does for an Entity in `Transit`. The options are to reject it
`in_transit`, to defer it, or to find the Entity in `Transit` and act on the record. And add an
AC that a Bind during transit never creates a second body.

### 4. Smaller corrections to the story

- **The Definition of done's "`AW-SRV-003`'s one-hop bounce is removed" is half done.**
  `AW-SRV-012` replaced the bounce with the fallback placement, and its story records that
  (`AW-SRV-012`, lines 220-222). But the line's second clause, "its story records the
  replacement", points at `AW-SRV-003`. That story still says `AW-SRV-028` retires the bounce
  (`AW-SRV-003`, lines 303-307). The line wants replacing with "`AW-SRV-003`'s record notes that
  `AW-SRV-012` replaced the bounce", rather than striking.
- **Keep `Arrive.origin_zone_id` and `origin_room_id`.** No code reads them for a bounce any
  more, but a cross-Zone move and `AW-SRV-036`'s goto both write them, and `AW-SRV-041` (draft, SPRINT-05) sets
  `from_direction` from them. They want re-documenting as the arrival's origin, not reserving.
- **Goto also produces a cross-Zone `Arrive`** (`server/sim/verbs.go:280-290`). Implementation
  will treat it as going through the same handshake (`Transit`, retry, `in_transit`) unless the
  story says otherwise. One line in Scope would settle it.
- **Field 14.** If `HandoffRejected` is dropped, leave 14 unused and drop the comment holding
  it. The proto keeps `reserved` for numbers that were used and then retired
  (`log.proto:72-77`).

### 5. The `Departures` window can expire before a retry arrives

The story prunes a `Departures` entry `2 × sim.handoff_retry_ticks + 1` of the target's ticks
after the departure. Its `[ASSUMPTION]` reasons that "a stale copy can only come from a retry
issued before the ack landed, and the ack is one hop". But a retry can reach the target later
than that window, for reasons on either side:
- **The source applies its ack late, so it keeps producing retries.** That happens when:
  - its Partition is frozen today, or its Zone is faulted under `AW-SRV-027` (item 2);
  - its Partition lags behind a backlog;
  - the broker is slow to deliver the ack, since the retry is produced on the source's own
    ticks and doesn't wait on the broker;
  - its process restarts while the target's keeps ticking. That one needs multi-process: in
    today's single process the World's tick stops for both, and the first live tick polls the
    queued ack before the retry phase.
- **The target applies a retry late, though it was produced in time.** B's Partition lags
  behind a backlog (`Source.Poll` takes at most `sim.max_per_tick`, `server/tickloop/loop.go:289`),
  or the broker delivers the retry late. Meanwhile B's ticks keep advancing and pruning. An ack
  that arrives promptly at a healthy A doesn't help: the retry was already produced.

Either way, a retry can outlive the target's record:
1. A→B with `handoff_seq` k. B places the Entity and acks. A produces a retry before it applies
   the ack, or B is slow to read one.
2. B moves the Entity on to C and records `Departures[e] = k+1`.
3. `2 × retry_ticks + 1` of B's ticks later, B prunes that entry.
4. The retry of `Arrive(seq k)` reaches B and finds neither the Entity nor a departure. B places
   a second copy, while the real Entity stands in C.

That is two Entities with one ID, the failure this story exists to prevent. Nothing in the story
already prevents it:
- AC-8's check against the Entity's stored `handoff_seq` applies only "when it is present", and
  the Entity isn't in B;
- the re-ack applies only while B holds the Entity.

Pointed out by Codex on #357.

**Ask:** a deduplication rule that doesn't depend on timing on either side. Some options, each
with a cost:
- **(a) A high-watermark per Entity, pruned by proof rather than time.** Each Zone keeps the
  highest `handoff_seq` it has seen for an Entity until something proves no retry can still come.
  One such proof is a third leg: when A applies the `HandoffAck`, it produces
  `HandoffClosed{entity_id, handoff_seq}` to B's Partition. That record is ordered after every
  retry A produced there, and B prunes its entry when the record arrives. State is then bounded
  by open handoffs. The costs:
  - one more record kind (field 14, if `HandoffRejected` is dropped);
  - the close is tick-produced, so replay must re-derive it as it does the retries;
  - a close that is lost only keeps an entry longer, and never causes a duplicate.

  Without a proof like this, the watermark grows with every Entity that has ever passed through.
- **(b) Bounded retries, with the window sized to match.** The source stops after N attempts and
  skips a faulted Zone (item 2 (c)), and the `Departures` window is at least `(N + 1) ×
  retry_ticks`. This bounds when retries are *produced*, not when B *applies* them, so a lagging
  target still defeats it. On its own it only narrows the window. It also leaves open what
  happens to an Entity whose `Arrive` never landed after N attempts.
- **(c) The Entity's sequence travels with it.** B rejects an `Arrive` whose `handoff_seq` is
  lower than one the World has already seen for that Entity. That needs a World-scoped record of
  each Entity's current sequence, which crosses Partitions.

## What isn't blocked

With item 5, less of the story stands as written than this file first said:
- **Waiting on item 5:** `Transit`, the retry and `Departures`.
- **Settled as contract but not buildable yet:** the `in_transit` rejection for verbs. It's only
  reachable with an Entity in `Transit`, so it waits on `Transit` and on item 1's `state.v1`
  fields.
- **Can start before the rest:** the round-trip test (AC-9). It can land over today's fields.
  It needs `handoff_seq` only once `EntityState` gains that field (item 1).

## Architecture: the rulings (2026-10-04)

All five items are answered, in the story's body (amended) and in the protos. `make proto` is run and
`gen/` is committed with this change, so implementation runs nothing for the wire. In the order the file
asked for them, with one correction up front.

**A correction to my own first answer.** I first ruled option (a), a third record `HandoffClosed` that the
source produces after the ack and the target uses to prune its dedup state, and an adversarial review of
that contract found it unsound. The proof ("`Closed` follows every retry") holds only within one process
life: after a crash the source's new life can produce a retry of `Arrive(k)` behind the old life's
`Closed(k)`, and the target, having pruned, would place a second body. The same review found that a
one-shot forward of a deferred Unbind or MarkLinkdead is lost the same way. I've withdrawn both. What
follows is what stands.

### Item 5: the dedup rule is a per-Zone high-water mark of placed sequences, kept for good

Each Zone keeps, for every Entity that has arrived in it by handoff, the highest `handoff_seq` it placed
(`ZoneState.placed`, hashed). A handoff sequence only grows along an Entity's life, so an `Arrive(e, s)` at or
below the mark is a retry or stale, **however late it comes and from whatever process life**, and the
decision reads only the target's own state. There is no third record, no ordering between Partitions or
producers, no window, and no client-contract claim to add to `AW-INF-005`. The story spells the cases out
(re-ack, stale-ack, new handoff, `entity_present`, `invalid_arrival`) and adds the **implicit ack**: an
`Arrive` for an Entity the source still holds in its own `Transit` with a lower sequence proves the target
placed it. Options (b) and (c) fall as before: (b) bounds when retries are produced, not when a lagging target
applies them, and (c) needs a World-scoped record crossing Partitions.

**The cost, which we won't like.** The marks are never pruned: one small entry per Entity that has ever
arrived in a Zone by handoff, hashed with the Zone. At the sizing fixture's 25,000 Entities that is at most
25,000 entries per Zone visited. `andara_handoff_placed_entries` shows it. Pruning by proof is a story of its
own if the gauge ever says it matters, and it would have to hold retries until the Partition has caught up on
recovery. That is recorded in the story's Out of scope.

### Item 1: the wire and snapshot fields (done in the protos)

- `log.v1.Entity.handoff_seq = 6`, since field 5 is `name`, and `log.v1.Arrive.handoff_seq = 6`. The
  `Arrive`'s is the authority; a difference, or a 0, is `invalid_arrival`. `origin_zone_id` and
  `origin_room_id` stay, re-documented as the arrival's origin; the comment about a one-hop bounce is gone.
- `HandoffAck` is `LoggedCommand` 13. 14 stays unused here, held in its comment for `AW-SRV-027`'s
  `HandoffRejected`. 20 stays free.
- `state.v1.ZoneState.transit = 9` and `placed = 10`, and `EntityState.handoff_seq = 13`, with
  `TransitRecord` and `PlacedArrival`. **A transit record embeds `state.v1.EntityState`**, the hashed shape,
  and `entity.room_id` is the origin Room.
- **Hash and `state_version`.** Each new record is written only when present, as `entity_dormant` and
  `entity_linkdead` are, so a World with no handoff hashes as it did before, and the version doesn't move.
  **But a log written before this story that contains a cross-Zone move doesn't replay**: the old code
  deleted the Entity at the source, the new code keeps it in `Transit`, and recovery exits `6` at that tick.
  `dev` has such logs. Pre-launch the remedy is `make world-reset ENV=dev` with the deploy that carries this
  story. The review caught that the first amendment said otherwise.

### Item 2: faults

- **(a)** `unknown_room` is gone, and AC-6 with it. `applyArrive` already places in the fallback Room.
- **(b)** **Not ordering 027 first, and `HandoffRejected` moves out of 028.** 028 is correct under both
  rules. Under today's freeze a faulted target never answers, so the Entity stays in the source's `Transit`,
  retrying on a backoff. Under `AW-SRV-027` the target consumes and rejects, and **027 adds
  `HandoffRejected{zone_faulted}` (field 14) and the restore at home**. Its body now also says a faulted Zone
  applies the dedup rule first: a retry of a handoff it already placed is acked, never rejected, because the
  rejection would make the source restore the Entity at home while the frozen Zone still holds it.
- **(c)** **Yes, the retry skips a faulted source Zone, and a source whose Partition is frozen.** The
  backoff (`sim.handoff_retry_max_ticks`, new, default 600) bounds what a stuck target costs: the interval
  doubles to a minute, and the exponent saturates so it can't wrap to zero.
- **There is no operator release for an Entity stuck in transit to a faulted Zone before 027.** Its
  Character gets `in_transit` on every Bind. The story and the runbook say so. Whether a restart clears a
  fault is left to 027.

### Item 3: Bind, Unbind and MarkLinkdead in transit

- **All three reject `in_transit`**, and `BindCharacter` creates nothing. Its search is unchanged
  first (every Zone's `Entities`) and then every Zone's `Transit`, so in the few ticks between the target
  placing the Entity and the source applying the ack, a Bind finds it in `Entities`, where it is. It keeps
  the single-process assumption the existing cross-Zone search has.
- **I withdrew deferring Unbind and MarkLinkdead on the record.** A one-shot forward when the ack applies is
  lost to a crash or a replay discard, and it can land where the Entity no longer is (it moved on, or the
  ack was implicit). The consequence of rejecting them is the plain one: a Session that drops mid-handoff
  leaves the body standing present with no linkdead timer until its player selects it again, which is the
  outcome of a failed teardown today. The Gateway holding teardown Commands for the transit window is the
  proper fix, and isn't this story's.

### Item 4: the small corrections

- The Definition of done line is reworded as you suggested.
- `Arrive.origin_zone_id` and `origin_room_id` are kept as the arrival's origin.
- **`Goto` goes through the same handshake**, in the story's Scope.
- Field 14 is held for 027, since it isn't dropped.

## Other corrections from the review, all in the story

The backoff exponent saturates; AC-3 asserts `ZoneCanonicalBytes` and `Placed` rather than the World hash,
which changes every Step; AC-6 asserts replay by the hashed `attempts` and `last_attempt_tick`; the
`BodyStateHash` refusal rules and a snapshot round trip are an AC; the round-trip test lists the fields
`log.v1.Entity` deliberately omits; and AC-8 says the test feeds a hand-built record, since a well-behaved
Gateway holds a Session's Commands before the log.

## For implementation

Take the story as amended and the protos as pinned. Run `make proto-check` to see `gen/` matches. You can
start the EntityState round-trip test (AC-10) and the `Transit` and `Placed` types now, then the dedup rule,
then `Transit` and the retry, then the Bind handling.

## For Brian, PM and SRE

- **Brian:** this change needs `make world-reset ENV=dev` with its deploy, because old logs containing a
  cross-Zone move won't replay. It's pre-launch, but it destroys `dev`'s Characters and Accounts.
- **SRE:** review the story's two Observability additions, `andara_handoff_placed_entries` (no labels) and
  `sim.handoff_retry_max_ticks` (a new config key for `keys.yaml` and the values schema). The runbook step
  for a stuck handoff goes in `docs/runbooks/simulation-lagging.md`.
- **PM:** `AW-SRV-027` grew: it now owns `HandoffRejected{zone_faulted}`, the source's restore at home, and
  the dedup-first rule. Its size may want revisiting. It isn't in a sprint, and `AW-SRV-028` doesn't wait on
  it.
