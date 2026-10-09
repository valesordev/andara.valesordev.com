# FEAT-01 — Walk a multi-zone settlement
Status: proposed
Release: R1 (must)

## Problem
Brian, the only R1 player, can only walk dev content, the `town` fixture with spawn Room `town/plaza` (`docs/roadmap.md`, 2026-09-21;
the walk to the Town Hall is in `docs/sprints/SPRINT-04-demo.md`). That proves the server moves a Character between Rooms. It doesn't test R1's bet, that an authored place is
worth returning to. No real settlement pack exists: the content board holds only canon stories
(`AWC-CAN-*`), and C1's pack stories aren't cut.

## Outcome
I enter the world in a settlement I'd recognise as a place: several zones with their own character,
joined by exits I can follow on foot. Every Room I stand in was written for this village, and I can
reach every zone from where I spawn.

## Success signal
Brian runs it on `dev`, once the C1 pack is published: he walks from the spawn Room to every settlement
zone by exits alone, then rolls the pack back. CI on the content PR checks the pack separately: it passes `make check` at the pinned
tag (C1 gate), where the compiler's `unknown_room` diagnostic rejects a dangling exit.
Brian records in the R1 review whether a zone he reached felt like somewhere, in his words. That
judgment is his; the walk is the pass/fail.

## Scope
- In: a real pack of 3 to 5 settlement zones with Rooms and exits from approved zone sheets (C1's gate); a
  spawn Room inside the settlement; every zone reachable from spawn by exits; `TODO(brian)` room text where C1 allows it.
- Out: NPCs that act, Items, combat (R2: each tests a different bet); surrounding zones outside the
  settlement (R2 `cut-first`, C3); the §18 room counts for the full region (C3); the room-text `director`
  pass beyond what C1's gate requires (follows C1); any new server capability (C1 "waits on nothing new").

## Dependencies
| Need | Owner | State |
|---|---|---|
| Starting village identity and map | world-builder (Brian approves) | C0 gate (tracker §1-§5, Phase 3 settlement identity): `needs world-builder`; no AWC story for the village identity yet (CYCLE-01 holds only AWC-CAN-001 to 003) |
| Zone sheets for 3 to 5 settlement zones | producer | `needs a C1 story`, none cut yet |
| `.aw` pack authored from the sheets | producer / content-developer | `needs a C1 story` |
| Publish and rollback of a Zone without deploy | pm / implementation | M3 gate, passed on test content in SPRINT-03 |
| Pack name | Brian | open (C1 states it is his) |

## Decisions
- Must for R1, no cut-first — the walk is the whole bet; without it R1 only repeats M1.
- Success is the walk plus Brian's recorded judgment, not a room count — counts are C3's and a number says nothing about whether it feels like a place.
- Spawn sits inside the settlement — otherwise the first thing a player sees is not the village.

## Open for Brian
- Pack name (C1 leaves it to Brian) — recommendation: decide before the first zone story is cut, because it appears in every publish and rollback.

## Sources
Vision principles 1 and 3; `docs/product/releases.md` R1; content roadmap C0, C1; `docs/roadmap.md` M3 and Phase 1
exit criterion 1; `docs/sprints/SPRINT-04-demo.md`. No open `product` issues.

## Log
- 2026-10-09: proposed
