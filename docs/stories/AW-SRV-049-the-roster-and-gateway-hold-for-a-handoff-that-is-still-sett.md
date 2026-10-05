---
id: AW-SRV-049
title: The roster and Gateway hold for a handoff that is still settling
epic: EPIC-04
component: server          # server | cli | infra | client
type: bug                  # feature | infra | spike | chore | bug
status: draft              # draft | ready | in-progress | review | done | blocked
size: M                    # S | M | L  — L means "split it"
depends_on: [AW-SRV-014, AW-SRV-015, AW-SRV-028]
blocks: []
lane: implementation       # architecture (contracts) | sre (infra, ops) | implementation (source)
risk: high                 # low | medium | high
---

## Context

`AW-SRV-028` makes a cross-Zone move durable and rejects every Command for an Entity in transit
`in_transit`, `BindCharacter`, `UnbindCharacter` and `MarkLinkdead` included. The Gateway already holds
for a crossing: `ReleaseSession` waits for it to settle, bounded by `ingress.transit_hold` (2 s,
`AW-SRV-010`), and `sim.handoff_retry_ticks` defaults to 10 ticks (1 s) so the first retry of a lost
`Arrive` lands inside the hold. What outlasts the hold is not handled, and the roster in
`AW-SRV-014` and `AW-SRV-015` never learned of the new rejection. `AW-SRV-028`'s Scope lists what it
doesn't build, and architecture asked for this story on that list
(`docs/feedback/AW-SRV-028-handoff-contract.md`, "For Brian, PM and SRE"). It follows `AW-SRV-028`
and doesn't block it.

Three cases, from the roster's code. A teardown (`UnbindCharacter` or `MarkLinkdead`) rejected
`in_transit` is rejected post-log, after the roster's produce returned nil. For a linkdead end the
roster then holds the Character's live flag, and `holdLinkdead` waits on a `LinkdeadEnded` that never
comes, so the Account's other Characters are `already_live` until that same Character is selected again.
A `SelectCharacter` whose `BindCharacter` is rejected `in_transit` answers OK with the Session bound and
no body. And a teardown that was rejected is never retried, unlike a failed produce.

## User story

As a player, I want to reconnect, switch Characters or lose my connection while my Character is
crossing a Zone boundary, so that I'm never locked out of my Account or left bound to a body that
isn't there.

## Scope

### In scope
- **(a)** `SelectCharacter` waits for the Character's crossing to settle, as `ReleaseSession` does,
  bounded by `ingress.transit_hold`.
- **(b)** The roster frees its linkdead hold unless `LinkdeadEntered` is observed within the produce
  deadline (`ingress.produce_deadline`), so a rejected `MarkLinkdead` doesn't leave the Account's other
  Characters `already_live`.
- **(c)** A teardown rejected `in_transit` is retried once the Binding settles, as a failed produce is.
- A `BindCharacter` rejected `in_transit` after the wait in (a) answers `SelectCharacter` with a retryable
  error, never OK with no body.

### Out of scope
- Marking a Character that was in transit at a crash — `AW-SRV-048`.
- The `HandoffRejected{zone_faulted}` path and a faulted Zone — `AW-SRV-027`.
- A `sim repl` failure-injection flag, and player-facing text for `in_transit`. Architecture listed both
  as optional follow-ups on `AW-SRV-028` with no rush. The text may be game design (Brian's).
- Changing `ingress.transit_hold` or `sim.handoff_retry_ticks` defaults.

## Acceptance criteria

1. **Given** a Character whose `Departure` is applied and `Arrive` not yet **when** its Account calls
   `SelectCharacter` for it **then** the call waits, and answers OK with a bound body once the crossing
   settles within `ingress.transit_hold`.
2. **Given** a crossing still unsettled after `ingress.transit_hold` **when** `SelectCharacter` is called
   **then** it fails `UNAVAILABLE` (retryable), no Session is bound, and the roster holds no live flag
   for the Character.
3. **Given** a Session whose stream drops while its Character is in transit and whose `MarkLinkdead` is
   rejected `in_transit` **when** `ingress.produce_deadline` passes without `LinkdeadEntered` **then** the
   roster frees the linkdead hold, and `SelectCharacter` for another of the Account's Characters is not
   `already_live`.
4. **Given** the same Session **when** the crossing settles **then** the rejected teardown is produced
   again and the Character is marked linkdead, once, and never twice.
5. **Given** `session.linkdead_grace` `0` **when** a QUIT `UnbindCharacter` is rejected `in_transit`
   **then** it is retried after the Binding settles, and the Character ends dormant.
6. **Given** a `BindCharacter` rejected `in_transit` after the wait **when** `SelectCharacter` answers
   **then** the status is not OK.
7. **Given** a teardown retried and the Character since rebound by a newer Session **when** the retry
   would apply **then** it doesn't (the Session that owns the Binding wins), and the newer Session stays bound.
8. **Given** `-race` and 100 concurrent `SelectCharacter` and stream-drop pairs across a crossing
   **when** they run **then** no Account is left `already_live` with no linkdead Character, and no
   Session is bound with no body.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package roster

// settle waits until the Character's Binding is not in transit, bounded by ingress.transit_hold.
// Shared by SelectCharacter (new) and ReleaseSession (exists).
func (r *Roster) settle(ctx context.Context, id sim.EntityID) error // ErrTransitHold past the bound

// holdLinkdead frees the live flag unless LinkdeadEntered for the Character is observed within
// ingress.produce_deadline.

// retryTeardown re-produces a teardown rejected in_transit once the Binding settles;
// a newer Binding for the Character cancels it.
```

- **Errors:** `SelectCharacter` past the hold fails `UNAVAILABLE` with ErrorInfo reason `in_transit`;
  existing reasons (`already_live`, …) are unchanged.
- **Config:** `ingress.transit_hold`, `ingress.produce_deadline`, `sim.handoff_retry_ticks`. No new keys.
- **The roster learns a rejection from the post-log `CommandRejected{in_transit}` event** keyed by the
  Character. Whether that event is enough, or the roster needs a new observation point, is Open question 1.

## Data / state impact

No persisted state. The roster's in-memory live and linkdead flags change when they are freed. No effect
on a recovered server, which starts with no flags (`AW-SRV-048` covers the orphan at boot).

## Observability requirements
`[ASSUMPTION]` for SRE's §7 review, which owns this section.

### Metrics
- `andara_roster_transit_waits_total` — counter, label `outcome` (`settled`, `timed_out`). Two series.
- `andara_roster_teardown_retries_total` — counter, label `kind` (`linkdead`, `quit`). Two series.
- `andara_roster_linkdead_holds_freed_total` — counter, no labels.

### Logs
- `info` per wait that ends: `character_id`, `outcome`, `waited_ms`, plus the required fields and the
  correlation ID. `warn` when a hold is freed without `LinkdeadEntered`: `account_id`, `character_id`.

### Traces
- `roster.select.settle` as a child of the `SelectCharacter` span (`outcome`).

### Alerts
- None. Past-hold waits are ordinary during a stuck handoff, and `andara_handoffs_in_transit`
  (`AW-SRV-028`) is the signal.

## Test plan
- **Unit:** (a) with a stepped clock and a fake `Log` that rejects `in_transit`; (b)'s deadline; (c)'s
  retry, once; the newer-Binding cancel (AC-7); the `-race` pairs (AC-8).
- **Integration:** a two-Zone sim with a delayed `Arrive` and a Session that selects and drops across it
  (AC-1 to AC-5).
- **Manual/operator:** none until `sim repl` has a failure-injection flag (see Out of scope).

## Definition of done
CLAUDE.md §8, plus:
- The roster README's `already_live` and teardown lines say what happens during a crossing.

## Open questions
1. **Where does the roster observe a rejected teardown?** *(Architecture's.)* The post-log
   `CommandRejected{in_transit}` Event may be enough. If the roster needs a synchronous answer from the
   Log, `AW-SRV-014`'s produce seam changes and this story is M with a seam change.
2. `[ASSUMPTION]` `SelectCharacter` past the hold is `UNAVAILABLE`, matching `AW-SRV-010`'s past-hold row,
   not a new status.
3. `[ASSUMPTION]` Retry of a teardown is once per settle, not on a timer, since the Binding's settle is
   the event.
