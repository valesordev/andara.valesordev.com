# AW-SRV-007: a Character in transit at a crash is left unmarked

Raised by architecture at `AW-SRV-007`'s §8, 2026-10-05, on Brian's decision to split it out of that story
(it was AC-17 on PR #419). Each heading names the role that should answer.

## For PM: a story request

**The gap.** A Character in a `Transit` record when the process is killed isn't in any Zone's `Entities`, so
`AW-SRV-007`'s boot sweep (`Roster.MarkOrphans` over the bodies present at boot) skips it. `MarkLinkdead`
rejects `in_transit` (`AW-SRV-028`), so nothing marks it while the handoff is pending. The `Arrive` retry then
lands it, non-linkdead and with no Session. `BindCharacter` accepts a present, non-linkdead body, so its
player can rebind it. Until they do, nothing times the body out: it stays in the world with no linkdead
deadline, the "present forever" that `AW-SRV-015`'s inherited line exists to prevent. The hole exists today;
`AW-SRV-007` doesn't widen it.

**The story.** `lane: implementation`, `component: server`, `type: bug` or `feature` as you judge, risk
high (a concurrency ordering between the Roster and the sim). `depends_on`: `AW-SRV-007` (its boot sweep),
`AW-SRV-028` (`Transit`, `in_transit`), `AW-SRV-014` and `AW-SRV-015` (Roster, linkdead). Sized M at most; split
it if the Roster side and the sim side separate. Candidate for SPRINT-05. It needs architecture's contract
review before `ready`, and the open design choice below.

**What it must achieve (candidate AC).** Given a Character in a `Transit` record at the kill, with its Session
gone, when the handoff lands, then its body is marked linkdead (an `UnbindCharacter{QUIT}` at
`session.linkdead_grace` `0`), and a Character that a player rebinds first is never marked, nor removed at
grace `0`.

## What `AW-SRV-007`'s review rounds found (a starting point for the contract)

Four rounds on PR #419, each finding a further gap, so the design below is the list of things any answer has
to hold, not a finished contract.
1. **Retry, not one shot.** The mark is retried until the Entity is placed or gone, flat every
   `sim.handoff_retry_ticks`. Each attempt re-resolves where the Character is (Entities, then Transit, as
   `BindCharacter` does) and goes to the Zone that holds it. A mark produced to the source after the ack
   no-ops (`applyMarkLinkdead` finds nothing there). A handoff stuck on a faulted Zone retries without end and
   shows on `andara_handoffs_in_transit`.
2. **Cap.** At most `sim.handoff_retry_batch` marks per tick, earliest due first, a budget apart from
   `Arrive` retries (so up to twice the batch overall), so a crash that left many Characters in transit
   doesn't recreate the restart burst the cap exists for.
3. **The rebind race.** A body that lands between attempts can be rebound, and a mark applied afterwards
   marks it or removes it. A flag checked and then produced doesn't close it: `Select` registers its entry under
   `r.mu`, releases the lock, and produces its `BindCharacter` afterwards. The Roster indexes entries by
   Account (`byAccount`, `bySession`), and an orphan has none, so a marking entry needs its own set by
   Character ID, checked by `Select` in the critical section that registers its entry.
4. **Apply order, not log order.** The mark goes to the Zone holding the body; `Select`'s Bind goes to the
   Roster's last-known Zone, usually stale for a crash orphan. Zones can be on different partitions
   (`hash(ZoneID) % 64`), so offset order doesn't span them. A Bind applied first finds the body present and
   not linkdead and answers `BindPresent` with no re-route, and the mark then lands on a played body. So the
   Bind has to go to the Zone the mark went to, **and the marking entry has to live until the mark is
   observed applied**, not only until its produce returns, or a `Select` arriving in between races again.
5. **"Observed applied" has no event at grace `0`.** At grace above `0`, `ObserveLinkdead` receives
   `LinkdeadEntered`, keyed by Character ID. At grace `0` the QUIT unbind leaves the body dormant and nothing
   signals it, so observation there is the loop's own re-resolution at the next period.
6. **At grace `0` the Bind wakes the dormant body** (`BindWoken`); it doesn't spawn a fresh one.
7. **`MarkOrphans` needs no ordering:** it runs before `grpc.listen` is bound, so no `Select` exists, and it
   takes the bodies present at boot.
8. **Accepted limit:** a `Select` whose own produce failed leaves its body unmarked until the next select
   (AC-11's failed-produce path); don't try to close it here.
9. **Tests.** A sim test over two Zones on two partitions is vacuous unless it has a control: a Bind to the
   other Zone, applied first, must yield `BindPresent` (the reproducer). The routing assertion belongs to a
   Roster test with a fake `Log` that records each command's `ZoneId`, run under `-race`, with a `Select` that
   registers before the check, during the produce, and after the produce but before the observation.

## For architecture (me), at that story's contract review

There is an alternative to the Roster protocol: a sim-level guard that makes the mark conditional on the body
not having been bound since recovery, deterministic and independent of apply order. It needs new hashed state
on the Entity, so it is a `state_version` question (a bump and a migration). Weigh it against the protocol
above when the story is written.
