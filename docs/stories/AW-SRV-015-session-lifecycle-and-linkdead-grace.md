---
id: AW-SRV-015
title: Session lifecycle and linkdead grace period
epic: EPIC-08
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-014]
blocks: [AW-CLI-008, AW-INF-017]
lane: implementation
risk: high
---

## Context

ADR-0006 decided the lifecycle: on stream loss the Character remains in the World marked linkdead for
`session.linkdead_grace` (180 seconds); reconnecting within that window rebinds; on expiry the Character
is **despawned** — removed from the World with its state persisted, not deleted. Combat extends the
timer to a ceiling.

The grace period is load-bearing beyond player experience. On any restart every Session drops and every
Character goes linkdead, so **`linkdead_grace` must outlast the RTO** or a routine deploy empties the map.
At 180 s against a 60 s RTO target there is 3× margin — and that is exactly the kind of relationship
someone breaks by tightening one number in isolation.

**Decided 2026-09-11 (Brian): linkdead is visible to other players.** A `CharacterLinkdead` Event is
Room-scoped and `look` shows the marker. An attacker sees the bounded window; the mechanic is honest
about itself. This closes Phase 1 exit criterion 1: disconnect, reconnect, and find the World as you left
it.

## User story

As a player, I want a dropped connection to be survivable rather than fatal, so that a bad network does
not cost me my place in the world.

## Scope

### In scope
- The Gateway Session state machine and the World-side linkdead state, and the Commands between them.
- The grace deadline as a Tick in Zone state, set, refreshed, and expired inside the sim.
- Combat extension and ceiling (ADR-0006) via one sim hook, `OnCombatInteraction`.
- Reconnect and rebind, resuming the Event stream via `AW-SRV-011`'s `last_event_id`.
- `CharacterLinkdead`, `CharacterReconnected`, `CharacterDespawned` Events, Room-scoped.
- Clean quit: `CloseSession` despawns immediately.
- Startup assertions on the ADR-0006 invariant and on the resume window.

### Out of scope
- Roster and selection — `AW-SRV-014`.
- The combat system. This story defines the deadline-refresh hook and nothing about damage.
- A linkdead Character defending itself or fleeing. Inert, per ADR-0006; a Behavior could change it
  later and would rebalance the three numbers.

## Acceptance criteria

1. **Given** a playing Session **when** its stream drops **then** within `session.linkdead_detect`
   (default 5 s) a `MarkLinkdead` Command is produced, the Character stays in its Room, and a
   `CharacterLinkdead` Event with Room scope is emitted; `look` in that Room lists the name in both
   `RoomDescribed.occupants` and `RoomDescribed.linkdead`. A transport close is detected at once; a
   keepalive miss with no close (a partition) within `linkdead_detect`.
2. **Given** a linkdead Character **when** the player reconnects within the grace period and selects it
   **then** the Session rebinds, a stream that carries `last_event_id` resumes from it with no gap, and a
   `CharacterReconnected` Event is emitted in place of `CharacterArrived`; the Character was never
   removed. `SelectCharacter` is not `already_live` for the Character the linkdead Session left.
3. **Given** a linkdead Character **when** its deadline Tick is reached **then** the sim despawns it in
   that tick, marks it dormant with its position, emits `CharacterDespawned{reason=LINKDEAD}`, and it is
   no longer targetable.
4. **Given** a despawned Character **when** the player logs back in and selects it **then** it spawns at
   the position it held at despawn (`AW-SRV-014` AC-6).
5. **Given** `CloseSession` **when** it is called by a playing Session **then** an `UnbindCharacter{QUIT}`
   is produced and `CharacterDespawned{reason="quit"}` follows, in place of AW-SRV-014's
   `CharacterLeft{to_direction: ""}`; no linkdead body is left. `CloseSessionResponse` is sent only
   after the unbind is durable in the log and the Account's live flag is free, so a
   `SelectCharacter` for the same Character sent after the response is never `already_live`
   (`docs/feedback/AW-SRV-014-character-roster.md` §3).
6. **Given** a server killed while a Character is linkdead **when** the World recovers by full-log
   replay (M1's recovery) **then** the body's `linkdead_since_tick`, `linkdead_deadline_tick`,
   `linkdead_ceiling_tick` and `linkdead_extension_ticks` equal their values before the kill, and it
   despawns on the same Tick it would have; the grace is neither extended nor cancelled. The same
   from a snapshot is `AW-SRV-007`'s (inherited line there).
7. **Given** `egress.resume_window` (an Event count, `AW-SRV-011`), `session.linkdead_max`, and
   `egress.assumed_event_rate` **when** the server starts **then** it refuses to start if
   `resume_window < linkdead_max × assumed_event_rate`, naming all three; in production
   `andara_reconnect_resyncs_total` rising is the signal the assumed rate is too low.
8. **Given** configuration violating `linkdead_max >= linkdead_grace > recovery.rto_target` **when** the
   server starts **then** it exits `1` naming the violated relation and every value in it.
9. *(Moved to `AW-SRV-007` at contract review, 2026-09-26: the full-restart-within-RTO rebind needs
   snapshot recovery, which this story doesn't have. It is an inherited Definition-of-done line
   there.)*
10. **Given** a linkdead Character **when** `OnCombatInteraction` fires for it **then** its deadline
    becomes `max(deadline, now + extension_ticks)`, capped at `linkdead_since + max_ticks`; a refresh,
    not an accumulation.
11. **Given** sustained attack **when** `linkdead_since + max_ticks` is reached **then** it despawns with
    `reason=LINKDEAD_CEILING` and `andara_linkdead_ceiling_despawns_total` increments.
12. **Given** one attack then silence **when** `extension_ticks` elapse **then** it despawns — 60 s
    after the last blow, not at the original 180 s deadline.
13. **Given** lethal damage before either deadline **when** it dies **then** normal death rules apply;
    `andara_linkdead_outcomes_total{outcome="died"}` increments.
14. **Given** the same log replayed **when** replay completes **then** every despawn lands on the
    identical Tick, including when `session.linkdead_*` was retuned between the run and the replay:
    the durations come from each `MarkLinkdead`, not from the replaying binary's config.
15. **Given** a graceful drain (SIGTERM) **when** the Gateway tears down its Sessions **then** each
    playing Session produces `MarkLinkdead`, not `UnbindCharacter{QUIT}`, and no Character despawns
    because of the drain. A revoked credential tears down as `CloseSession` does.
16. **Given** an Account whose Character is linkdead **when** a Session selects a *different*
    Character of that Account **then** it is `FAILED_PRECONDITION already_live` naming the linkdead
    Character, and nothing is produced.
17. **Given** a `MarkLinkdead` for a body that is already linkdead, dormant, or absent **when** it is
    applied **then** nothing changes and nothing is emitted.

## Interface contract

### Gateway Session states

```
connected ─authenticate─▶ authenticated ─SelectCharacter─▶ playing ─stream drop─▶ linkdead(gateway)
                                                              ▲                        │
                                                              └── SelectCharacter(same) ┘   (within grace)
playing ─CloseSession─▶ closed          linkdead(gateway) ─grace expiry Event─▶ closed
```

The Gateway's linkdead state is bookkeeping; the World's is authoritative. On stream drop the Gateway
produces `MarkLinkdead`; on the `CharacterDespawned` Event it closes the Session.

The wire and record shapes are on `main` in `docs/specs/protocol/` (contract review, 2026-09-26), and
the comments there are the contract:

- `andara.log.v1.LoggedCommand`: `MarkLinkdead mark_linkdead = 18` carrying `character_id`,
  `grace_ticks`, `extension_ticks`, `max_ticks`. The Gateway converts the three `session.linkdead_*`
  durations at `sim.tick_rate` when it produces it.
- `BindCharacter` (15) doubles as reconnect: applied to a linkdead body it zeroes the four linkdead
  fields and emits `CharacterReconnected`. Applied to a present body that isn't linkdead it does what
  `AW-SRV-014` says (taken where it stands, nothing emitted).
- `andara.game.v1.EventEnvelope`: `character_linkdead = 20`, `character_reconnected = 21`,
  `character_despawned = 22`, each carrying `zone_id`, `room_id`, `character_name`, like
  `CharacterArrived`. No `character_id`, no deadline. `CharacterDespawned.reason` is a string:
  `quit`, `switch`, `linkdead`, `linkdead_ceiling`.
- `RoomDescribed.linkdead = 7`: the linkdead subset of `occupants`.

Teardown, by how the Session ends:

| End | Produced | Body |
|-----|----------|------|
| `CloseSession` | `UnbindCharacter{QUIT}` | dormant, `CharacterDespawned{quit}` |
| credential revoked | `UnbindCharacter{QUIT}` | dormant, `CharacterDespawned{quit}` |
| stream closed or keepalive miss | `MarkLinkdead` | linkdead, `CharacterLinkdead` |
| drain (SIGTERM) | `MarkLinkdead` | linkdead, `CharacterLinkdead` |
| deadline or ceiling Tick | nothing; the sim despawns | dormant, `CharacterDespawned{linkdead\|linkdead_ceiling}` |

```go
// CONTRACT SKETCH — not an implementation
package sim
// Called by any Apply that constitutes a combat interaction against target.
// Whichever epic defines combat calls this and nothing else about linkdead.
func (e *Engine) OnCombatInteraction(target EntityID)
```

`EntityState` (`AW-SRV-006`) carries `linkdead_deadline_tick` (4), and gains `linkdead_since_tick` (7),
`linkdead_ceiling_tick` (11) and `linkdead_extension_ticks` (12). All four are hashed, and all four
go in the snapshot body (`snapshot_codec_test.go`'s field count moves with them).

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `session.linkdead_grace` | `ANDARA_LINKDEAD_GRACE` | `180s` | ADR-0006; → Ticks at `sim.tick_rate` |
| `session.linkdead_combat_extension` | `ANDARA_LINKDEAD_COMBAT_EXTENSION` | `60s` | |
| `session.linkdead_max` | `ANDARA_LINKDEAD_MAX` | `300s` | ceiling |
| `session.linkdead_detect` | `ANDARA_LINKDEAD_DETECT` | `5s` | stream keepalive miss before `MarkLinkdead` |
| `recovery.rto_target` | `ANDARA_RECOVERY_RTO_TARGET` | `60s` | for the invariant only; `slo/recovery.md` |
| `egress.assumed_event_rate` | `ANDARA_EGRESS_ASSUMED_EVENT_RATE` | `5` | Events/s per Session for the AC-7 check; 2048 ≥ 300 × 5 |

Startup asserts, in order, and exits `1` on the first failure:
`linkdead_max >= linkdead_grace`, `linkdead_grace > recovery.rto_target`,
`egress.resume_window >= linkdead_max × egress.assumed_event_rate`, `auth.session_ttl > linkdead_max`.

### Error taxonomy

`ErrInvariant{Relation, Values}` at boot. *(`ErrNotLinkdead` is struck at contract review: a sim that
rejected `BindCharacter` on a present, non-linkdead body would break `AW-SRV-014`'s crash path and its
idempotent retry after `DEADLINE_EXCEEDED`. The Gateway's live flag is the one-live guard, as
`AW-SRV-014` records; the sim has no Session to compare against.)*

## Data / state impact

The four linkdead fields are Zone state, hashed and snapshotted; `state_version` bumps by one with a
zero-fill migration. Deadlines are Ticks so replay is exact; a
wall-clock deadline would make a recovered World differ from the one players were in.

`session.linkdead_detect` is the one wall-clock number, and it is on the Gateway side of the log: it
decides *when* `MarkLinkdead` is produced, and the log then fixes the Tick.

## Observability requirements

### Metrics
- `andara_sessions_linkdead` — gauge, label `in_combat` (`true`/`false`).
- `andara_linkdead_outcomes_total` — counter, label `outcome` (`reconnected`, `despawned`, `ceiling`,
  `died`, `quit`).
- `andara_linkdead_duration_seconds` — histogram, label `in_combat`.
- `andara_linkdead_combat_extensions_total`, `andara_linkdead_ceiling_despawns_total` — counters.
- `andara_reconnect_resyncs_total` — counter; the AC-7 signal in production.

### Logs
- `info` on linkdead entry, reconnect, despawn with `session_id`, `character_id`, `outcome`,
  `deadline_tick`, `trace_id`.

### Traces
- `linkdead.enter` and `linkdead.reconnect` as span events on `session.lifetime`.

### Alerts
None of its own. A rising linkdead rate is a symptom tied to the Session availability SLO
(`AW-SRV-011`, `EPIC-07`).

## Test plan

- **Unit:** deadline arithmetic for AC-10–12 as a table over Ticks; startup invariant table (AC-7, AC-8);
  state machine transitions including `CloseSession` while already linkdead.
- **Integration:** drop-and-reconnect inside the window against `AW-SRV-011`'s stream (AC-2);
  a keepalive miss with no transport close, through a proxy that stops forwarding (AC-1);
  drop-and-expire (AC-3, AC-4); quit then an immediate select, 20 times, never `already_live`
  (AC-5); kill mid-grace and recover by full-log replay (AC-6); replay determinism of despawn Ticks
  under a retuned config (AC-14) using a fixture combat verb that calls `OnCombatInteraction`; drain
  with two playing Sessions (AC-15).
- **Manual/operator:**
  ```
  andara-cli play            # select a Character, then kill the client
  andara-cli play            # within 180 s: expect "reconnected", no gap in output
  # second terminal, same Room: expect "<name> (linkdead)" in look, then "<name> reconnects"
  ```

## Definition of done

CLAUDE.md §8, plus: the restart-mid-grace test and the startup invariant test. `make stack-linkdead`
(`AW-INF-017`) is the live observation of `andara_linkdead_outcomes_total{outcome="reconnected"}` and
`andara_sessions_linkdead` from the running server.

## Open questions

- **Resolved 2026-09-11 (Brian): linkdead is visible.** AC-1's marker and Event are that decision.
- **Combat extends the timer, bounded by a ceiling** (ADR-0006): 60 s / 300 s, retuned once combat
  pacing exists. `andara_linkdead_ceiling_despawns_total` says when the ceiling is too short.
- `[ASSUMPTION]` A linkdead Character is inert: it takes damage, it does nothing. ADR-0006's reading.
- `[ASSUMPTION]` `session.linkdead_detect` 5 s. The gRPC keepalive from `AW-SRV-005` is the detector.
- **Inherited from `AW-SRV-010` (2026-09-19):** the ingress forgets a Session when its context
  ends — `Ingress.forget` drops its queue, its rate-limit bucket, and its `Bindings` entry. A resumed
  Session (linkdead grace) is therefore unbound on the Gateway and must be re-bound from the
  Character it drives (`AW-SRV-014`'s binding state) before its first Submit, or that Submit is
  `not_authorized` ("you are not in the world"). Resume also lands the Session on one pod; `AW-SRV-031`'s
  idempotency window is per process and relies on that.
- **Inherited from `AW-SRV-011` (flip to `review`, 2026-09-21):** the egress retains a Session's
  sent Events in a ring `egress.resume_window` deep, keyed by **Session**, fed by a pump goroutine
  that holds the Session's one Hub subscription for the Session's life. A linkdead reconnect is a
  *new* Session selecting the same Character, so this story moves the key to the Character and
  keeps the ring and pump alive for `linkdead_grace` after the stream drops — they were built to be
  handed over, not rebuilt — or every linkdead resume is a `Resync{no_history}`. Consequence to
  own: `events.max_subscribers` bounds Sessions that have *ever* subscribed (each a pump and up to
  `resume_window` envelopes), not concurrent streams, and linkdead makes Sessions linger; the
  number is this story's to size with AC-7's invariant.

## Contract review (architecture, 2026-09-26)

Stays `ready`. The answers to `docs/feedback/AW-SRV-015-linkdead.md` and to §3 of
`docs/feedback/AW-SRV-014-character-roster.md` are in the body above; this is the index.

1. **Numbers.** `mark_linkdead = 18`; the Events 20–22; `EntityState` 7, 11, 12;
   `RoomDescribed.linkdead = 7`. Landed in `docs/specs/protocol/` and `gen/` in this review, so
   nothing else can take them.
2. **AC-9 moved to `AW-SRV-007`**, as an inherited Definition-of-done line. AC-6 is stated against
   full-log replay. `depends_on` stays `[AW-SRV-014]`, and the story can close in SPRINT-02.
3. **The Events carry `zone_id`, `room_id`, `character_name`**, no `character_id` and no deadline.
   `reason` is a string, as every client-facing reason is. `(linkdead)` in `look` is a field,
   `RoomDescribed.linkdead`, not a suffix on a name in `occupants`.
4. **SRV-014 §3, `already_live` after a clean quit:** `CloseSession` answers after the teardown's
   produce and the flag release (AC-5). Not after apply: log order already puts the next
   `BindCharacter` behind the unbind in the same Partition.

Found in review, beyond the three items:

5. **The durations ride in `MarkLinkdead`.** A grace read from config at apply makes a replay under
   a retuned config despawn on different Ticks and fail its State Hash (AC-14). This is why the state
   has four fields, not two.
6. **`ErrNotLinkdead` struck** (Error taxonomy).
7. **Teardown by cause** (the table): a drain must be linkdead, or every deploy despawns the map,
   which is the thing the grace exists to prevent (AC-15).
8. **Another Character while one is linkdead is `already_live`** (AC-16). ADR-0006 calls
   `linkdead_max` the combat-logging knob. Switching Characters would be a way around it.
9. **`CharacterDespawned` replaces `CharacterLeft{to_direction: ""}`** on an unbind, so a
   bystander reads one line on a quit, not two. `AW-CLI-004`'s `leaves.` row for an empty direction
   stays for older servers.
