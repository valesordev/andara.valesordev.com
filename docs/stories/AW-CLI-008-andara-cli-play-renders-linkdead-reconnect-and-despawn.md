---
id: AW-CLI-008
title: andara-cli play renders linkdead, reconnect, and despawn
epic: EPIC-08
component: cli
type: feature
status: draft
size: S
depends_on: [AW-CLI-004, AW-SRV-015]
blocks: [AW-INF-017]
lane: implementation
risk: low
---

## Context

`AW-SRV-015` adds three Room-scoped Events: `CharacterLinkdead`, `CharacterReconnected` and
`CharacterDespawned`. `AW-CLI-004`'s `renderEvent` has no row for any of them. Today a bystander in
the Room would read "Something happened here that this client cannot describe (event N)." three
times, and SPRINT-02's demo would show exactly that. `AW-SRV-015` puts rendering out of its scope,
and no other story carries it.

The rows are pure functions of the Event, like every row in `render.go`. The `(linkdead)` marker in
`look` is the server's, carried in `RoomDescribed.occupants` (`AW-SRV-015` AC-1), so it renders
through the existing `Here:` row with no change here.

## User story

As a player, I want to read when someone in my Room loses their connection, comes back, or leaves
the world, so that the linkdead mechanic Brian decided to make visible is actually visible to me.

## Scope

### In scope
- One `renderEvent` row each for `CharacterLinkdead`, `CharacterReconnected` and
  `CharacterDespawned` (every `DespawnReason`).
- The golden recording gains a case for each row and each reason.
- `--output json` carries the three Events as envelopes, like every other Event (no change
  expected; asserted).

### Out of scope
- The Events and their fields: `AW-SRV-015`.
- Second-person rendering of the viewer's own Character: a later story, if Brian wants it (SPRINT-02
  game-design question).
- The `perceived_from` row: `AW-SRV-029` (feedback in `docs/feedback/AW-CLI-004-play.md` §2).

## Acceptance criteria

1. **Given** a `CharacterLinkdead` Event for `Aldric` **when** it is rendered **then** the player
   reads exactly one line, `Aldric goes linkdead.`
2. **Given** a `CharacterReconnected` Event for `Aldric` **when** it is rendered **then** the line
   is `Aldric reconnects.`
3. **Given** a `CharacterDespawned` Event for `Aldric` **when** it is rendered **then** the line is,
   by reason: `QUIT` and `SWITCH` → `Aldric leaves the world.`; `LINKDEAD` and `LINKDEAD_CEILING`
   → `Aldric fades from the world.`; an unknown reason → `Aldric leaves the world.`
4. **Given** any of the three Events **when** it is rendered **then** the line contains no
   `character_id`, tick, deadline, or reason code.
5. **Given** `--output json` **when** any of the three arrives **then** stdout carries its envelope
   as one JSON line, and nothing is written to stdout in prose.
6. **Given** the golden recording **when** `go test ./admin/cli/...` runs **then** each row and
   each `DespawnReason` has a golden line, and removing any row fails the test.

## Interface contract

Renderer rows in `admin/cli/render.go`, keyed on the Event payload. The name comes from the Event's
`character_name` field. That field is **required of `AW-SRV-015`'s amended contract**
(`docs/feedback/AW-SRV-015-linkdead.md` §3): the sketch carries only `character_id`, which a
bystander's client can't turn into a name.

```
// CONTRACT SKETCH — not an implementation
CharacterLinkdead{character_name}               -> "<name> goes linkdead."
CharacterReconnected{character_name}            -> "<name> reconnects."
CharacterDespawned{character_name, QUIT|SWITCH} -> "<name> leaves the world."
CharacterDespawned{character_name, LINKDEAD|LINKDEAD_CEILING} -> "<name> fades from the world."
```

No new flags, config keys, or exit codes.

## Data / state impact

None. The renderer holds no state.

## Observability requirements

Per `AW-CLI-001`: output only. No metrics, logs, traces, or alerts added. The Events' own
instrumentation is `AW-SRV-015`'s.

## Test plan

- **Unit:** a table over the three payloads and every `DespawnReason`, plus an unknown reason
  value; AC-4's negative assertions on each line.
- **Integration:** the golden recording extended with the three Events.
- **Manual/operator:** `make stack-linkdead` (`AW-INF-017`) asserts the bystander's transcript.

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` The four player-facing lines above are placeholders in the renderer's voice, like
  `arrives`/`leaves`. Their wording is Brian's (SPRINT-02 game-design question 2), and changing it
  changes the golden file, not the contract.
- `[ASSUMPTION]` `AW-SRV-015`'s amendment adds `character_name` to all three Events. If
  architecture decides otherwise, this story's contract changes with it.
