---
id: ADR-NNNN
title: <decision subject, stated as a noun phrase>
status: draft             # draft | proposed | accepted | rejected | superseded by ADR-XXXX
date: YYYY-MM-DD
deciders: [brian]
gates: []                 # epic/story IDs that cannot reach `ready` until this is accepted
---

<!--
status meanings:
  draft     — the decision is identified and scoped, options not yet argued to a conclusion.
              Gates stories exactly as `proposed` does. Write one when a story needs to name a
              real ADR ID for something not yet decided.
  proposed  — options argued, a decision recommended, awaiting Brian.
  accepted  — decided. Clear `gates` and record the constraint in the affected epics' `adr_refs`.
-->

## Context

What forces are in play. What is true today. Why this decision is being made now rather than later
or earlier. Include the constraint that makes the choice non-obvious — if there isn't one, this
doesn't need an ADR.

## Options considered

### Option A — <name>

How it works, in enough detail to cost it.

**Good:** …
**Bad:** …
**Costs us:** the thing we will actually dislike about living with this.

### Option B — <name>

…

## Decision

The chosen option, stated flatly. Not "we lean toward" — pick one.

## Consequences

What becomes true. Include the consequences we will not like. Include what this forecloses.

## Revisit when

The concrete, observable signal that would make us reopen this. A number where possible.
