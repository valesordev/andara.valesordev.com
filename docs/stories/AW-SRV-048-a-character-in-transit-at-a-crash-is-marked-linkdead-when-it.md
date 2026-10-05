---
id: AW-SRV-048
title: A Character in transit at a crash is marked linkdead when its handoff lands
epic: EPIC-04
component: server          # server | cli | infra | client
type: bug                  # feature | infra | spike | chore | bug
status: draft              # draft | ready | in-progress | review | done | blocked
size: M                    # S | M | L  — L means "split it"
depends_on: [AW-SRV-007, AW-SRV-014, AW-SRV-015, AW-SRV-028]
blocks: []
lane: implementation       # architecture (contracts) | sre (infra, ops) | implementation (source)
risk: high                 # low | medium | high
---

## Context

`AW-SRV-007`'s boot sweep (`Roster.MarkOrphans`) marks every Character body present at boot linkdead,
because a restart ends every Session (`AW-SRV-015`'s inherited line: no body is "present forever").
A Character that was in a `Transit` record when the process was killed isn't in any Zone's `Entities`
at boot, so the sweep skips it. `MarkLinkdead` rejects `in_transit` (`AW-SRV-028`), so nothing marks it
while the handoff is pending. The `Arrive` retry then lands the body non-linkdead with no Session.
`BindCharacter` accepts a present, non-linkdead body, so its player can rebind it, but until they do,
nothing times it out: it stays in the World with no linkdead deadline. The hole exists today, and
`AW-SRV-007` doesn't widen it. Architecture split it out of that story at its §8 (2026-10-05, Brian's
decision, `docs/feedback/AW-SRV-007-transit-orphan-mark.md`) after four review rounds each found a
further gap in the fix. It's an ordering protocol between the Roster and the sim across Zone
partitions, so it is its own story. Candidate for SPRINT-05.

The design below is what those reviews found, so it is the list of things any answer must hold, not a
finished contract. Whether the answer is a Roster protocol or a sim-level guard is architecture's
(Open question 1), so the story stays `draft` until that is ruled.

## User story

As an operator, I want a Character that was mid-handoff when the server was killed to be marked linkdead
once it lands, so that a crash never leaves a body in the World that nothing will ever time out.

## Scope

### In scope
- After recovery, a Character whose `Transit` record is applied (its `Arrive` lands) with no Session
  bound is marked linkdead: `MarkLinkdead`, or `UnbindCharacter{QUIT}` when `session.linkdead_grace`
  is `0`, as `ReleaseSession` does.
- A Character that its player rebinds first is never marked, and at grace `0` never removed.
- The mark is retried until the Entity is placed or gone: flat every `sim.handoff_retry_ticks`, each
  attempt re-resolving where the Character is (Entities, then Transit, as `BindCharacter` does) and
  going to the Zone that holds it.
- A cap of `sim.handoff_retry_batch` marks per tick, earliest due first, a budget apart from `Arrive`
  retries (so up to twice the batch overall).
- The Roster side of the rebind race (below).
- Tests, including the control that makes a two-Zone sim test non-vacuous.

### Out of scope
- The boot sweep of bodies present at boot — `AW-SRV-007`.
- A `Select` whose own produce failed leaving its body unmarked until the next select: an accepted limit
  (`AW-SRV-014` AC-11's failed-teardown-produce path). Don't close it here.
- `SelectCharacter` waiting on a crossing, the roster freeing a linkdead hold that never ends, and a
  teardown rejected `in_transit` being retried — a separate roster and Gateway story that follows
  `AW-SRV-028` (architecture's request, `docs/feedback/AW-SRV-028-handoff-contract.md`). This story marks
  an orphan after a crash, and that one handles live handoffs.
- NPCs, which the sweep leaves untouched (`Template == andara.core.Character` is the Character test).
- Pruning, quarantine and `HandoffRejected` — `AW-SRV-027`.

## Acceptance criteria

1. **Given** a Character in a `Transit` record at a kill, with its Session gone, **when** the server
   recovers and the handoff lands, **then** its body is marked linkdead within
   `ceil(n / sim.handoff_retry_batch) + 1` retry periods of the `Arrive` (`n` = Characters in transit),
   and with `session.linkdead_grace` `0` it is dormant, not removed.
2. **Given** that Character's player rebinds after it lands and before the mark is applied, **when** the
   mark is applied, **then** the body is not marked linkdead, not removed at grace `0`, and the Session
   stays bound.
3. **Given** two Zones on two different partitions (`hash(ZoneID) % 64`) and a `Select`'s `Bind` produced
   to the Roster's last-known Zone while the mark went to the Zone that holds the body, **when** both
   are applied in either order, **then** the `Bind` is routed to the same Zone as the mark, and the result
   is the same as AC-2. *(The control: a `Bind` sent to the other Zone and applied first yields
   `BindPresent`; the test fails without the routing.)*
4. **Given** a `Select` that registers before the Roster's check, during the mark's produce, and after
   the produce but before the mark is observed applied, **when** each runs under `-race`, **then** none
   leaves a marked body with a bound Session, or a Session with a removed body.
5. **Given** `sim.handoff_retry_batch + 10` Characters in transit at the kill, **when** the server
   recovers, **then** no tick produces more than `sim.handoff_retry_batch` marks, and every Character is
   marked or rebound within `ceil(n / sim.handoff_retry_batch) + 1` periods.
6. **Given** a handoff stuck on a faulted Zone, **when** the mark retries, **then** it retries without
   end, never marks a body that isn't placed, and the stuck handoff shows on `andara_handoffs_in_transit`.
7. **Given** a mark produced to the source Zone after the ack, **when** it is applied, **then**
   `applyMarkLinkdead` finds nothing there and no state changes, and the next attempt goes to the
   destination Zone.
8. **Given** `session.linkdead_grace` above `0`, **when** the mark is applied, **then** `LinkdeadEntered`
   is observed for that Character ID and the attempt completes; **given** grace `0`, **then** the attempt
   completes when the loop's re-resolution at the next period finds the body marked (there is no event).
9. **Given** the boot sweep, **when** it runs, **then** it needs no ordering, since it runs before
   `grpc.listen` is bound, so no `Select` exists. It is unchanged from `AW-SRV-007`.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package roster

// MarkTransit begins marking a Character that was in transit at recovery. Idempotent per Character ID.
// The entry lives until the mark is OBSERVED applied (not until its produce returns), and Select checks
// the entry set in the same r.mu critical section that registers its own entry.
func (r *Roster) MarkTransit(ctx context.Context, id sim.EntityID) error

// resolve: Entities first, then Transit (as BindCharacter does); returns the Zone that holds the body.
func (r *Roster) resolveHolder(id sim.EntityID) (sim.ZoneID, bool)

// Select's Bind goes to resolveHolder's Zone when a marking entry exists for the Character,
// never to the Roster's last-known Zone.
```

- **Marking entries** are a set keyed by Character ID, separate from `byAccount` and `bySession`
  (an orphan has no Account entry).
- **Observed applied:** at grace above `0`, `ObserveLinkdead` receives `LinkdeadEntered`; at grace `0`
  the QUIT unbind leaves the body dormant and nothing signals it, so observation is the loop's own
  re-resolution at the next period.
- **At grace `0`, the Bind wakes the dormant body** (`BindWoken`); it doesn't spawn a fresh one.
- **Apply order, not log order.** Zones sit on different partitions, so offset order doesn't span them.
  A `Bind` applied before the mark finds a present, non-linkdead body, answers `BindPresent` with no
  re-route, and the mark then lands on a played body. Routing the `Bind` to the mark's Zone closes it.
- **Config:** `sim.handoff_retry_ticks` and `sim.handoff_retry_batch` (`AW-SRV-028`). No new keys.
- **Error taxonomy:** none new. A mark that finds nothing is a no-op, not an error.

## Data / state impact

No new persisted state in the Roster protocol. The sim-level alternative needs new hashed state on the
Entity, which is a `state_version` bump and a migration (Open question 1). No effect on live Sessions:
the mark acts on bodies that have none.

## Observability requirements
`[ASSUMPTION]` for SRE's §7 review, which owns this section.

### Metrics
- `andara_transit_orphan_marks_total` — counter, label `outcome` (`marked`, `rebound`, `gone`). Three
  series.
- No other new series: `andara_handoffs_in_transit` (`AW-SRV-028`) already shows a stuck handoff.

### Logs
- `info` per Character when a mark is applied or skipped: `character_id`, `outcome`, `zone_id`,
  `attempt`, plus the required fields and the correlation ID. Per-event, not per tick.
- `warn` when a mark has retried `sim.handoff_retry_max_ticks` worth of periods without the body placed.

### Traces
- `roster.mark_transit.attempt` per attempt (`character_id`, `zone_id`, `outcome`), a root span: the
  retry is tick-driven after boot, so it has no `recovery.run` parent.

### Alerts
- None. A stuck handoff is `AW-SRV-028`'s signal.

## Test plan
- **Unit:** the Roster's marking-entry set and its `Select` check, with a fake `Log` that records each
  command's `ZoneId`, run under `-race`, with a `Select` that registers before the check, during the
  produce, and after the produce but before the observation (AC-4). The cap (AC-5). The retry and
  re-resolution (AC-6, AC-7). Grace `0` and above `0` observation (AC-8).
- **Integration:** a sim test over two Zones on two partitions with a **control**: a `Bind` to the other
  Zone, applied first, must yield `BindPresent` (the reproducer), or the test is vacuous (AC-3). A
  broker-level kill with a Character in a `Transit` record (AC-1), alongside `AW-SRV-007`'s
  `TestRecoveryWithAHandoffInFlight`.
- **Manual/operator:** none until `sim repl` has a failure-injection flag (architecture's optional
  follow-up on `AW-SRV-028`). A kill mid-handoff then stays an integration test.

## Definition of done
CLAUDE.md §8, plus:
- `AW-SRV-014`'s README line "nothing at boot invents an unbind" stays "nothing but the boot sweep and
  the mark of a Character that was in transit".
- The story's §8 record says whether the first live observation of `andara_transit_orphan_marks_total`
  is in the cluster or only against the local stack (§8, no in-cluster caller).

## Open questions
1. **Roster protocol or sim-level guard?** *(Architecture's, at contract review.)* The alternative to the
   protocol above is a guard that makes the mark conditional on the body not having been bound since
   recovery: deterministic and independent of apply order, but it needs new hashed state on the Entity
   (a `state_version` bump and a migration). The story can't be `ready` until this is ruled. If the guard
   wins, AC-3 and AC-4 and the Interface contract change and the story may shrink to S.
2. `[ASSUMPTION]` The retry is flat every `sim.handoff_retry_ticks`, as the feedback file says, though
   `AW-SRV-028`'s `Arrive` retry backs off exponentially to `sim.handoff_retry_max_ticks`. Flat keeps
   the orphan's wait short; if architecture wants the same schedule, AC-1's bound changes.
3. `[ASSUMPTION]` Size M. Split it if the Roster side and the sim side separate (the feedback file's
   own suggestion).
