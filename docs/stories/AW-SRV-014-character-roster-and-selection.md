---
id: AW-SRV-014
title: Character roster, creation, selection, and binding
epic: EPIC-08
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-007, AW-SRV-008, AW-SRV-022]
blocks: [AW-SRV-015]
lane: implementation
risk: medium
---

## Context

ADR-0006 decided up to five Characters per Account with **exactly one live at a time**, confirmed by Brian
on 2026-09-07. This story is the roster: creation, naming, selection, binding to a Session, and soft
deletion.

It depends on `AW-SRV-007` rather than only on `AW-SRV-008` because a Character is World state — it has a
position and a history — and a roster that survives a restart requires recovery to work first. The one
genuinely awkward consequence of ADR-0006's Account/World split is that a Character's *identity* is
Account state and its *body* is World state, and name uniqueness has to hold across that boundary. This
story designs that seam rather than letting it happen.

## User story

As a player, I want to own several characters and choose which one I am playing, so that I can hold more
than one place in the world.

## Scope

### In scope
- Roster RPCs on `Game`: `ListCharacters`, `CreateCharacter`, `SelectCharacter`, `DeleteCharacter`.
- The roster cap of five and the one-live rule, enforced at the Gateway against Account and Session
  state before anything reaches the log.
- Globally unique, immutable, case-insensitively reserved Character names, reserved on deletion.
- Binding: `SelectCharacter` produces a `BindCharacter` Command; the sim spawns or re-materializes the
  Character and emits `CharacterSpawned`. Switching Characters is despawn then spawn, both visible.
- Dormant Characters: a Character not in the World keeps its last position in Zone state, so
  "where you were" survives restart and replay.
- Soft deletion with a retention window, after which the sim drops the dormant body and the name stays
  reserved.
- Spawn placement for a never-played Character at `character.spawn_room`.

### Out of scope
- Linkdead and reconnect — `AW-SRV-015`. Authentication — `AW-SRV-008`.
- Appearance, class, stats, inventory. A Character instantiates the `andara.core.Character` Template
  (ADR-0010, `AW-SRV-022`); what components that Template carries is Brian's and is additive to this contract.

## Acceptance criteria

1. **Given** an Account with five Characters, deleted ones included until retention expires **when** a
   sixth is created **then** `CreateCharacter` returns `RESOURCE_EXHAUSTED` naming the cap.
2. **Given** a name reserved by any Character, active or soft-deleted, on any Account **when** creation
   uses it in any letter case **then** it returns `ALREADY_EXISTS` with one fixed message.
3. **Given** an Account with a live Character **when** a second Character is selected on another Session
   **then** `SelectCharacter` returns `FAILED_PRECONDITION` naming the live Character; nothing is
   produced.
4. **Given** a Character selected **when** the `BindCharacter` Command applies **then** the Character is
   placed at its dormant position, or at `character.spawn_room` if it has never been bound, and a
   `CharacterSpawned` Event is emitted with Room scope.
5. **Given** a soft-deleted Character **when** `character.delete_retention` expires, measured in Ticks
   **then** the sim removes the dormant body, `andara_characters_total{state="deleted"}` decrements, and
   the name reservation record remains.
6. **Given** a server restart **when** an Account reconnects **then** its roster and every Character's
   position, live or dormant, are exactly as before, per `AW-SRV-007`.
7. **Given** two concurrent `SelectCharacter` calls for the same Character from two Sessions **when**
   both arrive **then** exactly one produces a `BindCharacter` and the other returns
   `FAILED_PRECONDITION`.
8. **Given** a player switching Characters **when** the second `SelectCharacter` applies **then** a
   `CharacterDespawned` for the first and a `CharacterSpawned` for the second are emitted, each with its
   own Room scope, in that order.
9. **Given** a World rollback to an earlier snapshot round **when** a name created after that round is
   checked **then** it is still reserved, because the reservation lives on `andara.accounts.v1`.
10. **Given** a name that fails `character.name_pattern` **when** creation is attempted **then**
    `INVALID_ARGUMENT` names the rule, and the name is not reserved.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation; additions to andara/game/v1/game.proto
rpc ListCharacters(ListCharactersRequest) returns (ListCharactersResponse);
rpc CreateCharacter(CreateCharacterRequest) returns (CreateCharacterResponse);   // name
rpc SelectCharacter(SelectCharacterRequest) returns (SubmitResponse);            // character_id; returns the BindCharacter offset
rpc DeleteCharacter(DeleteCharacterRequest) returns (DeleteCharacterResponse);   // character_id
message CharacterSummary { string character_id = 1; string name = 2; CharacterStatus status = 3;
                           string zone_id = 4; bool live = 5; int64 created_unix = 6; }

// additions to andara/accounts/v1/account.proto (AW-SRV-008)
message Account { /* … */ repeated CharacterRef characters = 11; }   // sorted by character_id
message CharacterRef { string character_id = 1; string name = 2; CharacterStatus status = 3; int64 deleted_unix = 4; }
message NameReservation { string character_id = 1; string account_id = 2; }   // key: name/{fold(name)}

// additions to andara/log/v1/log.proto LoggedCommand oneof
BindCharacter bind_character = 12;      // character_id, account_id, spawn RoomRef if never bound
UnbindCharacter unbind_character = 13;  // character_id, reason: SWITCH | QUIT
PurgeCharacter purge_character = 14;    // character_id; retention expiry
```

`ZoneState.EntityState` gains `bool dormant = 5` and `uint64 dormant_since_tick = 6` (`AW-SRV-006`).

### Binding protocol

```
SelectCharacter ─▶ gateway lock(account) ─▶ live? FAILED_PRECONDITION
                                          ─▶ owned & ACTIVE? else NOT_FOUND
                                          ─▶ session.live = character (tentative)
                                          ─▶ produce BindCharacter to zone partition
                                          ─▶ respond offset
sim apply(BindCharacter) ─▶ dormant body? clear dormant : create from andara.core.Character at spawn
                        ─▶ emit CharacterSpawned{character_id, room}
```

The live flag is Session state at the Gateway (single process, ADR-0001) and is the one-live guard;
the sim trusts `BindCharacter` because `authorize` already checked ownership. A second Gateway process
would need the guard moved into the log, which is the sharding story's problem, recorded here.

### Name folding

`fold(name)` = Unicode NFKC, casefold, trim. Reservation key is `name/{fold}`; display name is as typed.
`character.name_pattern` default `^[\p{L}][\p{L}' -]{2,23}$`.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `character.max_per_account` | `ANDARA_CHARACTER_MAX_PER_ACCOUNT` | `5` | ADR-0006 |
| `character.spawn_room` | `ANDARA_CHARACTER_SPAWN_ROOM` | — | `zone_id/room_id`; boot fails if unresolvable |
| `character.delete_retention` | `ANDARA_CHARACTER_DELETE_RETENTION` | `720h` | 30 d, converted to Ticks at `sim.tick_rate` |
| `character.name_pattern` | `ANDARA_CHARACTER_NAME_PATTERN` | above | RE2 |

### Error taxonomy

| Condition | gRPC code |
|-----------|-----------|
| cap reached | `RESOURCE_EXHAUSTED` |
| name reserved | `ALREADY_EXISTS` |
| name invalid | `INVALID_ARGUMENT` |
| another Character live, or Character bound elsewhere | `FAILED_PRECONDITION` |
| not owned / deleted | `NOT_FOUND` |

## Data / state impact

`andara.accounts.v1` gains `Account.characters` and the `name/*` keyspace. Both are written under the
`AW-SRV-008` single-writer lock, reservation first, then the Account record. Dormant bodies are Zone
state and therefore part of the State Hash and every snapshot; `state_version` bumps by one with a
migration that adds `dormant=false` to existing entities.

Retention expiry is driven by a Tick comparison inside the sim (`PurgeCharacter` is produced by the
Gateway's sweep and applied in order), so replay purges on the same Tick.

## Observability requirements

### Metrics
- `andara_characters_total` — gauge, label `state` (`live`, `dormant`, `deleted`).
- `andara_character_creations_total` — counter, label `outcome` (`ok`, `cap`, `name_taken`,
  `name_invalid`).
- `andara_character_bindings_total` — counter, label `outcome` (`ok`, `already_live`, `race_lost`,
  `not_found`).
- `andara_character_purges_total` — counter.
Character name and Account ID are rejected as labels.

### Logs
- `info` on create, select, bind-applied, delete, purge with `account_id`, `character_id`,
  `session_id`, `trace_id`. Names are player-supplied and are logged escaped, never as a key.

### Traces
- `character.select` child of `session.lifetime`; the `BindCharacter` apply joins via `trace_id` in the
  log record.

### Alerts
None. Roster problems present as authentication or Session problems, which already alert.

## Test plan

- **Unit:** name folding table (case, NFKC confusables, whitespace); cap counting including deleted;
  `name_pattern` edge cases; `BindCharacter` apply from dormant and from never-bound.
- **Integration:** 20-way concurrent `SelectCharacter` (AC-7); switch asserting Event order and scopes
  (AC-8); kill-and-recover with one live and one dormant Character (AC-6); rollback to an older round
  asserting the reservation survives (AC-9); retention expiry in a replayed log asserting the same purge
  Tick.
- **Manual/operator:**
  ```
  andara-cli play
  > create Aldric            # expect: "Aldric created (1 of 5)"
  > select Aldric            # expect: spawn Room description; others see "Aldric appears"
  > quit
  andara-cli character list  # expect: Aldric, dormant, zone shown
  ```

## Definition of done

CLAUDE.md §8, plus: the concurrent-binding test (AC-7) and the rollback name-reservation test (AC-9).
Inherited from `AW-SRV-010`'s §8 pass (2026-09-20): this is the first story that binds a Character
in-cluster, so a live `Submit` first reaches the produce here. §8's backend verification includes
showing, from the running server on the compose stack, `andara_ingress_submits_total{outcome="produced"}`,
`andara_ingress_produced_total{partition}` moving, `andara_ingress_produce_duration_seconds`,
`andara_command_duration_seconds{phase="pre_log"}`, the `log.produce` span under `command.execute`
in Tempo, and one `UNAVAILABLE{world_read_only}` on the wire during `docker compose stop redpanda` —
`AW-SRV-010` proved each by the integration suite only, every live Submit having fallen at `authorize`.

## Open questions

- `[NEEDS BRIAN]` Which Room is `character.spawn_room`. Lore-bearing; the key exists and boot refuses an
  unresolvable value, so the answer is one values-file line.
- `[NEEDS BRIAN]` What a Character *is* beyond a name and a position. Components on
  `andara.core.Character` are additive; nothing here changes when they arrive.
- `[ASSUMPTION]` Soft-delete retention 30 d, counted against the cap until purged, so deletion is not a
  way to hold six names.
- `[ASSUMPTION]` One-live is enforced at the Gateway in process memory. Correct under ADR-0001; the
  sharding story moves it.
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
