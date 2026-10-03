# AW-SRV-028: contract questions before durable handoff can start

Story: `AW-SRV-028` (`ready`, not started). Implementation picked it up on 2026-10-02 and stopped
at the contract, because each item below needs a decision the story doesn't record. The story
was groomed on 2026-09-18, and every item comes from a story that landed or was groomed since:
- `AW-SRV-012`: the fallback Room;
- `AW-SRV-014` and `AW-SRV-015`: the Character, Session binding and linkdead;
- `AW-SRV-027`: faults reject rather than freeze;
- `AW-SRV-036`: goto.

## For architecture

### 1. `Entity.handoff_seq = 5` collides with `Entity.name`

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

Once the `.proto` lands, implementation runs `make proto` and commits `gen/` with the code.
**Ask:** the field number for `Entity.handoff_seq` (6 is the next free), and the proto change
itself.

### 2. `HandoffRejected`: `unknown_room` can't happen, and `zone_faulted` depends on `AW-SRV-027`

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

A restart doesn't clear the freeze, and it wouldn't clear 027's quarantine either. `Faulted` is
hashed state:
- the snapshot carries it (`server/sim/state.go:35,144`; `server/sim/snapshot_codec.go:80,170`);
- replay re-faults the Zone deterministically;
- nothing resets it on recovery.

An Entity in transit to a faulted Zone is therefore stuck as long as the Zone stays faulted,
which today means across restarts as well.

**Ask:**
- **(a)** Drop `unknown_room` from the code set, and AC-6 with it. The fallback is the answer to
  a missing Room.
- **(b)** Decide 028's behaviour toward a faulted target under both rules, or order `AW-SRV-027`
  before 028. Under 027, `HandoffRejected{zone_faulted}` restoring the Entity at home looks like
  the intended path. Under the freeze, the story needs to say what happens to the Entity: stuck
  until an operator acts, or the source giving up after N retries.

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

- **The Definition of done's "`AW-SRV-003`'s one-hop bounce is removed" is already true.**
  `AW-SRV-012` replaced the bounce with the fallback placement, and its story records that
  (`AW-SRV-012`, lines 220-222). The line can be struck.
- **Keep `Arrive.origin_zone_id` and `origin_room_id`.** No code reads them for a bounce any
  more, but `AW-SRV-036`'s goto writes them, and `AW-SRV-041` (draft, SPRINT-05) sets
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

All four need `handoff_seq` on the wire, though, so the work can't start before item 1.
