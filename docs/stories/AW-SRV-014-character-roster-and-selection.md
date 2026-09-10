---
id: AW-SRV-014
title: Character roster, creation, selection, and binding
epic: EPIC-08
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-007, AW-SRV-008]
blocks: [AW-SRV-015]
lane: implementation
risk: medium
---

> `status: draft` — unblocked by ADR-0006 and scoped. Groomed to `ready` when M2 approaches.

## Context

ADR-0006 decided up to five Characters per Account with **exactly one live at a time**, confirmed by Brian
on 2026-09-07. This story is the roster: creation, naming, selection, binding to a Session, and soft
deletion.

It depends on `AW-SRV-007` rather than only on `AW-SRV-008` because a Character is World state — it has a
position, an inventory, a history — and a roster that survives a restart requires recovery to work first.

## User story

As a player, I want to own several characters and choose which one I am playing, so that I can hold more
than one place in the world.

## Scope

### In scope
- Character creation, with the roster cap of five enforced server-side.
- Globally unique, immutable Character names, reserved on deletion.
- Character selection and binding to a Session, with the one-live rule enforced.
- Soft deletion: leaves the roster, name stays reserved, World state retained for a stated window.
- The Account↔Character link, which lives in Account state, while the Character itself lives in World
  state — the split ADR-0006 requires.
- Spawn placement for a newly created Character.

### Out of scope
- Session lifecycle and linkdead — `AW-SRV-015`.
- Authentication — `AW-SRV-008`.
- Character appearance, class, stats, or any game system. `[NEEDS BRIAN]` — this story delivers identity
  and placement; what a Character *is* is a design decision.

## Acceptance criteria (known now; completed at grooming)

1. **Given** an Account with five Characters **when** a sixth is created **then** it is rejected naming
   the cap.
2. **Given** a Character name already in use, including by a soft-deleted Character **when** creation is
   attempted **then** it is rejected without revealing whether the existing Character is active.
3. **Given** an Account with a live Character **when** a second Character is selected on another Session
   **then** it is rejected. One live Character per Account, enforced at binding time rather than queued.
4. **Given** a Character selected **when** the Session binds **then** the Character is placed in the World
   at its last known position, or at the spawn Room if it has never played.
5. **Given** a soft-deleted Character **when** the retention window expires **then** its World state is
   removed and its name remains reserved.
6. **Given** a server restart **when** an Account reconnects **then** its roster and each Character's
   position are exactly as before, per `AW-SRV-007`.
7. **Given** two concurrent selection attempts for the same Character **when** both arrive **then** at
   most one binds. A Character must never be bound to two Sessions.
8. **Given** a player switching Characters **when** they do **then** it is a despawn followed by a spawn,
   both visible to anyone in the affected Rooms — not a silent swap and not a second Session.

## Interface contract

To be written at grooming. It will cover the roster RPCs, the name-uniqueness mechanism across the
Account topic and World state, and the binding protocol.

Committed now: the Account↔Character link is Account state and the Character is World state, so name
uniqueness is enforced across a boundary — which is the one genuinely awkward consequence of ADR-0006's
separation and needs a designed answer rather than an incidental one.

## Data / state impact

Name reservation must survive both a World rollback and an Account-topic compaction, which means it lives
in Account state, not in the World. A World rollback that freed a name for reuse while another player
held it would be an unrecoverable identity collision.

## Observability requirements

- **Metrics:** `andara_characters_total` (gauge, label `state` — `active`, `deleted`),
  `andara_character_creations_total` (counter, label `outcome`), `andara_character_bindings_total`
  (counter, label `outcome`). Character name and Account ID are rejected as labels.
- **Logs:** creation, selection, binding, and deletion at `info` with Account, Character, and Session
  correlation ID. Character names are player-supplied and are escaped.
- **Traces:** `character.select` as a child of `session.lifetime`.
- **Alerts:** none. Roster problems present as authentication or session problems, which already alert.

## Test plan

Cap enforcement; name uniqueness including against soft-deleted names; concurrent selection asserting
single binding; restart asserting roster and position survive; retention expiry.

## Definition of done

CLAUDE.md §8, plus: the concurrent-binding test from AC-7, and an explicit test that name reservation
survives a World rollback.

## Open questions

- `[NEEDS BRIAN]` Soft-delete retention window.
- `[NEEDS BRIAN]` What a Character *is* beyond a name and a position. This story is deliberately empty of
  game design.
- `[NEEDS BRIAN]` Spawn Room for new Characters, which is lore-bearing.
