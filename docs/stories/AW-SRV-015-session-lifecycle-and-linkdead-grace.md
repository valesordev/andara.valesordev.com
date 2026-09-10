---
id: AW-SRV-015
title: Session lifecycle and linkdead grace period
epic: EPIC-08
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-014]
blocks: []
lane: implementation
risk: high
---

> `status: draft` — unblocked by ADR-0006 and scoped. Groomed to `ready` when M2 approaches. The
> combat interaction is now decided, so what remains is the interface contract.

## Context

ADR-0006 decided the lifecycle: on stream loss the Character remains in the World marked linkdead for
`session.linkdead_grace` (180 seconds); reconnecting within that window rebinds; on expiry the Character
is **despawned** — removed from the World with its state persisted, not deleted.

The grace period is load-bearing beyond player experience. On any restart every Session drops and every
Character goes linkdead, so **`linkdead_grace` must outlast the RTO** or a routine deploy empties the map.
At 180 s against a 60 s RTO target there is 3× margin — and that is exactly the kind of relationship
someone breaks by tightening one number in isolation.

This is the story that closes Phase 1 exit criterion 1: disconnect, reconnect, and find the World as you
left it.

## User story

As a player, I want a dropped connection to be survivable rather than fatal, so that a bad network does
not cost me my place in the world.

## Scope

### In scope
- The Session state machine: connected → authenticated → playing → linkdead → playing or despawned.
- The linkdead grace timer, its configuration, and its expiry behavior.
- **Combat extension of the grace timer** (ADR-0006): a linkdead Character in combat stays attackable and
  its remaining grace refreshes to `linkdead_combat_extension` on each combat interaction, bounded by the
  absolute `linkdead_max` ceiling. Damage lands; death is possible; survival by despawn is possible.
- The combat-interaction interface: combat emits an Event that refreshes the deadline. This story owns
  the contract; whichever epic defines combat consumes it.
- Reconnect and rebind, including resuming the Event stream via `AW-SRV-011`'s `last_event_id`.
- `CharacterDespawned` Event so everyone in the Room sees it happen.
- Clean quit: an explicit `CloseSession` despawns immediately rather than leaving a linkdead body.
- A startup assertion that `linkdead_max >= linkdead_grace > RTO_target`, and that `linkdead_grace`
  covers `egress.resume_window` at the observed Event rate — failing loudly rather than producing a world
  where every restart empties the map or where the ceiling truncates the ordinary case.
- Reconciling the resume window with the grace period — `AW-SRV-011` flags that if the window is too
  small at the observed Event rate, every linkdead reconnect becomes a resync.

### Out of scope
- Roster and selection — `AW-SRV-014`.
- The combat system itself. This story defines the deadline-refresh contract and nothing about how damage
  is computed.
- Whether a linkdead Character defends itself or flees. If it should, that is a Behavior (`EPIC-09`), not
  a Session concern — and it would rebalance all three grace numbers.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a playing Session **when** its stream drops **then** the Character is marked linkdead, remains
   in its Room, and a state change is visible to others in the Room.
2. **Given** a linkdead Character **when** the player reconnects within the grace period **then** the
   Session rebinds to the same Character, the Event stream resumes from `last_event_id` with no gap, and
   the Character was never removed.
3. **Given** a linkdead Character **when** the grace period expires **then** it is despawned, its state is
   persisted, a `CharacterDespawned` Event is emitted, and it is no longer targetable.
4. **Given** a despawned Character **when** the player logs back in **then** it is placed at the position
   it held at despawn.
5. **Given** an explicit quit **when** `CloseSession` is called **then** the Character despawns
   immediately, with no linkdead body left behind — per `AW-CLI-004` AC-11.
6. **Given** a server restart while a Character is linkdead **when** the World recovers **then** the
   linkdead state and its remaining grace are reconstructed from World state, not lost. A restart must not
   silently extend or cancel a grace period.
7. **Given** the observed Event rate **when** the resume window and the grace period are compared **then**
   a reconnect at the end of the grace period resumes rather than resyncs, or the configuration is flagged
   as inconsistent at startup.
8. **Given** a configuration violating `linkdead_max >= linkdead_grace > RTO_target` **when** the server
   starts **then** it refuses to start, naming the violated relation and every value in it. The ADR-0006
   invariant is asserted, not documented.
9. **Given** a full server restart completing within RTO **when** clients reconnect **then** every
   Character that was playing rebinds and none despawns. This is the criterion the grace period exists
   for.
10. **Given** a linkdead Character being attacked **when** each combat interaction lands **then** its
    remaining grace becomes `max(remaining, linkdead_combat_extension)` — a refresh, not an accumulation
    — and the Character remains a valid target throughout.
11. **Given** a linkdead Character under sustained attack **when** `linkdead_max` is reached **then** it
    despawns regardless of ongoing combat, and a `CharacterDespawned` Event is emitted so the attacker
    sees it leave rather than simply stopping.
12. **Given** a linkdead Character attacked once and then left alone **when** `linkdead_combat_extension`
    elapses with no further interaction **then** it despawns — 60 s after the last blow, not at the
    original 180 s deadline.
13. **Given** a linkdead Character that takes lethal damage before either deadline **when** it dies
    **then** normal death rules apply. Being linkdead confers no protection beyond the eventual despawn.
14. **Given** the same combat sequence replayed from a snapshot **when** replay completes **then** the
    despawn happens on the identical Tick, because the deadline is a Tick extended by a Command inside
    the tick rather than a wall-clock timer.

## Interface contract

To be written at grooming. Committed now: the grace timer is World state, not Session state — AC-6
requires it, since Session state does not survive a restart and the grace period must.

## Data / state impact

The linkdead flag and the grace deadline are World state and therefore part of `StateHash` and every
snapshot. Expressing the deadline as a Tick rather than a wall-clock time is what keeps it deterministic
under replay; a wall-clock deadline would make recovery produce a different World than the one players
were in, which is exactly the failure ADR-0002's determinism rules exist to prevent.

## Observability requirements

- **Metrics:** `andara_sessions_linkdead` (gauge, label `in_combat`),
  `andara_linkdead_outcomes_total` (counter, label `outcome` — `reconnected`, `despawned`, `died`),
  `andara_linkdead_duration_seconds` (histogram, label `in_combat`),
  `andara_linkdead_combat_extensions_total` (counter),
  `andara_linkdead_ceiling_despawns_total` (counter — despawns that hit `linkdead_max` while still in
  combat; a rising rate is the signal that the ceiling is too short and players are combat-logging),
  `andara_reconnect_resyncs_total` (counter — the AC-7 signal).
- **Logs:** linkdead entry and exit at `info` with Session, Character, and outcome.
- **Traces:** linkdead entry and reconnect as events on the `session.lifetime` span.
- **Alerts:** a rising linkdead rate is a symptom of network or server trouble and is tied to the Session
  availability SLO from `AW-SRV-011` rather than getting its own.

## Test plan

Drop-and-reconnect inside the window; drop-and-expire asserting despawn and persisted position; restart
mid-grace asserting the deadline survives (AC-6); explicit quit asserting no linkdead body; the resume
window and grace period consistency check.

## Definition of done

CLAUDE.md §8, plus: the restart-mid-grace test, and a startup check that flags an inconsistent
`resume_window` / `linkdead_grace` pair rather than letting every reconnect resync silently.

## Open questions

- **Combat extends the timer, bounded by a ceiling** (ADR-0006). Defaults: 60 s extension, 300 s
  ceiling — both starting points to retune once combat pacing exists to measure against.
  `andara_linkdead_ceiling_despawns_total` is the metric that tells you the ceiling is too short.
- **180-second default grace**, decided. Long enough to survive a dropped connection *and* a full server
  restart, short enough that a body is not a trade dummy for ten minutes.
- `[NEEDS BRIAN]` Whether other players can tell a Character is linkdead. This matters more now than it
  did: with combat extending the timer, a visible linkdead marker tells an attacker they have a bounded
  window on a defenceless target, while hiding it makes the mechanic invisible to the people it affects.
- `[NEEDS BRIAN]` Whether a linkdead Character is inert, defends itself, or flees. The decision makes the
  timeout the survivability mechanism, which implies inert — carried from ADR-0006.
