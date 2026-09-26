---
id: AW-CLI-008
title: andara-cli play renders linkdead, reconnect, and despawn
epic: EPIC-08
component: cli
type: feature
status: done
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
`look` is the server's, carried in `RoomDescribed.linkdead` (`AW-SRV-015` AC-1), the linkdead subset of
`occupants`. The `Here:` row appends ` (linkdead)` to each name that appears in both.
*(Amended at architecture's contract review, 2026-09-26: the draft had the marker inside
`occupants`, and a name there is kept to only a name.)*

## User story

As a player, I want to read when someone in my Room loses their connection, comes back, or leaves
the world, so that the linkdead mechanic Brian decided to make visible is actually visible to me.

## Scope

### In scope
- One `renderEvent` row each for `CharacterLinkdead`, `CharacterReconnected` and
  `CharacterDespawned` (every `reason`).
- The `Here:` row of `RoomDescribed` marks the names in `linkdead`.
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
   by reason: `quit` and `switch` → `Aldric leaves the world.`; `linkdead` and `linkdead_ceiling`
   → `Aldric fades from the world.`; an unknown or empty reason → `Aldric leaves the world.`
4. **Given** any of the three Events **when** it is rendered **then** the line contains no
   `character_id`, tick, deadline, or reason code.
5. **Given** `--output json` **when** any of the three arrives **then** stdout carries its envelope
   as one JSON line, and nothing is written to stdout in prose.
6. **Given** the golden recording **when** `go test ./admin/cli/...` runs **then** each row and
   each `reason` has a golden line, and removing any row fails the test.
7. **Given** a `RoomDescribed` with `occupants: [Aldric, Brin]` and `linkdead: [Aldric]` **when**
   it is rendered **then** the `Here:` line reads `Here: Aldric (linkdead), Brin`. A name in
   `linkdead` that isn't in `occupants` is ignored.

## Interface contract

Renderer rows in `admin/cli/render.go`, keyed on the Event payload. The payloads are on `main` in
`andara/game/v1/event.proto` (`character_linkdead = 20`, `character_reconnected = 21`,
`character_despawned = 22`), each with `zone_id`, `room_id` and `character_name`. The name comes
from `character_name`. `CharacterDespawned.reason` is a string.

```
// CONTRACT SKETCH — not an implementation
CharacterLinkdead{character_name}               -> "<name> goes linkdead."
CharacterReconnected{character_name}            -> "<name> reconnects."
CharacterDespawned{character_name, quit|switch|other} -> "<name> leaves the world."
CharacterDespawned{character_name, linkdead|linkdead_ceiling} -> "<name> fades from the world."
RoomDescribed{occupants, linkdead}              -> "Here: <name>[ (linkdead)], ..."
```

No new flags, config keys, or exit codes.

## Data / state impact

None. The renderer holds no state.

## Observability requirements

Per `AW-CLI-001`: output only. No metrics, logs, traces, or alerts added. The Events' own
instrumentation is `AW-SRV-015`'s.

## Test plan

- **Unit:** a table over the three payloads and every `reason`, plus an unknown and an empty
  reason; AC-4's negative assertions on each line; AC-7's `Here:` cases.
- **Integration:** the golden recording extended with the three Events.
- **Manual/operator:** `make stack-linkdead` (`AW-INF-017`) asserts the bystander's transcript.

## Definition of done

CLAUDE.md §8.

## Open questions

- ~~`[ASSUMPTION]` The four player-facing lines above are placeholders in the renderer's voice, like
  `arrives`/`leaves`. Their wording is Brian's (SPRINT-02 game-design question 2), and changing it
  changes the golden file, not the contract.~~
  **Resolved at §8, 2026-09-26:** the lines ship as the renderer's wording. Brian's answer to
  SPRINT-02 game-design question 2 changes the golden file, not the contract, and doesn't reopen
  this story.
- **Resolved 2026-09-26 (architecture's contract review of `AW-SRV-015`):** all three Events carry
  `character_name`, and the linkdead marker is `RoomDescribed.linkdead`.

## Verification (implementation, 2026-09-26)

On `impl/aw-cli-008-linkdead-render`.

| AC | Evidence |
|----|----------|
| 1, 2, 3, 4 | `TestRender_LinkdeadRows`: a table over the three payloads and every `reason`, including an unknown one (`banished`) and an empty one. Each is one line, and the test asserts it carries no event ID, tick, zone, room or reason code |
| 5 | `TestPlay_JSONOutputCarriesTheLinkdeadEvents`: the three envelopes are on stdout as JSON lines, with none of their prose |
| 6 | `testdata/play/events.jsonl` gains a line per row and per reason, and `transcript.txt` is regenerated. `TestRender_RecordingCoversTheTable` now requires all three types. Removing the `CharacterReconnected` row fails `TestRender_Golden` (mutation-checked) |
| 7 | `TestRender_HereMarksTheLinkdead`: `Here: Aldric (linkdead), Brin`. A `linkdead` name that isn't an occupant is ignored, and so is its order. The golden recording has that case too |

`admin/README.md`'s rendering table gains the three rows and the marker. The live bystander's
transcript is `make stack-linkdead` (`AW-INF-017`). It needs `AW-SRV-015` (#114), which emits these
Events and fills `RoomDescribed.linkdead`.

## §8 review (architecture, 2026-09-26)

On `arch/sprint-02-review-3`, against `main` at `5c5d83c`. **`done`.**

| §8 item | Holds? | Evidence |
|---------|--------|----------|
| Every AC passes | yes | The verification table's tests pass. Live: `make stack-linkdead` (`AW-INF-017`, #119) against a stack built from `5c5d83c`, where the bystander read `<A> goes linkdead.`, `Here: <A> (linkdead)`, `<A> reconnects.` and `<A> leaves the world.` from the real server |
| Tests run in CI | yes | `admin/cli` is in `make test`; the bystander transcript is the `stack` workflow's `make stack-linkdead` step |
| `make check` | yes | clean |
| Instrumentation | n/a | output only, per `AW-CLI-001` |
| Config documented | yes | no new config; `admin/README.md`'s rendering table has the three rows and the marker |
| Migrations | n/a | none |
| Glossary | yes | Linkdead, Despawn, Dormant exist |
| No `[ASSUMPTION]` | yes | resolved above |
