---
id: AW-SRV-032
title: Character deletion, name retention, purge, and switching bodies
epic: EPIC-08
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-014, AW-SRV-007]
blocks: []
lane: implementation
risk: medium
---

## Context

`AW-SRV-014` was split on 2026-09-21: the half M1 needs — create, select, bind, quit — stayed there,
and this is the other half, the part that genuinely wants `AW-SRV-007`'s recovery and `AW-SRV-006`'s
snapshot format before it is safe: deleting a Character while keeping its name reserved, purging the
dormant body after a retention window measured in Ticks, and switching bodies inside one Session.
ADR-0006 fixed the shape — five Characters, one live, names never reused — and this story finishes
it.

The retention sweep is the first thing in the World that runs on a *schedule* rather than in answer to
a Command, and it must still be deterministic under replay: the Gateway produces `PurgeCharacter`
when it observes expiry, the sim applies it in log order, and a replayed log purges on the same Tick.

## User story

As a player, I want to delete a character I no longer play and switch between the ones I keep, without
someone else taking a name I made, so that my roster is mine to shape.

## Scope

### In scope
- `DeleteCharacter`: soft delete — `status: DELETED`, `deleted_unix` set, the body left dormant, the
  name reservation kept forever.
- Deleted Characters count against the cap of five until purged, so deletion is not a way to hold six
  names.
- `PurgeCharacter` produced by the Gateway's sweep after `character.delete_retention`, applied by the
  sim: the dormant body is removed from Zone state; the roster entry stays `DELETED`; the reservation
  stays.
- Switching: `SelectCharacter` on a Session that already has a live Character produces
  `UnbindCharacter{SWITCH}` then `BindCharacter`, so the Room sees the first leave and the second
  arrive, in that order.
- Reclaiming a reservation that names no Character (a crash between the two writes in `AW-SRV-014`'s
  create) in the same sweep.

### Out of scope
- Creation, selection, binding, quit — `AW-SRV-014`. Linkdead — `AW-SRV-015`.
- Hard deletion of the roster entry or the reservation. Names are immutable and never reused
  (ADR-0006); an operator override, if ever wanted, is an admin story with an audit record.

## Acceptance criteria

1. **Given** an Account with five Characters, one of them deleted yesterday **when** a sixth is created
   **then** `RESOURCE_EXHAUSTED` `roster_full` — the deleted one still counts.
2. **Given** a deleted Character's name **when** any Account creates with it, in any letter case
   **then** `ALREADY_EXISTS` `name_taken`, before and after the purge.
3. **Given** a soft-deleted Character **when** `character.delete_retention` expires, measured in Ticks
   **then** a `PurgeCharacter` is produced once, the sim removes the dormant body,
   `andara_characters_total{state="dormant"}` decrements (the body is gone),
   `andara_roster_characters{status="deleted"}` does not (the entry stays), and the reservation
   record remains.
4. **Given** the log that produced AC-3 **when** it is replayed from the beginning, or recovered from a
   snapshot plus tail (`AW-SRV-007`) **then** the purge applies on the same Tick and the State Hash
   matches.
5. **Given** a live Character **when** `DeleteCharacter` names it **then** `FAILED_PRECONDITION`
   `character_live`; quit first.
6. **Given** a Session with a live Character **when** it selects another of its own **then** an
   `UnbindCharacter{SWITCH}` and a `BindCharacter` are produced in that order to their Zones'
   partitions, `CharacterLeft{""}` for the first and `CharacterArrived{""}` for the second are
   emitted, each with its own Room scope, and the Session's `Bindings` entry names the second before
   its arrival applies.
7. **Given** a World rollback to an earlier snapshot round **when** a name created after that round is
   checked **then** it is still reserved, because the reservation lives on `andara.accounts.v1`.
8. **Given** a reservation with no Character behind it **when** the sweep runs **then** it is removed
   and the name is creatable again; a reservation with a Character behind it, deleted or not, is
   never removed.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation; additions to andara/game/v1/game.proto
rpc DeleteCharacter(DeleteCharacterRequest) returns (DeleteCharacterResponse);   // session_id, character_id
// CharacterSummary and CharacterRef gain int64 deleted_unix = 8 / 7 and CharacterRef int64 purged_unix = 8;
// CharacterStatus.DELETED comes into use.

// additions to andara/log/v1/log.proto LoggedCommand oneof
PurgeCharacter purge_character = 17;   // character_id; produced by the Gateway sweep at retention expiry
// UnbindCharacter.reason gains SWITCH (AW-SRV-014 declared QUIT; AW-SRV-015 adds LINKDEAD).
```

- The sweep runs in the Gateway on a timer, reads the roster index under `AW-SRV-008`'s lock, and
  produces one `PurgeCharacter` per expired Character to the Character's last-known Zone partition;
  it is idempotent — a purged Character is marked `purged_unix` on the roster and never produced
  again. Expiry is `deleted_unix + delete_retention`; the sim additionally records the purge Tick so
  replay agrees with itself, not with the wall clock.
- Switching reuses `AW-SRV-014`'s binding protocol twice: `Bindings.Bind` to the new Character
  *before* either produce (so the arrival routes), the two produces in order, the live flag moved
  between them. A failed second produce leaves the Session unbound with the first body dormant — the
  Session is out of the World, not in two Rooms.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `character.delete_retention` | `ANDARA_CHARACTER_DELETE_RETENTION` | `720h` | 30 d, converted to Ticks at `sim.tick_rate`; moved here from `AW-SRV-014`'s pending row |
| `character.purge_sweep_interval` | `ANDARA_CHARACTER_PURGE_SWEEP_INTERVAL` | `10m` | how often the Gateway looks for expired Characters |

### Error taxonomy

`ErrorInfo{domain: andara.character, reason}`.

| Condition | gRPC code | `reason` |
|-----------|-----------|----------|
| delete a live Character | `FAILED_PRECONDITION` | `character_live` |
| delete a Character not owned or already deleted | `NOT_FOUND` | `no_such_character` |
| switch to a Character that is not `ACTIVE` | `NOT_FOUND` | `no_such_character` |

## Data / state impact

`CharacterRef` gains `deleted_unix` and `purged_unix`; `EntityState.dormant_since_tick` (`AW-SRV-006`)
is what the purge Tick compares against on replay. Removing a body from Zone state changes the State
Hash, by design — a purge is a state change. `state_version` bumps by one if `AW-SRV-006` has shipped
a snapshot without `purged` semantics; otherwise none. Migration: none for the accounts topic — new
fields are additive; a record without `deleted_unix` is `ACTIVE`.

## Observability requirements

### Metrics
- `andara_roster_characters` — gauge, label `status` (`active`, `deleted`), **emitted by the
  Gateway** from the roster index; `AW-SRV-014`'s `andara_characters_total{state}` stays the sim's
  count of bodies, and a purge moves the sim's `dormant` down while the roster's `deleted` stands.
- `andara_character_purges_total` — counter, label `outcome` (`ok`, `already_purged`, `reclaimed`).
- `andara_character_unbinds_total{reason="switch"}`.
- `andara_character_sweep_duration_seconds` — histogram, one sample per sweep.

### Logs
- `info` on delete, purge produced, purge applied, switch, reclaim with `account_id`, `character_id`,
  `session_id` where there is one, `trace_id`.

### Traces
- `character.sweep` root span per run with `expired`, `produced`; `character.delete` child of
  `session.lifetime`; the applies join via the record's `trace_id`.

### Alerts
None.

## Test plan

- **Unit:** cap counting with deleted Characters; expiry arithmetic in Ticks at several tick rates;
  the sweep's idempotence; reclaim only of reservations with no Character.
- **Integration:** retention expiry in a replayed log asserting the same purge Tick and State Hash
  (AC-3, AC-4); kill-and-recover across a purge (`AW-SRV-007`); rollback to an older round asserting
  the reservation survives (AC-7); a switch asserting Event order and scopes on two streams (AC-6).
- **Manual/operator:** `andara-cli character delete Aldric` (the CLI command lands with a CLI story
  groomed alongside this one), `andara-cli character list` showing `deleted`, then with
  `ANDARA_CHARACTER_DELETE_RETENTION=1m` on the local stack, the purge line in Loki two minutes later
  and `andara_characters_total{state="dormant"}` back down while `andara_roster_characters{status="deleted"}` holds.

## Definition of done

CLAUDE.md §8, plus: the replay-purge test and the rollback-reservation test.

- **Inherited from `AW-SRV-019` (2026-09-24):** `CharacterPurged` gets a row in `server/projector`'s
  `Touched` table that tombstones `character:<zone>/<id>`, and that story's AC-5 assertion runs for it:
  after a purge replays and compaction runs, the key is absent. Force compaction as `AW-SRV-019` AC-5 (amended 2026-09-24) does, on a throwaway topic with its cleaner settings lowered; `topics.py` cannot force it.
  `AW-SRV-019` could exercise the tombstone path only through a Zone exit, because no destroy Event
  existed yet.

## Open questions

- `[ASSUMPTION]` Soft-delete retention 30 d, counted against the cap until purged, so deletion is not
  a way to hold six names. Carried from `AW-SRV-014`.
- `[ASSUMPTION]` Deleted Characters keep their dormant body until purge, so an undelete — if ever
  wanted — is one status flip. No undelete RPC is groomed.
