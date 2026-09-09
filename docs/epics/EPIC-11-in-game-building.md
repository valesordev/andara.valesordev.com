---
id: EPIC-11
title: In-game building for admins and builders
phase: 2
component: server
milestone: post-Phase-1
status: draft
adr_gates: []
adr_refs: [ADR-0004, ADR-0009]
---

## Goal
A Builder or Admin shapes the World from inside it — creating and editing Rooms, Exits, Items, and
NPCs while connected — and what they make is published through the same content pipeline as anything
authored offline.

## Why now
It is not now. This epic exists because ADR-0004 said in-game building deserved its own epic if it
turned out to be a design goal rather than a convenience, and on 2026-09-07 Brian confirmed it is a
design goal — for Admins and Builders, never for players.

Naming it now costs nothing and prevents the failure it is here to prevent: `andara-cli` quietly
becoming the only thing that can write content, so that adding a second authoring surface later means
unpicking assumptions rather than adding a feature. Every story that touches the content path should
be able to point at this epic and ask whether it is foreclosing it.

## In scope
- In-session authoring commands and their authorization, distinct from Game Master intervention:
  a GM changes the running World's *state*, a Builder changes its *content*.
- Publishing from inside the World through the same blobs, versions, and Active Pointer as
  `andara-cli` — never a second write path into the content topics.
- How in-session edits interact with the two-person activation rule (`AW-SRV-013`), which is the
  hardest question here: an authoring loop fast enough to be pleasant and an approval step in front of
  the live World are in direct tension.

## Out of scope
- Player-facing building of any kind.
- A graphical or web editor. A different surface, the same pipeline.

## Done when
Not defined. This epic is not groomed and has no stories; it is a placeholder with a rationale.

## Open questions
- `[NEEDS BRIAN]` What "Builders can build new game types that subtype server types" means. See the
  glossary and ADR-0009 — it may belong to this epic, to the content model, or to both.
- `[NEEDS BRIAN]` Whether an in-session edit is visible to the Builder immediately and to everyone
  else only after approval, which is the only shape that reconciles a fast loop with moderation.

## Stories
None. Deliberately.
