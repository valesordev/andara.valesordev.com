---
id: AW-SRV-014
title: Character creation, selection, and binding — a Session enters the World
epic: EPIC-08
component: server
type: feature
status: in-progress
size: M
depends_on: [AW-SRV-008, AW-SRV-022]
blocks: [AW-SRV-015, AW-SRV-032, AW-CLI-007]
lane: implementation
risk: medium
---

## Context

ADR-0006 decided up to five Characters per Account with **exactly one live at a time**, confirmed by
Brian on 2026-09-07. This story is the half of the roster M1 needs: create a Character, choose it,
and bind it to a Session so the Session is *in the World* — the one thing that stands between what has
shipped and M1's gate (a human runs `andara-cli play`, sees a Room, types `north`, sees a different
Room, the Command having gone through Kafka). Every other M1 story is `done` or in `review`, and today
every live Submit is refused at `authorize` with "you are not in the world".

**Re-groomed 2026-09-21 (split).** This story was sequenced into M2 behind `AW-SRV-007` on the
reasoning that "a roster that survives a restart requires recovery to work first". It does not: replay
recovery works from M1 (the roadmap says so — slow, and correct), a dormant body is Zone state rebuilt
by replay like every other Entity, and name uniqueness is Account state in `AW-SRV-008`'s store, which
is already durable. What genuinely wants recovery and retention machinery — deletion, the 30-day
retention sweep, switching bodies, purging — is `AW-SRV-032`, M2. This story keeps the contract it
had for the parts it keeps; nothing here is a throwaway that 032 replaces.

The one awkward consequence of ADR-0006's Account/World split stands: a Character's *identity* is
Account state and its *body* is World state, and name uniqueness holds across that boundary. This
story designs that seam.

## User story

As a player, I want to create a character and step into the world as it, so that `andara-cli play`
shows me a Room and my commands move me through it.

## Scope

### In scope
- Roster RPCs on `Game`: `ListCharacters`, `CreateCharacter`, `SelectCharacter`.
- The roster cap of five and the one-live rule, enforced at the Gateway against Account and Session
  state before anything reaches the log.
- Globally unique, immutable, case-insensitively reserved Character names.
- Binding: `SelectCharacter` produces a `BindCharacter` Command; the sim materializes the Character
  at `character.spawn_room` (never bound before) or at its dormant position, and emits its arrival.
- Unbinding on Session end: the Gateway produces `UnbindCharacter{QUIT}`; the sim makes the body
  dormant and emits its departure. A quit is visible to the Room.
- Dormant Characters: a Character not in the World keeps its last position in Zone state, so "where
  you were" survives a restart and a replay.
- Spawn placement at `character.spawn_room` — `town/plaza` for the dev content (Brian, 2026-09-21).

### Out of scope
- Deletion, name reservation on delete, the retention sweep, `PurgeCharacter`, and switching bodies
  within a Session — `AW-SRV-032`.
- Linkdead and reconnect — `AW-SRV-015`. Until it lands, a Session's end is a quit: the body goes
  dormant at once.
- Authentication — `AW-SRV-008`. The CLI's side of this — `andara-cli character`, `play --character` —
  is `AW-CLI-007`.
- Appearance, class, stats, inventory. A Character instantiates the `andara.core.Character` Template
  (ADR-0010, `AW-SRV-022`); what components that Template carries is Brian's and is additive to this
  contract.

## Acceptance criteria

1. **Given** an Account with five Characters **when** a sixth is created **then** `CreateCharacter`
   returns `RESOURCE_EXHAUSTED` with reason `roster_full` naming the cap, and nothing is written.
2. **Given** a name reserved by any Character on any Account **when** creation uses it in any letter
   case or NFKC-equivalent form **then** it returns `ALREADY_EXISTS` with reason `name_taken` and one
   fixed message; the name is not written.
3. **Given** a name that fails `character.name_pattern` **when** creation is attempted **then**
   `INVALID_ARGUMENT` with reason `name_invalid` names the rule, and the name is not reserved.
4. **Given** an Account with a live Character on one Session **when** a Character is selected on
   another Session **then** `SelectCharacter` returns `FAILED_PRECONDITION` with reason `already_live`
   naming the live Character, and nothing is produced.
5. **Given** a never-bound Character selected **when** the `BindCharacter` Command applies **then** the
   Character is instantiated from `andara.core.Character` at `character.spawn_room`, a
   `CharacterArrived{from_direction: ""}` is emitted with Room scope, and the Session's next `look`
   is answered with that Room, `Here:` naming the Character.
6. **Given** a dormant Character selected **when** `BindCharacter` applies **then** it is placed at its
   dormant position — the Room it was in when it went dormant — not at the spawn Room, and its arrival
   is emitted there.
7. **Given** two concurrent `SelectCharacter` calls for the same Character from two Sessions **when**
   both arrive **then** exactly one produces a `BindCharacter` and the other returns
   `FAILED_PRECONDITION` `already_live`.
8. **Given** a bound Session **when** it ends — `CloseSession`, or its connection drops — **then** an
   `UnbindCharacter{reason: QUIT}` is produced, the body is dormant on apply, a
   `CharacterLeft{to_direction: ""}` is emitted with Room scope, the Room's occupants no longer name
   it, and `andara_characters_total{state="present"}` decrements.
9. **Given** a server crash with one Character present and one dormant — so no `UnbindCharacter`
   for the present one is in the log — **when** the log is replayed from the beginning **then** the
   present body is present at the Room it was in, with no Session; the dormant one is dormant where
   it was; `ListCharacters` shows both with their Zones; and the Account's next `SelectCharacter` of
   the present one succeeds (AC-11) and its `look` shows that Room.
10. **Given** a bound Session **when** it submits `north` **then** the Command is produced to the
    Character's Zone partition, applies in the tick, and the Session's stream carries the move — the
    M1 gate, end to end, through Kafka.
11. **Given** a `BindCharacter` for a body that is already present — a crash left it, or the Session's
    teardown produce failed, or a `SelectCharacter` was retried after `DEADLINE_EXCEEDED` **when** it
    applies **then** the Session takes the body where it stands, no arrival is emitted (the Room never
    saw it leave), and the State Hash is unchanged by the apply.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation; additions to andara/game/v1/game.proto
rpc ListCharacters(ListCharactersRequest) returns (ListCharactersResponse);       // session_id
rpc CreateCharacter(CreateCharacterRequest) returns (CreateCharacterResponse);    // session_id, name → CharacterSummary
rpc SelectCharacter(SelectCharacterRequest) returns (SubmitResponse);             // session_id, character_id; the BindCharacter offset
message CharacterSummary { string character_id = 1; string name = 2; CharacterStatus status = 3;
                           string zone_id = 4; string room_id = 5; bool live = 6; int64 created_unix = 7; }
enum CharacterStatus { CHARACTER_STATUS_UNSPECIFIED = 0; ACTIVE = 1; DELETED = 2; }   // DELETED: AW-SRV-032

// additions to andara/accounts/v1/account.proto (AW-SRV-008); 11 and 12 are free
message Account { /* … */ repeated CharacterRef characters = 11; }   // sorted by character_id
message CharacterRef { string character_id = 1; string name = 2; CharacterStatus status = 3;
                       string zone_id = 4; string room_id = 5; int64 created_unix = 6; }
message NameReservation { string character_id = 1; string account_id = 2; }   // key: name/{fold(name)}

// additions to andara/log/v1/log.proto LoggedCommand oneof — 13 and 14 are AW-SRV-028's
BindCharacter   bind_character   = 15;   // character_id, account_id, name; spawn RoomRef when never bound
UnbindCharacter unbind_character = 16;   // character_id, reason: QUIT (SWITCH: AW-SRV-032; LINKDEAD: AW-SRV-015)
```

- `DeleteCharacter`, `PurgeCharacter` (arm 17), `CharacterRef.deleted_unix`, and `DELETED` in use are
  `AW-SRV-032`'s; the enum value is declared here so the field never changes shape.
- **Spawn and despawn are `CharacterArrived` and `CharacterLeft` with an empty direction** — the form
  `event.proto` already documents as "did not come through an Exit". No `CharacterSpawned` /
  `CharacterDespawned` types: the Hub's Observer-follow keys on `CharacterArrived` addressed to the
  Entity, so a spawn places the Session's perception with no new rule; `AW-CLI-004` renders the
  empty form ("Aldric arrives." / "Aldric leaves.") already. *(Corrects the earlier sketch.)*
- `Bindings.Bind(session, command.Binding{Actor, Zone, Room})` is called by `SelectCharacter`
  **before** the produce, with the roster's last-known Zone and Room (spawn Room when never bound), so
  the arrival is routed to the Session and `Egress.Rebind` sees a perception it can compare
  (`Room` carried: this resolves the inherited `AW-SRV-011` question below in favor of carrying it).
  `UnbindCharacter` is produced by the Session's teardown (`gateway.Session` close, any outcome) and
  `Bindings.Unbind` runs at the same moment; the sim's apply is what makes the body dormant.
- **`BindCharacter` on a present body is idempotent** (AC-11): the Gateway cannot guarantee an
  `UnbindCharacter` for every Session — a crash tears nothing down, and a teardown while the ingress
  is read-only is refused — so after replay a body can be *present without a Session*. Nothing in the
  log makes it dormant, and a boot-time sweep would be non-deterministic under replay; the rule is
  that the next `SelectCharacter` takes it where it stands. `AW-SRV-015` turns this state into
  linkdead-then-despawn. The same rule makes a `SelectCharacter` retried after `DEADLINE_EXCEEDED`
  safe — it carries no `client_ref`, so `AW-SRV-031`'s table does not cover it.
- **The teardown produce** of `UnbindCharacter{QUIT}` runs on its own context (the Session's is
  gone), bounded by `ingress.produce_deadline`; a failure is logged at `warn` with `session_id`,
  `character_id`, counted on `andara_character_unbinds_total{reason="quit",outcome="produce_failed"}`,
  and not retried — the body stays present and the rule above applies.
- **The old Session's teardown is what frees the Character.** Until it has run, a `SelectCharacter`
  from the same Account is `already_live` — including a reconnecting client's, which can arrive
  before the server has noticed the old connection die. `AW-CLI-007` retries it on the reconnect
  backoff; nothing here shortens the window (`AW-SRV-015`'s `linkdead_detect` does).
- The roster's `zone_id`/`room_id` are the Gateway's last knowledge of the body: written at create
  (spawn Room), and at unbind from `Bindings`' current entry (which follows transit). They route the
  next `BindCharacter`; the sim's dormant position is authoritative and `BindCharacter` applies where
  the body is, not where the roster says.

### Binding protocol

```
SelectCharacter ─▶ gateway lock(account) ─▶ live? FAILED_PRECONDITION already_live
                                          ─▶ owned & ACTIVE? else NOT_FOUND
                                          ─▶ session.live = character (tentative)
                                          ─▶ Bindings.Bind(session, {Actor, Zone, Room})
                                          ─▶ produce BindCharacter to the Zone's partition
                                          ─▶ respond offset (a produce failure clears both)
sim apply(BindCharacter) ─▶ dormant body? clear dormant : instantiate andara.core.Character at spawn
                        ─▶ emit CharacterArrived{from_direction: ""} to the Room
session end ─▶ produce UnbindCharacter{QUIT} ─▶ Bindings.Unbind ─▶ session.live cleared
sim apply(UnbindCharacter) ─▶ body dormant at its Room ─▶ emit CharacterLeft{to_direction: ""}
```

The live flag is Session state at the Gateway (single process, ADR-0001) and is the one-live guard;
the sim trusts `BindCharacter` because `authorize` already checked ownership. A second Gateway process
would need the guard moved into the log, which is the sharding story's problem, recorded here. A
dormant body is a Zone-state Entity with `dormant = true`: in no Room's occupant list, addressed by
no Event, invisible to `look`, and part of the State Hash like any Entity.

### Name folding

`fold(name)` = Unicode NFKC, casefold, trim. Reservation key is `name/{fold}`; display name is as
typed. `character.name_pattern` default `^[\p{L}][\p{L}' -]{2,23}$`.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `character.max_per_account` | `ANDARA_CHARACTER_MAX_PER_ACCOUNT` | `5` | ADR-0006 |
| `character.spawn_room` | `ANDARA_CHARACTER_SPAWN_ROOM` | `town/plaza` | `zone_id/room_id`; boot fails if unresolvable against the loaded content. The default is the dev fixture's (Brian, 2026-09-21); real content sets its own. |
| `character.name_pattern` | `ANDARA_CHARACTER_NAME_PATTERN` | above | RE2 |

`character.delete_retention` is `AW-SRV-032`'s; its pending row in `keys.yaml` moves there.

`character.spawn_room`'s default is the dev fixture's Room, and boot refuses a value the loaded
content does not resolve — so **every non-local values file must set it**, and the chart's
`values.schema.json` marks it required outside `ENV=local`. Better a refused boot than a spawn into a
Room that does not exist.

This story is a heavy M — three proto files, the roster store under `AW-SRV-008`'s lock, two Command
applies, dormancy in the sim, the teardown produce, and the `Bindings` hand-off — and is not a split
candidate: every piece is on the path from `SelectCharacter` to a rendered Room. Budget for it.

### Error taxonomy

`ErrorInfo{domain: andara.character, reason}` on every roster error.

| Condition | gRPC code | `reason` |
|-----------|-----------|----------|
| cap reached | `RESOURCE_EXHAUSTED` | `roster_full` |
| name reserved | `ALREADY_EXISTS` | `name_taken` |
| name invalid | `INVALID_ARGUMENT` | `name_invalid` |
| another Character live on the Account, or this one bound elsewhere | `FAILED_PRECONDITION` | `already_live` |
| not owned, or not `ACTIVE` | `NOT_FOUND` | `no_such_character` |
| the produce failed (`AW-SRV-010`'s taxonomy) | as `Submit` | as `Submit` — the tentative live flag and the binding are cleared |

## Data / state impact

`andara.accounts.v1` gains `Account.characters` and the `name/*` keyspace. Both are written under the
`AW-SRV-008` single-writer lock, reservation first, then the Account record; a crash between the two
leaves a reservation with no Character, which `CreateCharacter` treats as taken (the name is lost, the
Account is not corrupted) — `AW-SRV-032`'s sweep may reclaim it. Dormant bodies are Zone state and
therefore part of the State Hash; `AW-SRV-006` carries `dormant` and `dormant_since_tick` in
`EntityState` (its sketch already reserves 5–9 for them), so no `state_version` bump happens here —
there is no snapshot yet to version.

Rollout: the RPCs and Command arms are additive (ADR-0007). A live Session on an older server has no
Character and loses nothing.

## Observability requirements

### Metrics
- `andara_characters_total` — gauge, label `state` (`present`, `dormant`), **emitted by the sim**
  from Zone state — the bodies. Present-without-a-Session counts as `present`.
- `andara_sessions_bound` — gauge, **emitted by the Gateway** from its live flags — the Sessions with
  a Character. The two are different owners of different facts; `present − bound` is the number of
  bodies no Session drives, which `AW-SRV-015` makes a linkdead count.
- `andara_character_creations_total` — counter, label `outcome` (`ok`, `roster_full`, `name_taken`,
  `name_invalid`).
- `andara_character_bindings_total` — counter, label `outcome` (`ok`, `already_live`, `race_lost`,
  `not_found`, `produce_failed`).
- `andara_character_unbinds_total` — counter, labels `reason` (`quit`; `switch` and `linkdead`
  later) and `outcome` (`ok`, `produce_failed`).
Character name and Account ID are rejected as labels.

### Logs
- `info` on create, select, bind-applied, unbind with `account_id`, `character_id`, `session_id`,
  `trace_id`. Names are player-supplied and are logged escaped, never as a key.

### Traces
- `character.create` and `character.select` children of `session.lifetime`; `select` has a
  `log.produce` child like `Submit`; the `BindCharacter` and `UnbindCharacter` applies join via the
  record's `trace_id` like any Command.

### Alerts
None. Roster problems present as authentication or Session problems, which already alert.

## Test plan

- **Unit:** name folding table (case, NFKC confusables, whitespace); cap counting; `name_pattern`
  edge cases; `BindCharacter` apply from dormant and from never-bound; `UnbindCharacter` apply
  (occupants, dormancy, the emitted `CharacterLeft`); a dormant body excluded from `look` and from
  Room-scoped Events.
- **Integration:** 20-way concurrent `SelectCharacter` (AC-7); the M1 gate over a real gateway —
  create, select, `look`, `north`, `look`, with the Events asserted on the stream and the Commands
  in the log (AC-5, AC-10); a second Session in the Room seeing the arrival and the departure
  (AC-8); replay from the beginning with one live and one dormant Character (AC-9); a produce
  failure on select leaving no live flag and no binding.
- **Manual/operator** (the M1 gate, `AW-CLI-007`'s commands):
  ```
  make up
  andara-cli auth login --username operator
  andara-cli character create Aldric     # expect: Aldric (1 of 5)
  andara-cli play --character Aldric     # expect: the plaza, "Here: Aldric"
  > north                                # expect: the hall
  > look
  ^C                                     # a second terminal in the plaza saw "Aldric arrives." then "Aldric leaves."
  andara-cli character list              # expect: Aldric, dormant, town/hall
  ```

## Definition of done

CLAUDE.md §8, plus: the concurrent-binding test (AC-7); the M1 gate scripted in `scripts/stack_play.sh`
(the half `AW-CLI-004` left to this story, extended by `AW-CLI-007`'s commands) and green in the
`stack` workflow; the roadmap's M1 gate observed by a human on the kind cluster and recorded here.
Inherited from `AW-SRV-010`'s §8 pass (2026-09-20): this is the first story that binds a Character
in-cluster, so a live `Submit` first reaches the produce here. §8's backend verification includes
showing, from the running server on the compose stack, `andara_ingress_submits_total{outcome="produced"}`,
`andara_ingress_produced_total{partition}` moving, `andara_ingress_produce_duration_seconds`,
`andara_command_duration_seconds{phase="pre_log"}`, the `log.produce` span under `command.execute`
in Tempo, and one `UNAVAILABLE{world_read_only}` on the wire during `docker compose stop redpanda` —
`AW-SRV-010` proved each by the integration suite only, every live Submit having fallen at `authorize`.
Inherited from `AW-SRV-011`'s flip to `review` (2026-09-21) and `AW-CLI-004`'s (PR #39), the same
bound Character on the same compose stack: Event delivery on a `Subscribe` stream (011 AC-1, AC-2),
a stream ended `buffer_full` from a deliberately stalled `play` with its `warn` line in Loki
(`session_id`, `buffered`, `last_sent`), `andara_session_egress_drops_total{reason="buffer_full"}`
and `andara_sessions_in_drop_state` moving, a resume that replays retained Events
(`stream.resumed` on the `Game/Subscribe` span, and `play`'s AC-8 with `resume_window_exceeded`),
`andara_stream_buffer_depth` samples, `andara_stream_events_sent_total{type}` for an EventType; and
from `play` (`scripts/stack_play.sh`'s header names them): the Room after `look` (AC-1), Events
after `north` (AC-2), a second client seeing the departure (AC-3), a post-log `CommandRejected`
(AC-4), and read-only under `docker compose stop redpanda` (AC-6). The Session availability SLO
(99.5 % over 28 d) gets its first measurement here.
Inherited from `AW-SRV-031`'s flip to `review` (2026-09-21): the first *produced* Submit's dedup —
the same `client_ref` again answered `{partition, offset N}` with `andara_ingress_submits_total{outcome="deduplicated"}`
moving and the topic's end offset still — and the ambiguous fates on the running server (`play
--client-timeout 50ms` against `docker compose pause redpanda`, retry `north`, unpause: one
`CharacterArrived`, per 031's manual step); 031 showed both by the integration suite against the
stack's broker only.

## As built (2026-09-21)

`server/roster` (the Gateway's side: `ListCharacters`, `CreateCharacter`, `SelectCharacter`,
`ReleaseSession`), `server/auth/characters.go` (the roster records, the reservations, the fold),
`server/sim/character.go` (`BindCharacter`, `UnbindCharacter`, dormancy, the body count),
`server/gateway` (the `Roster` seam and the three RPCs), `server/boot/roster.go`,
`server/config` (the three `character.*` keys), `internal/smoke/m1_test.go` (the live gate), and
`scripts/stack_play.sh`, which now runs it first.

- **Schema.** `CharacterStatus` lives in `andara.accounts.v1` — the record schema that needs it —
  rather than `game.v1`, and its values are `CHARACTER_STATUS_ACTIVE` / `CHARACTER_STATUS_DELETED`:
  protobuf scopes enum values to the package, and `AccountStatus.ACTIVE` already holds the bare
  name. `game.proto` imports `account.proto` for it, as `admin.proto` already does.
  `SelectCharacter` returns `SelectCharacterResponse{accepted_offset, partition}` — Submit's
  fields in a message of its own, because buf's `RPC_REQUEST_RESPONSE_UNIQUE` refuses one message
  as two RPCs' response and the two may grow apart. `AccountRecord` gains
  `name_reservation = 3`; `Account.characters = 11`; `LoggedCommand` arms 15 and 16;
  `logv1.Entity.name = 5` so a name crosses a Zone boundary with the body (dormancy is not carried:
  a dormant body never moves). `UnbindReason` declares `SWITCH` and `LINKDEAD` unused.
- **Identity and the body.** A Character's `EntityID` is its `character_id` (32 hex, like an
  account id); the display name is `EntityState.Name`, carried by the `BindCharacter` that made the
  body, and `DisplayName()` returns it — so `Here:`, `CharacterArrived`, and `CharacterLeft` name
  "Aldric" while the log and the routing table name the id. `EntityState` gains `Name`, `Dormant`,
  and `DormantSince`; each is a canonical record omitted when unset, so nothing that existed before
  them hashes differently and `StateVersion` stays 1. A dormant body: in no Room's occupants, not
  found by `locate` (`actor_not_found` "you are not here"), addressed by no Event.
- **The sim's `BindCharacter`** decides from Zone state alone: dormant here → cleared, arrival
  emitted where it stands (AC-6); present here → nothing, the Entity untouched (AC-11, asserted
  byte-for-byte); absent → instantiated from `andara.core.Character` at `spawn_room_id` with the
  arrival (AC-5). **One addition the contract did not have:** a body found in *another* Zone — the
  roster's last knowledge was stale, which a crash after a cross-Zone move leaves — is re-routed:
  the same Command is produced to the Zone that holds it (a later tick, never a call) and applies
  there, so no second body is spawned; a present one found that way is announced to the Character
  alone, so the routing table learns where it stands and the Room learns nothing. The handler reads
  `a.State.Zones` for that check — legal under ADR-0001's single process and deterministic on
  replay; the sharding story revisits it with the live flag. A spawn Room the Zone lacks rejects
  `unknown_room`; a World without the Template rejects the new code `template_missing` (the boot
  refuses both, so these are for content swapped under the log). `ContentVersion` on a new body is
  empty until AW-SRV-012 versions content in the log — any value chosen now is one a replay after a
  content change could not reproduce.
- **`UnbindCharacter`** on a body this Zone holds and present: dormant, `DormantSince = tick`,
  `CharacterLeft{to_direction: ""}` to its Room. Not held, or already dormant: nothing, no
  rejection — the Session that produced it is gone, and a second teardown must apply as none.
- **The roster records** are `Account.characters` (sorted by id, normalized on every commit) and
  `name/{fold}` reservations, written in that order under `wmu`; the fold is NFKC → case-fold →
  trim (`x/text`); the reservation index is rebuilt at boot from the `name/*` keys, and an orphaned
  reservation holds its name. The cap counts every Character on the record, ACTIVE or DELETED
  (AW-SRV-032 purges). `SetCharacterPosition` is the unbind's write; a position already recorded is
  not rewritten. The account index log line gains `character_names`.
- **The Gateway's live flag** is `roster.Roster`'s map by Account: set *tentatively* before the
  produce — a second `SelectCharacter` in that window is counted `race_lost`, after it
  `already_live`, both `FAILED_PRECONDITION already_live` naming the live Character — confirmed
  when the `BindCharacter` is in the log, and freed by the teardown once its produce has run. A
  produce failure clears the flag and the binding and is answered as Submit answers it
  (`ingress.WireError`); a retry after `DEADLINE_EXCEEDED` finds no flag and produces again, safe
  because the sim's `BindCharacter` is idempotent on a present body. `Bindings.Bind` runs before
  the produce with the roster's Zone *and Room* (the AW-SRV-011 question, decided: carried), so
  `egress.Rebind` moves the stream's perception to the Room the body will appear in before the
  arrival is emitted. `spawn_room_id` on the wire is the roster's Room — the spawn Room at create,
  where the body went dormant after an unbind; the sim ignores it when the body exists.
- **The teardown** is a new gateway seam: `Roster.ReleaseSession(s)` is called from
  `sessionStore.close` *before* `s.cancel()`, whichever way the Session ends (CloseSession, drop,
  revoke, drain), so the routing table is read while it still says where the body is — `Bindings`'
  entry follows transit, so a body that walked to the hall is unbound in the hall. The produce runs
  on its own context bounded by `ingress.produce_deadline`, then `Bindings.Unbind` (the ingress does
  it too on the same signal, but a Session that never submitted has no ingress state), then the
  roster position (skipped in transit: the Room is unknown and the roster keeps what it had), then
  the flag. `Roster.Wait()` is called by `CloseIngress` so a drain's unbinds reach the log before
  the producer closes. Until the teardown has run the Character is `already_live` — a reconnecting
  client's `SelectCharacter` included; AW-CLI-007 retries on the reconnect backoff.
- **Metrics** as specified: `andara_characters_total{state}` is set from `Engine.Characters()` on
  the loop goroutine after recovery and after every tick that applied a Command (nothing else moves
  a body); `andara_sessions_bound` from the confirmed flags; `andara_character_creations_total`
  lives with the store's metrics, the other two with the roster's. `andara_character_bindings_total`
  distinguishes `race_lost` (a tentative flag) from `already_live`. Names are logged as
  `"name":"\"Aldric\""` — quoted, a value, never a key.
- **Traces:** `character.create` and `character.select` are children of the RPC span, linked to
  `session.lifetime`; `log.produce` is `select`'s child, and the tick's `command.apply` for the
  `BindCharacter` joins the trace through the record's `trace_id` (Tempo shows all four under one
  trace ID). The teardown's `character.unbind` is its own root linked to the Session.
- **Configuration:** `character.max_per_account`, `character.spawn_room`, `character.name_pattern`
  as the table says (flag, env, file `character:` section); the boot resolves `spawn_room` against
  the loaded World and requires `andara.core.Character` in the registry — a server that cannot
  make a Character refuses to start. `keys.yaml` records the real `name_pattern` default; a new
  `required_outside_local: true` attribute makes `scripts/values_schema.py` emit an `allOf`
  conditional, so a values file whose `telemetry.environment` is not `local` (or unset — the
  server's default is local) must set `server.character.spawn_room`; `dev.yaml` and `prod.yaml` set
  `town/plaza` with a note that AW-SRV-012's content replaces it, and `helm-test` asserts the
  refusal.
- **Content.** `testdata/content/valid` — the dev World the compose stack, the kind cluster, and
  the boot tests load — now carries `templates/` with the core pack, held byte-identical to
  `content/core/templates` by `TestCoreSeedMatchesFixture` alongside the Template fixture. A
  ConfigMap holds no subdirectory, so the chart gains `contentVolume.templatesConfigMapName`,
  mounted at `/content/templates`, and `helm_install.sh` builds `andara-content-templates` from
  `content/core/templates` for `ENV=local`.
- **Not built, by the contract:** deletion, retention, purge, switching (AW-SRV-032); linkdead
  (AW-SRV-015); `andara-cli character` and `play --character` (AW-CLI-007). Nothing here changes
  when `andara.core.Character` gains Components.

### Verification record (2026-09-21, `ff97070` and after)

- **Unit.** `sim`: spawn (AC-5, with both players' `look`), unbind (AC-8: departure, dormancy,
  invisible to `look`, acts for nobody, a repeated unbind applies as none), wake where it was
  (AC-6), present-body idempotence (AC-11, the Zone's bytes unchanged), the cross-Zone re-route,
  the two rejections, name and dormancy in the canonical bytes, the name crossing a Zone, and
  **replay** (AC-9: present and dormant survive, the hash matches, the present one is taken where
  it stands and `look`s the hall). `auth`: the folding table, the cap, the pattern naming its rule,
  reservation-before-record on the log, taken in any case on any Account with one message, nothing
  written on a refusal, restart with an orphaned reservation, the options. `roster`: the wire
  taxonomy (codes, reasons, domain, `max_per_account`), bound-before-produced observed from inside
  the producer, AC-4 on another Session and on the same one, a produce failure clearing flag and
  binding and answered as Submit's `world_read_only`, **AC-7 twenty-way** (one produce, nineteen
  `already_live`, `race_lost` counted), the teardown's `UnbindCharacter` to the Zone the table
  followed to with the roster position written from it, a failed teardown produce freeing the
  flag, a Session dying mid-select. All under `-race`.
- **Integration**, `cmd/andara-server` `TestRun_M1Gate` over a real gateway and
  `sim.source=memory`: two Accounts, create, a name the other holds refused, select with the
  arrivals on both streams (AC-3), `already_live` from a second Session with the list flagging the
  live one, `look` with `Here: Brin`, `north` through the log and the tick on both streams (AC-10),
  `look` in the hall, a `CloseSession` seen by the other player as a departure with no direction
  and gone from the occupants (AC-8), the roster freed in the plaza, the wake where it was (AC-6),
  every instrument on `/metrics`, a drain unbinding the rest, names never a log key. ~1 s.
- **Live, on the compose stack** (`make stack-play`, and CI's `stack` workflow):
  `TestLive_M1Gate` — the same gate through Redpanda, plus AW-SRV-031's dedup of a produced
  `north` (same offset, `deduplicated` moving) and play's AC-4 rejection as prose — then the play
  half, then the server restart under an open session. The restart's recovery **replayed the
  BindCharacter/UnbindCharacter history**: `andara_characters_total{state="dormant"} 8` after four
  runs, every hash matching its boundary (a mismatch halts the boot). Tempo: one trace
  `Game/SelectCharacter → character.select → log.produce`, with the tick's `command.apply`
  (`tick`, `partition`, `offset`) joined by the record's `trace_id`. Loki: `character created`,
  `character selected`, `character unbound` with `account_id`, `character_id`, `session_id`,
  `trace_id`, `reason=quit`, `outcome=ok`, `zone`, `room`. Read-only with a bound Character
  (AW-SRV-010's inherited line): during `docker compose stop redpanda` the first Submit was
  `DEADLINE_EXCEEDED produce_deadline` and every one after `UNAVAILABLE world_read_only` with the
  player's message and `RetryInfo 1s`; `andara_ingress_submits_total{outcome="deadline"|"unavailable"}`
  moved. `stack_play.sh`'s readiness wait after the restart is now 180 s: this stack's log holds
  2.8 M tick boundaries and recovery replays them all until AW-SRV-006.
- **Inherited lines closed here** (a bound Character on the running server):
  `andara_ingress_submits_total{outcome="produced"}`, `andara_ingress_produced_total{partition}`,
  `andara_ingress_produce_duration_seconds`, `andara_command_duration_seconds{phase="pre_log"}`
  and `{phase="post_log",verb="move"|"bind_character"}`, the `log.produce` span, read-only on the
  wire (AW-SRV-010); Event delivery on a `Subscribe` stream (AW-SRV-011 AC-1, AC-2),
  `andara_stream_events_sent_total{type}`, `andara_stream_buffer_depth` samples; the produced
  Submit's dedup (AW-SRV-031). **Inherited lines that pass to AW-CLI-007** — they need `play`
  driving a bound Character: play's own AC-1/2/3/4/6 transcript, a stream ended `buffer_full` from
  a deliberately stalled `play` (its `warn` line, the drops counter, `andara_sessions_in_drop_state`),
  a resume that replays retained Events (`stream.resumed`, play's AC-8 with
  `resume_window_exceeded`), and AW-SRV-031's ambiguous fates (`play --client-timeout 50ms` against
  `docker compose pause redpanda`). The kind-cluster observation by a human waits on the same
  commands.
- `make check` clean (fmt, vet, lint, tests, proto-check with no breaking change, values-schema,
  k8s-dry, helm-test, license).

## Open questions

- **Resolved 2026-09-21 (Brian): `character.spawn_room` is `town/plaza`** for the dev content — a
  values-file line; real content sets its own.
- `[NEEDS BRIAN]` What a Character *is* beyond a name and a position. Components on
  `andara.core.Character` are additive; nothing here changes when they arrive.
- `[ASSUMPTION]` **A body in another Zone than the roster names is re-routed by the sim** (see As
  built): the `BindCharacter` handler looks the Character up across this process's Zones and
  produces the same Command to the one that holds it. Reading another Zone's state from a handler
  is inside ADR-0001's single process and deterministic on replay; the alternative — a second body
  at the spawn Room — is a duplicate the World could never reconcile. The sharding story owns the
  cross-process form, with the live flag.
- `[ASSUMPTION]` **A present body found in another Zone is announced to the Character alone**
  (`CharacterArrived` scoped to the Entity, not the Room), so the routing table and the stream
  learn where it stands; AC-11's "nothing emitted" holds for the same-Zone case it describes.
- `[ASSUMPTION]` **`CharacterStatus` lives in `andara.accounts.v1` with prefixed values**, and
  `SelectCharacter` has its own response message — both forced by the toolchain (package-scoped
  enum values; buf's unique-response lint), neither changing what a client reads.
- `[ASSUMPTION]` **The unbind in transit keeps the roster's last Room.** Between a cross-Zone
  departure and its arrival the routing table has a Zone and no Room; the teardown produces the
  `UnbindCharacter` to that Zone (a no-op there — the body is in flight) and leaves the roster
  position as it was. The body then arrives present with no Session, and the next
  `SelectCharacter` takes it where it stands (AC-11), re-routed if the roster's Zone is the old one.
- **Resolved 2026-09-21 (re-groom):** this story no longer depends on `AW-SRV-007`. Deletion,
  retention, purge, and switching — the parts that want recovery and a snapshot format — are
  `AW-SRV-032` (M2). One-live at the Gateway in process memory stays: correct under ADR-0001, the
  sharding story moves it.
- **Inherited from `AW-SRV-011` (review of PR #37, 2026-09-20):** `Egress.Rebind` compares the new
  binding against the subscribe-time Observer, not the Room the Hub has since followed the Entity
  to, so a same-Actor `Bind` after a sim-driven move discards the Session's retained history and a
  resume from before it is `no_history`. Rare, and this story decides whether `Bindings.Bind`
  carries the Room (so `Rebind` can tell "same perception" from "new one") or accepts the spurious
  discard. **Decided at the re-groom (2026-09-21): it carries the Room** — see the contract.
- **Corrected 2026-09-19 (review of PR #34): the contract sketch's arm numbers are taken.**
  `Arrive` shipped as `LoggedCommand` arm 12 (`AW-SRV-003`) and `AW-SRV-028` takes 13
  (`HandoffAck`) and 14 (`HandoffRejected`). `BindCharacter` and `UnbindCharacter` are 15 and 16.
  Two things this story must do that the sketch does not say: (1) `ingress.Bindings.Publish` moves
  only Sessions whose Character is already in its table, so `SelectCharacter` calls
  `Bindings.Bind(session, {Actor, Zone})` with the roster's last-known Zone *before* the
  `BindCharacter` Command's `CharacterArrived` materializes the Character — otherwise the arrival
  is not routed and the Session's first Submit goes to the roster's Zone by luck. (2) An
  `UnbindCharacter` reaching the sim must also `Bindings.Unbind` on the Gateway, or a switched
  Character keeps routing to the old one until the Session ends.
