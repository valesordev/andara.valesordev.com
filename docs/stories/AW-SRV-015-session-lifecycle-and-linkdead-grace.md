---
id: AW-SRV-015
title: Session lifecycle and linkdead grace period
epic: EPIC-08
component: server
type: feature
status: done
size: M
depends_on: [AW-SRV-014]
blocks: [AW-CLI-008, AW-INF-017, AW-SRV-007]
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
   that tick, marks it dormant with its position, emits `CharacterDespawned{reason="linkdead"}`, and it is
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
    `reason="linkdead_ceiling"` and `andara_linkdead_ceiling_despawns_total` increments.
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
go in the snapshot body. *(Amended 2026-09-26, `AW-SRV-006`'s second §8 pass: #102 replaced the
field count with a tripwire over the proto descriptors, `TestBodyHashCoversEveryProtoField`. Until
this story hashes the four, `sim.BodyStateHash` refuses a body carrying any of them non-zero.)*

How they're hashed:
- `EntityCanonicalBytes` writes a linkdead record, holding all four fields, **only for a body
  whose `linkdead_deadline_tick` is non-zero**. This works the way the dormant record works.
- A World with no linkdead body therefore hashes exactly as it does today. The golden sequence
  and the determinism tests don't move.
- This story removes the four from `BodyStateHash`'s refusal, and the tripwire must pass with
  them covered.
- A body with `linkdead_deadline_tick` zero and any other of the three non-zero is not a state
  the sim produces. `BodyStateHash` keeps refusing it, the way it refuses `dormant_since_tick`
  on a body that isn't dormant.

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

The four linkdead fields are Zone state, hashed and snapshotted. *(Amended at architecture's §8
review, 2026-09-26: `state_version` does **not** bump. The fields are additive, and zero means
"not linkdead" before and after, so ADR-0007 rule 2 doesn't apply; the ruling is in the §8 record.)* Deadlines are Ticks so replay is exact; a
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
- ~~`[ASSUMPTION]` A linkdead Character is inert: it takes damage, it does nothing. ADR-0006's reading.~~
  **Resolved 2026-09-26 (Brian, via PM in #122): wholly inert.** It takes damage and neither
  defends itself nor flees. That is what this story built. ADR-0006's `[NEEDS BRIAN]` is closed
  with a dated note.
- ~~`[ASSUMPTION]` `session.linkdead_detect` 5 s. The gRPC keepalive from `AW-SRV-005` is the detector.~~
  **Resolved at §8, 2026-09-26:** built and tested (`TestKeepalive_APartitionedClientIsLinkdead`).
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
   `AW-SRV-007` takes this story in its `depends_on`, and this story's `blocks` names it.
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

## Verification (implementation, 2026-09-26)

On `impl/aw-srv-015-linkdead`. Commits: `0430f7e` (sim), `b18f57e` (configuration), `3702e9e`
(Gateway and roster), `ca4da42` (metrics and logs), `98aad40` (keepalive), `16cbefa` (egress
handover), `1f01f09` (end to end).

| AC | Evidence |
|----|----------|
| 1 | `TestMarkLinkdead_KeepsTheBodyAndTellsTheRoom` (sim). `TestKeepalive_APartitionedClientIsLinkdead`: through a proxy that stops forwarding without closing, torn down as linkdead within `linkdead_detect`, while an idle client answering its pings is untouched; mutation-checked. `TestRun_Linkdead`: a real drop, and `look` with `occupants` and `linkdead` |
| 2 | `TestBind_ReconnectsALinkdeadBody` (sim), `TestRoster_LinkdeadReconnectAndAlreadyLive`, `TestRoster_ReconnectWaitsForTheLinkdeadTeardown`, `TestLinkdead_ReconnectResumesWithNoGap` (egress; mutation-checked). `TestRun_Linkdead`: the stream resumes from `last_event_id` with what the Room did in between, then `CharacterReconnected` |
| 3, 4 | `TestLinkdead_DespawnsAtTheDeadline` (sim). `TestRun_Linkdead`: the grace runs out, the Account is free, and the body wakes where it was |
| 5 | `TestUnbind_MakesTheBodyDormant` (now `CharacterDespawned{quit}`), `TestCloseSession_WaitsForTheTeardown` (mutation-checked). `TestRun_Linkdead`: 20 quits each followed at once by a select, none `already_live` |
| 6 | `TestLinkdead_ReplayDespawnsOnTheSameTick`: a fresh engine replays the log to a kill mid-grace, holds the four fields exactly, and despawns on the same Tick with every State Hash equal. This is at the sim level (`Engine.Replay`, the function `tickloop.Recover` drives). There is no broker-level kill test on this branch |
| 7, 8 | `TestParse_LinkdeadInvariants`: each relation, in order, first failure wins, the relation and every value named. `TestLinkdead_ReconnectPastTheWindowIsCounted` for `andara_reconnect_resyncs_total` |
| 9 | Moved to `AW-SRV-007` at contract review |
| 10, 11, 12 | `TestOnCombatInteraction_RefreshesTheDeadline` (a table over the blow's Tick), `TestLinkdead_SustainedAttackDespawnsAtTheCeiling`, `TestLinkdead_OneBlowThenSilenceDespawnsAnExtensionLater`, all on the fixture combat verb `simtest.Strike` |
| 13 | **Not exercisable yet:** there are no death rules. `andara_linkdead_outcomes_total{outcome="died"}` is declared and pre-seeded, and the first story with lethal damage owns the assertion |
| 14 | `TestLinkdead_ReplayDespawnsOnTheSameTick`. The sim reads no configuration to apply a `MarkLinkdead`: the durations are the record's |
| 15 | `TestRoster_ReleasedBeforeTheContextIsCanceled` (drop → linkdead, close → quit). `TestRun_M1Gate`: a drain with two playing Sessions logs two `character marked linkdead` and no unbind |
| 16 | `TestRoster_LinkdeadReconnectAndAlreadyLive`, `TestRun_Linkdead` |
| 17 | `TestMarkLinkdead_IsANoOpOnABodyItCannotMark`: already linkdead, dormant, absent |

**Deviations and open items:**
- **Hashing follows the amended contract** (architecture's note from `AW-SRV-006`'s second §8
  pass). The linkdead record is written only for a body with a non-zero `linkdead_deadline_tick`.
  `BodyStateHash` refuses a body with the other three fields and no deadline. The tripwire passes,
  and the refusal is mutation-checked.
- **`state_version` is not bumped.** The four fields are hashed only when set, so every existing
  hash and log stays valid. The reason, and the one-line change if you want the bump anyway, are in
  `docs/feedback/AW-SRV-015-linkdead.md` under "Implementation, 2026-09-26". Architecture decides.
- **The inherited `AW-SRV-011` line is delivered** as parking rather than re-keying by Character.
  The ring and the pump outlive the Session for `linkdead_max`, and the reconnecting Session adopts
  them. `events.max_subscribers` now counts parked subscriptions too.
- **The inherited `AW-SRV-010` line holds:** the reconnect re-binds the routing table before its
  `BindCharacter`, as any select does.
- **A drop mid-crossing** waits for the crossing to settle and marks the Zone the body arrived in
  (review of #114, `TestRoster_LinkdeadMidCrossingMarksTheArrivalZone`). Only a crossing that outlasts
  `ingress.transit_hold` falls back to the Zone it left, where the mark is a no-op. That body stays
  present with no Session, and the roster's hold keeps it for the reconnect.
- **The hold has no wall-clock bound** (review of #114). The sim's `LinkdeadEnded` frees it, and so
  does a reconnect. A failed reconnect restores it (`TestRoster_FailedReconnectRestoresTheHold`,
  `TestRoster_FailedReconnectAfterTheDespawnHoldsNothing`). Both are mutation-checked.
- **`deploy/helm/andara/values.schema.json` and `templates/_env.tpl`** are regenerated by
  `make values-schema` from `keys.yaml`, which already declared the five keys. That's a diff under
  `deploy/`, flagged on the PR.
- **DoD's live observation** of `andara_linkdead_outcomes_total{outcome="reconnected"}` and
  `andara_sessions_linkdead` from the running server is `make stack-linkdead` (`AW-INF-017`).
  `TestRun_Linkdead` scrapes both from the server's own `/metrics`.

## §8 review (architecture, 2026-09-26)

On `arch/sprint-02-review-3`, against `main` at `5c5d83c`. The live run is `make stack-linkdead` (#119). **Stays `review`** on three items:
- implementation's: the despawn log line at the deadline (below, and
  `docs/feedback/AW-SRV-015-linkdead.md` "§8, 2026-09-26");
- implementation's: #121, `TestRun_Linkdead` intermittently gets `Resync{no_history}` on the
  reconnect's resume (seen in CI on #119). That's AC-2 failing, so it's a product race until
  shown otherwise;
- Brian's: whether a linkdead Character is inert (ADR-0006's `[NEEDS BRIAN]`).

| §8 item | Holds? | Evidence |
|---------|--------|----------|
| Every AC passes | yes, AC-13 excepted | The verification table's tests, all present and passing. AC-6 is proven at the sim level (`Engine.Replay`, which `tickloop.Recover` drives); the broker-level kill is `AW-SRV-007`'s, with AC-9. AC-13 has no death rules to exercise: the first story with lethal damage carries `andara_linkdead_outcomes_total{outcome="died"}` as an inherited line |
| Tests run in CI | yes | All in `make test` (`go test -race ./...`); `TestRun_Linkdead` needs no stack. `make stack-linkdead` (`AW-INF-017`) runs in the `stack` workflow |
| `make check` | yes | clean on `main` at `5c5d83c` |
| Instrumentation, live | **no, one line** | Metrics: `make stack-linkdead` against a stack built from `5c5d83c` read `andara_linkdead_outcomes_total{outcome="reconnected"}` +1 and `andara_sessions_linkdead` up by one, then back, from the server's `/metrics`. Traces: Tempo holds `session.lifetime` spans carrying `linkdead.enter` and `linkdead.reconnect` from that run. Logs: entry, reconnect and quit take the LoggedCommand's traceparent, not the tick loop's, so they don't repeat `AW-SRV-014`'s defect. **The despawn at the deadline or ceiling logs `session_id=""` and `trace_id=""`** (`sim/linkdead.go` `expireLinkdead` → `despawn`) |
| Config documented | yes | Six keys in `server/README.md` and `keys.yaml`, env names and defaults matching; `values-schema-check` current |
| Migrations | yes, no bump | Ruling below |
| Glossary | yes, amended here | Resume Window and Egress rewritten for parking; Parked Subscription and Linkdead Hold added |
| No `[ASSUMPTION]` | **no** | `linkdead_detect` is resolved; inertness is Brian's, below |

**`state_version` does not bump** (implementation's question in the feedback file). ADR-0007 rule 2
moves `state_version` for a change protobuf can't absorb, a change in what existing state means.
The four fields are additive, a zero in each means "not linkdead" before and after this story, and
the linkdead record is only written when the deadline is set, so every existing hash, boundary and
snapshot keeps its value. A bump would buy nothing and would make every existing log
unreplayable, since nothing replays across a version change. Data / state impact is amended to
match.

**The two `[ASSUMPTION]`s:**
- *`linkdead_detect` 5 s, with the gRPC keepalive as the detector:* resolved. Built and tested:
  `TestKeepalive_APartitionedClientIsLinkdead` marks a partitioned client within it.
- *A linkdead Character is inert:* **open.** ADR-0006 marks it `[NEEDS BRIAN]`; it implies inert
  but doesn't decide it. Sent to PM for Brian's game-design batch. It doesn't change the contract,
  and a "yes, inert" closes it with no code change. *(Corrected in review of #120: this record first
  called it ADR text.)*

**Item 4, the despawn line at the deadline**, is a contract clarification plus a small fix:
- `session_id` is the Session that went linkdead, which the roster's hold keeps. A body recovered
  linkdead after a restart (`Roster.SeedLinkdead`) has no Session: none survives a restart, and
  nothing in Zone state names one. Its expiry line omits `session_id` and carries
  `recovered=true`, with `character_id` and `deadline_tick` as the correlation.
- `trace_id` is the trace of the tick span that applied the expiry. No request is in flight
  at expiry, and the tick is the traced unit (§7). An empty field is what the contract rules out.
- Assert both cases in a test, the held and the recovered, so neither regresses silently.

A nit, not blocking: `Entity.Linkdead()` keys on `LinkdeadSince != 0` while the hash keys on the
deadline. They agree because no Command applies at Tick 0. Keying both on the deadline would drop
that reasoning.

## §8 review, second pass (architecture, 2026-09-26)

On `arch/sprint-02-review-4`, against `main` at `6561cde`. **Stays `review`** on #127, a lock-order
inversion #124 introduced (below). The three items the first pass held it on are closed:

| Item | Closed by | Checked |
|------|-----------|---------|
| The despawn line at the deadline or ceiling | #123 (`389a717`) | The roster fills `session_id` from the linkdead hold, or from a reconnect that took it over. A body recovery left carries `recovered=true` and no `session_id`. The tick loop stamps each linkdead step that has no Command with the `sim.tick` span's traceparent. `TestRoster_ExpiryLine` asserts both cases, and `TestRun_Linkdead` asserts the held line end to end. `Entity.Linkdead()` now keys on the deadline, as the hash does, which closes the nit |
| #121, the reconnect's `Resync{no_history}` | #124 (`393c654`, `ef86c73`) | The park now happens in `ParkSession`, which the gateway calls before it cancels the Session. It no longer happens in `forget`, whose goroutine nothing ordered against the reconnect's `Subscribe`. The race was proven by widening the window (live-assertions rule 4): a 3 s delay before `forget` failed every run on the old code and passes on the new. `TestLinkdead_ReconnectBeforeTheOldSessionEnds` holds the window open deterministically. A stale `Rebind` of the lost Session leaves parked or adopted state alone (`TestLinkdead_StaleRebindLeavesTheParkedRing`). **But #124 inverts a lock order (#127):** `ParkSession` takes `e.mu` then `s.rebind`, while `session()` holds `s.rebind` and, on a failed first subscribe, calls `discard`, which takes `e.mu`. *(This record first said nothing takes `e.mu` under `s.rebind`. That was wrong, and review of #126 caught it.)* `server/egress` and `server/roster` pass `-race -count=10`, and `TestRun_Linkdead` passes `-race -count=5` |
| Inertness | #122, Brian: wholly inert | Open questions above; ADR-0006 note |

The rest of the first pass's table stands. `make stack-linkdead` runs in the `stack` workflow and
passed on `main` at `8ab863a`. It passed again locally on a stack that `make up` built from `6561cde`,
with #123 and #124 in it. The server logged `character linkdead` and `character reconnected`
with their Sessions and non-empty `trace_id`s. The run doesn't wait out a grace, so it doesn't
show the expiry line live; the tests above assert it.

**Holding it: #127.** A playing Session's first `Subscribe` that fails while the same connection
is parked can deadlock the egress. Each side holds the lock the other waits on, and `e.mu` then
blocks every later stream operation in the process. It needs one lock order and a test that holds
the window open, and then this story flips.

Two lines are carried, not deferred:
- **AC-13 (death while linkdead)** has no carrier yet: no story or epic defines lethal damage.
  It's sent to PM (feedback file) to record as an inherited Definition-of-done line wherever
  combat is first groomed. `andara_linkdead_outcomes_total{outcome="died"}` is declared and
  pre-seeded meanwhile.
- **AC-9 and the broker-level kill mid-grace** are `AW-SRV-007`'s (its inherited line), with
  snapshot recovery. AC-6 is proven here by full-log replay at the sim level.

## §8 review, third pass (architecture, 2026-09-27): `done`

On `arch/sprint-02-review-5`, against `main` at `3d34212`. The one item holding it, #127, is closed
by #131 (`81dd3a4`).

**The fix.** The discard now runs in `release`, deferred ahead of `s.rebind.Unlock`, so it runs
after the unlock. Every path now takes `e.mu` → `s.rebind` → `s.mu`. `rebindSession`, `resubscribe`
and `close` take nothing under `s.rebind` except `s.mu`, the Hub and the Observers.

Review of #131 found the race the fix opened: a second Subscribe waiting on `s.rebind` could
subscribe on state the first one's discard then dropped. `users` closes it:
- The count is taken under `e.mu`, and only the last one out discards.
- A `Rebind` of state that never subscribed leaves it alone.

**The tests.** Three hold windows open per `live-assertions.md` rule 4:
- `TestLinkdead_FailedFirstSubscribeRacingThePark` hung 3 of 3 on the old code.
- `TestLinkdead_FailedFirstSubscribeLeavesAConcurrentOneTracked` covers the second Subscribe.
- `TestLinkdead_RebindLeavesNeverSubscribedStateAlone` covers the Rebind.

| §8 item | Holds? | Evidence |
|---------|--------|----------|
| Every AC passes | yes, AC-13 excepted | As the first pass recorded. AC-13 is carried (below) |
| Tests run in CI | yes | `make test`. `server/egress` and `server/roster` pass `-race -count=10` on `3d34212` |
| `make check` | yes | clean on `3d34212` |
| Instrumentation, live | yes | `make stack-linkdead` passed on a stack `make up` built from `3d34212`. In Loki, `character linkdead` and `character reconnected` carry the LoggedCommand's `trace_id`, and each one resolves in Tempo. The three `session closed{outcome=dropped}` lines resolve to their `session.lifetime` span, which #132 fixed (`AW-SRV-014`'s pass). The expiry line isn't shown live: the run doesn't wait out a grace. `TestRoster_ExpiryLine` covers it, and #132 keeps the tick that applies an expiry |
| Config documented | yes | unchanged from the first pass |
| Migrations | yes, no bump | first pass's ruling |
| Glossary | yes | first pass |
| No `[ASSUMPTION]` | yes | inertness answered in #122 |

**Carried, as inherited Definition-of-done lines** (the second pass):
- **AC-13** goes to the first story with lethal damage. The feedback file sends it to PM.
- **AC-9 and the broker-level kill mid-grace** go to `AW-SRV-007`.
