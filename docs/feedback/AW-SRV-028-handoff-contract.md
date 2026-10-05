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
asked for them, with two corrections up front.

**Two corrections to my own earlier answers, from adversarial reviews of this contract.**
- I first ruled option (a), a third record `HandoffClosed` that prunes the target's dedup state. The proof
  ("`Closed` follows every retry") holds only within one process life: after a crash the source's new life can
  produce a retry behind the old life's `Closed`, and the target, having pruned, would place a second body.
  I also first deferred a teardown Command on the transit record, and a one-shot forward is lost to a crash.
  Both are withdrawn.
- I first put the retry schedule (`attempts`, `last_attempt_tick`) in the hashed record. It is derived from
  config, and config never enters hashed state: a restart with a retuned `sim.handoff_retry_ticks` would replay
  to a different hash and exit `6`. The schedule is now in memory, so a record found after a recovery is due on
  the first live tick.

### Item 5: the dedup rule is a per-Zone, per-Entity high-water mark of decided sequences, kept for good

Each Zone keeps, for every Entity decided in it by handoff, the highest `handoff_seq` it decided
(`ZoneState.placed`, hashed). A handoff sequence only grows along an Entity's life, so an `Arrive(e, s)` at or
below the mark is a retry or stale, **however late it comes and from whatever process life**, and the decision
reads only the target's own state. There is no third record, no ordering between Partitions or producers, no
window, and no client-contract claim. The story spells the cases out (re-ack, stale-ack, new handoff,
`entity_present`, `invalid_arrival`) and adds the **implicit ack**: an `Arrive` for an Entity the source still
holds in its own `Transit` with a lower sequence proves the target placed it. An ack with another sequence is
ignored, so a late `ack(e, 1)` can't drop `Transit(e, 3)`. Options (b) and (c) fall as before: (b) bounds when
retries are produced, not when a lagging target applies them, and (c) needs a World-scoped record crossing
Partitions.

**The cost, which we won't like.** The marks are never pruned: one entry per Entity decided in a Zone by
handoff, hashed with the Zone and copied by every in-tick snapshot. At the sizing fixture's 500 Characters that
is nothing; it grows with the Entities that cross Zones. `andara_handoff_placed_entries` shows it, and the
runbook sets a threshold past which pruning is wanted. Pruning by proof would need the target to be sure no
retry can still arrive, which it can't be across a restart of the source without holding retries until the
Partition has caught up on recovery, so it is a story of its own.

**A constraint on later stories: an Entity ID is never reused.** Marks assume it. Today only
`BindCharacter`'s never-bound path creates an Entity at seq 0, and the only deletions are cross-Zone departures.
That creation is guarded (`id_reused`). `AW-SRV-032`'s `PurgeCharacter` removes a body, so the roster never
reissues a `character_id` (clearing marks in every Zone would cross Partitions and reopen the hole), and
`AW-SRV-047`'s Item Instances take IDs that are never reused. Both stories say so now.

### Item 1: the wire and snapshot fields (done in the protos)

- `log.v1.Entity.handoff_seq = 6`, since field 5 is `name`, and `log.v1.Arrive.handoff_seq = 6`. The
  `Arrive`'s is the authority; a difference, or a 0, is `invalid_arrival`. `origin_zone_id` and
  `origin_room_id` stay, re-documented as the arrival's origin; the comment about a one-hop bounce is gone.
- `HandoffAck` is `LoggedCommand` 13. 14 is `HandoffRejected`, pinned in `log.proto` for `AW-SRV-027`,
  which builds it. 20 stays free.
- `state.v1.ZoneState.transit = 9` and `placed = 10`, and `EntityState.handoff_seq = 13`, with
  `TransitRecord` (entity, target Zone and Room, direction) and `PlacedArrival`. **A transit record embeds
  `state.v1.EntityState`**, the hashed shape, and `entity.room_id` is the origin Room.
- **Hash and `state_version`.** Each new record is written only when present, as `entity_dormant` and
  `entity_linkdead` are, so a World with no handoff hashes as it did before, and the version doesn't move.
  **But a log written before this story that contains a cross-Zone move doesn't replay**: the old code
  deleted the Entity at the source, the new code keeps it in `Transit`, and recovery exits `6` at that tick.
  `dev` has such logs. Pre-launch the remedy is `make world-reset ENV=dev` with the deploy that carries this
  story.

### Item 2: faults

- **(a)** `unknown_room` is gone, and AC-6 with it. `applyArrive` already places in the fallback Room.
- **(b)** **Not ordering 027 first, and `HandoffRejected` moves out of 028.** 028 is correct under both
  rules. Under today's freeze a faulted target never answers, so the Entity stays in the source's `Transit`,
  retrying on a backoff. Under `AW-SRV-027` the target consumes and rejects, and **027 adds
  `HandoffRejected{zone_faulted}` (field 14) and the restore at home**. Its body also says a faulted Zone
  applies the dedup rule first, because rejecting a retry of an already-placed handoff would make the source
  restore the Entity at home while the frozen Zone still holds it; and that a rejection also sets the mark, so
  a retry after a restore can't place a second body once the Zone is healthy. 027 now depends on 028.
- **(c)** **Yes, the retry skips a faulted source Zone, and a source whose Partition is frozen.** The
  backoff (`sim.handoff_retry_max_ticks`, new, default 100) bounds what a stuck target costs: the interval
  doubles to 10 s, and the exponent saturates so it can't wrap to zero. A new `sim.handoff_retry_batch`
  (default 50) caps the retries produced in one tick, so a restart with many in-flight handoffs can't fill a
  tick's budget and defer players' Commands.
- **There is no operator release for an Entity stuck in transit to a faulted Zone before 027.** Its
  Character gets `in_transit` on every Bind. The story says so, and so will the runbook. Whether a restart
  clears a fault is left to 027.

### Item 3: Bind, Unbind and MarkLinkdead in transit

- **All three reject `in_transit`**, and `BindCharacter` creates nothing. Its search is unchanged
  first (every Zone's `Entities`) and then every Zone's `Transit`, so in the few ticks between the target
  placing the Entity and the source applying the ack, a Bind finds it in `Entities`, where it is. It keeps
  the single-process assumption the existing cross-Zone search has.
- **The Gateway already holds for a crossing**: `ReleaseSession` waits for it to settle, bounded by
  `ingress.transit_hold` (2 s). The first retry should land inside the hold, so `sim.handoff_retry_ticks`
  defaults to 10 ticks (1 s) and startup logs a `warn` when it doesn't (a warning, not a refusal, since the
  default is in ticks and `sim.tick_rate` is configurable). It holds for the first retry only.
- **What outlasts the hold is worse than I first wrote.** A lost `Arrive` that outlasts two seconds leaves
  a teardown rejected after the roster's produce returned nil, so the roster holds a linkdead flag that
  `LinkdeadEnded` never frees (the Account's other Characters are `already_live` until the same Character is
  selected again), and a `BindCharacter` rejected post-log leaves `SelectCharacter` OK with the Session
  bound and no body. The accurate account is in the story's Scope.
- **A linkdead body can't depart** (`actor_linkdead`): `locate` accepts it today, and the linkdead fields
  aren't carried, so a body that left would lose its despawn timer.

### Item 4: the small corrections

- The Definition of done line is reworded as you suggested.
- `Arrive.origin_zone_id` and `origin_room_id` are kept as the arrival's origin.
- **`Goto` goes through the same handshake**, in the story's Scope, with an AC.
- Field 14 is `HandoffRejected`, pinned in the proto for 027 (a message with `entity_id`, `handoff_seq` and `code`), since 028 doesn't build it.

## Other corrections from the reviews, all in the story

The backoff exponent saturates; AC-3 asserts `ZoneCanonicalBytes` and `Placed` rather than the World hash,
which changes every Step; the retry schedule isn't hashed, with an AC for recovering under a changed config;
the `BodyStateHash` refusal rules and a snapshot round trip are an AC; ACs for a mark per Entity (two
Entities), ack matching and `Goto`; the round-trip test lists the fields `log.v1.Entity` deliberately omits;
AC-8 says the test feeds a hand-built record, since a well-behaved Gateway holds a Session's Commands before
the log; and the `AW-SRV-007` handoff-in-flight recovery test moved into 007's Definition of done.

## From the third review, all in the story

The retry mechanism is specified: the engine keeps `{attempts, last attempt}` in memory per
`(Entity, handoff_seq)`, a missing entry is due, a departure writes `(1, T)`, and `Replay` and `RestoreEngine`
clear every entry when they finish, since `ReplayEach` runs the same `Step` as the live loop. It is capped per
tick (`sim.handoff_retry_batch`). The config validation is a startup warning, not a refusal. The change isn't
reversible once a handoff is logged, and the recovery is `make world-reset ENV=dev CONFIRM=andara-dev`. The
state projector omits an Entity in transit, accepted here and left to `AW-SRV-019`. `AW-SRV-027`'s ACs are
written (a retry of a placed handoff is acked, a new handoff is rejected and sets the mark, a retry after a
restore is stale). The new rejection codes are in the glossary and in `sim.RejectCodes()`. The manual
`sim repl` step is dropped, since `sim repl` has no failure-injection flag.

## From the fourth review, all in the story

The retry pass is `Engine.DueHandoffs(tick)`, called by the live loop after `Step` and **never by replay**:
recovery runs through `ReplayEach`, which runs the same `Step` as the live loop, so a pass inside `Step` (or a
clear hooked to `Replay`) would leave each record looking just tried, and the projector's engine, which
must never produce, would retry too. An engine built by `RestoreEngine` has no schedule entries, so a
recovered record is due on the first live call. The entry is deleted whenever a `Transit` record is dropped.
`HandoffRejected` is now a pinned message in `log.proto` (field 14) for `AW-SRV-027`, which builds it; 027's
ACs are renumbered 1 to 9, and a `HandoffAck` for a faulted source is consumed with no Event.

## From Codex on #403, in the story

Two more, both real. A rejected handoff has to stay rejected: if the first `HandoffRejected` is lost, the
source retries, and a mark that held only the sequence would classify the retry as stale and ack it, so the
source would drop a record whose Entity was never placed and never restored. The mark now records its
outcome (`PlacedArrival.rejected`, field 3), and the target reissues the same answer. And replay writes no
schedule entries at all, so a record whose departure was replayed is due on the first live call like one
restored from a snapshot; this also closes the AC-6 over-claim the last review left.

## For implementation

Take the story as amended and the protos as pinned. Run `make proto-check` to see `gen/` matches. You can
start the EntityState round-trip test (AC-10) and the `Transit` and `Placed` types now, then the dedup rule,
then `Transit` and the retry, then the Bind handling.

## For Brian, PM and SRE

- **Brian:** this change needs `make world-reset ENV=dev` with its deploy, because old logs containing a
  cross-Zone move won't replay. It's pre-launch, but it destroys `dev`'s Characters and Accounts.
- **PM:** two things. `AW-SRV-027` grew: it owns `HandoffRejected{zone_faulted}`, the source's restore at
  home, the dedup-first rule and the mark on rejection, and now depends on 028. And a **new roster and
  Gateway story**, `lane: implementation`, for what the story's Scope lists and doesn't build:
  `SelectCharacter` waits for the Character's crossing to settle as release does; the roster frees a linkdead
  hold unless `LinkdeadEntered` is observed within the produce deadline; a teardown rejected `in_transit` is
  retried once the Binding settles. It follows `AW-SRV-028` and isn't a blocker for it.
- **SRE:** review the story's Observability additions, `andara_handoff_placed_entries` (no labels) and the
  per-tick summary `warn` in place of one per retry, and the new config keys `sim.handoff_retry_max_ticks`
  and `sim.handoff_retry_batch` for `keys.yaml` and the values schema, beside the default of
  `sim.handoff_retry_ticks`. The runbook step for a stuck handoff goes in `docs/runbooks/simulation-lagging.md`.

## Implementation's hand-off (2026-10-04): what's built, and what waits on SRE

Built on `impl/aw-srv-028-durable-handoff`. The code, the tests, `server/README.md` (config rows, metrics, the
handoff section) are in the PR. Two things the story's Definition of done asks for are in SRE's paths, so I
haven't made them:

- **Helm** (`deploy/helm/andara/keys.yaml`, `values.schema.json`, `templates/_env.tpl`): three keys, each an
  `int`, `story: AW-SRV-028`:
  - `sim.handoff_retry_ticks`, `ANDARA_HANDOFF_RETRY_TICKS`, default `10`, `min: 1`;
  - `sim.handoff_retry_max_ticks`, `ANDARA_HANDOFF_RETRY_MAX_TICKS`, default `100`, `min: 1` (the server also
    refuses a value below `sim.handoff_retry_ticks`);
  - `sim.handoff_retry_batch`, `ANDARA_HANDOFF_RETRY_BATCH`, default `50`, `min: 1`.
  The server reads them from the environment, the `--sim-handoff-retry-*` flags and the `sim:` block of its
  config file, like `sim.max_per_tick`.
- **Runbook** (`docs/runbooks/simulation-lagging.md`): a step for a stuck handoff. What the PR's code gives it:
  `andara_handoffs_in_transit` sustained above 0 means the broker or the target Partition is stuck; the `warn`
  line `handoffs retried: an Arrive was not acknowledged` (`count`, `oldest_attempt`) says how long and how
  many; `andara_handoff_retries_total` rising confirms the retry is running; a Character in transit to a
  **faulted** Zone has **no release** until `AW-SRV-027` (its Commands and Binds get `in_transit` until the fault
  is resolved). And a threshold on `andara_handoff_placed_entries` past which pruning is wanted: the story asks
  the runbook to set one, and I have no basis for the number. One mark is a few dozen bytes, hashed with its
  Zone and copied by every in-tick snapshot, so the figure is the one at which the snapshot copy's CPU limit
  (`stallFactor`, `server/simtest`) is no longer comfortable. **Architecture or SRE to pick it.**
- A **dashboard panel** for `andara_handoffs_in_transit` (the story's Observability section names it).
- The deploy needs `make world-reset ENV=dev CONFIRM=andara-dev` (the PR says so too).

## SRE: the observability amendment, and the instrumentation check (2026-10-05)

The §7 review SRE sent architecture on 2026-10-04 (architecture replied that it was accepted, in a cross-session
message that isn't in the repository) is landed in the story's
Observability section, with the sizing-fixture Test plan line. Two points where the review gave way to the
code once it merged:
- **The gauges' cost.** The review asked for the gauges to be maintained as marks change, not found by scanning
  every Zone each tick. The code sums the `len` of each Zone's `Transit` and `Placed` maps on each tick, which
  is the number of Zones, not the number of marks, so that concern was moot. The amended §7 says what is built,
  and keeps the part that mattered: the gauges are derived from state, never incremented, so a restart reads
  the restored values (observed live: 5 marks before and after a restart).
- **The Test plan line** is worded as a follow-up, not a Definition-of-done item, because the story is already at
  `review` and its tests are delivered. Architecture asked for it as a test-plan line; if it should gate
  `done`, say so.

The instrumentation check is in the story's "§8 instrumentation check" section. One deviation, the summary
`warn` (per tick rather than per window, and missing `retries` and `in_transit`), is issue #409 for
implementation and is not blocking.

### For architecture: one point to confirm
**The log bound is a change to the merged contract.** #403's §7 said one `warn` per tick that produced retries; the
amended §7 says at most one per `sim.handoff_retry_ticks` window, with `retries`, `oldest_attempt`, `tick`,
`in_transit` and `trace_id`. You replied on 2026-10-04 that it was accepted, in a message that isn't in the
repository, so please confirm it here. If you do, #409 asks implementation for the change; if not, the amended §7
goes back to per tick and #409 is closed.

(A retry's trace needs no confirmation: the code gives a retry no trace id because the hashed `Transit` record holds
none, the implementation record's Deviations has that as agreed with you on 2026-10-04, and the amended §7 now says
so. The one thing to know is that it is what puts a retry's `command.apply` at the tick's one-in-a-hundred.)

### For PM: a follow-up for the sizing fixture with marks
The runbook's two thresholds on `andara_handoff_placed_entries` (25,000 and 100,000) are derived from the
snapshot sizing fixture and unmeasured. Measuring them is a small `lane: implementation` story: a `Placed` mark
population (25,000 and 100,000) in `server/simtest/sizing.go`, with the in-tick copy and State Hash cost
recorded. It belongs before `AW-SRV-047`, whose Item Instances are what make marks grow.
