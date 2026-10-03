# AW-SRV-028: three contract questions before durable handoff can start

Story: `AW-SRV-028` (`ready`, not started). Implementation picked it up on 2026-10-02 and stopped
at the contract, because each item below needs a decision the story doesn't record. All three
come from stories that landed after this one was groomed on 2026-09-18.

## For architecture

### 1. `Entity.handoff_seq = 5` collides with `Entity.name`

The contract sketch adds `uint64 handoff_seq = 5` to `andara.log.v1.Entity`. Field 5 has been
`string name` since `AW-SRV-014` (`docs/specs/protocol/andara/log/v1/log.proto`, `message Entity`).
`Arrive.handoff_seq = 6` is free, and so are 13 and 14 in the `LoggedCommand` oneof, which the
proto already reserves in a comment.

The four additions belong in `docs/specs/`, which is architecture's, so implementation can't
make them:
- `Arrive.handoff_seq`;
- `Entity.handoff_seq`;
- `HandoffAck`;
- `HandoffRejected`.

Once the `.proto` lands, implementation runs `make proto` and commits `gen/` with the code.
**Ask:** the field number for `Entity.handoff_seq` (6 is the next free), and the proto change
itself.

### 2. AC-6 and `HandoffRejected` have no reachable trigger

AC-6 is "B's target Room removed **and no `fallback_room`**". Since `AW-SRV-012`, every Zone must
declare a `fallback_room` that is one of its own Rooms. The loader refuses content that doesn't
(`ErrFallbackMissing`, `server/sim/build.go:333-352`). `applyArrive` already places an Entity
whose Room is gone in the fallback Room, with `EntityRelocated{room_removed}`
(`server/sim/verbs.go:305-330`). So `unknown_room` can't reach a rejection.

The other code, `zone_faulted`, can't be produced by the target either. A faulted Zone's
Partition is frozen: its records are never polled (`Engine.FaultedPartitions`, and
`ErrZoneFaulted` in `Step`), so B never applies the `Arrive` and never answers. The source would
retry every `handoff_retry_ticks`, and each retry queues another record behind the frozen offset.

Neither can a removed Zone produce one, because a swap that removes a Zone is refused
(`zone_removed`).

**Ask, one of:**
- **(a)** Drop `HandoffRejected`, AC-6 and `andara_handoff_rejected_total`, and leave field 14
  reserved. The fallback is the answer to a missing Room.
- **(b)** Keep `HandoffRejected` for a case that can happen, and say which: for example, the
  source giving up on a target whose Zone is faulted after N retries, and restoring the Entity
  at home.
- **(c)** Something else.

Whichever applies, the story should say what happens to an Entity in transit to a faulted Zone.
As written it stays in the source's `Transit` and retries until the fault clears, which needs a
restart (`AW-SRV-027`). That may be the intent, but no AC states it.

### 3. The Definition of done's "one-hop bounce is removed" is already true

`AW-SRV-012` replaced `AW-SRV-003`'s bounce with the fallback placement. `Arrive.origin_zone_id`
and `origin_room_id` are still in the proto, documented as the bounce's, but no code reads them
for a bounce. If (a) above holds, the story could say instead whether those two fields stay,
deprecated, or are reserved.

## What isn't blocked

Nothing else in the story depends on these answers:
- `Transit` and `Departures`;
- the retry;
- `in_transit`;
- the round-trip test (AC-9).

All of them need `handoff_seq` on the wire, though, so the work can't start before item 1.
