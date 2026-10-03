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

**`log.v1`.** 
The contract sketch adds `uint64 handoff_seq = 5` to `andara.log.v1.Entity`. Field 5 has been
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
reproduced on restore, and AW-SRV-007 AC-5 exits 2" (`docs/specs/protocol/andara/state/v1/zone_state.proto:12-17`).
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
Its records are never polled (`server/tickloop/loop.go:220`), and `Step` refuses them
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

## What isn't blocked

The answers above leave four parts of the story as they are:
- `Transit` and `Departures`;
- the retry;
- `in_transit` for verbs;
- the round-trip test (AC-9).

All four need `handoff_seq` on the wire and the transit state in the snapshot body, though, so
the work can't start before item 1.
