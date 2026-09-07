---
id: EPIC-06
title: Operator and builder CLI
phase: 1
component: cli
milestone: M1-M3
status: in-progress
adr_gates: []
adr_refs: [ADR-0003, ADR-0004, ADR-0007]
---

## Goal
`andara-cli` is the only tool an Operator or Builder needs, and it reaches the server exclusively
over the same versioned Protocol as every other client. No back door, no direct datastore writes.

## Why now
ADR-0003 made `andara-cli` load-bearing for three audiences rather than one: it is the Operator's
tool, the Builder's tool, and — since a human cannot telnet into a gRPC server — the *player's* tool.
The skeleton is therefore M1 work, and every later command lands in a shape established before there
were many commands to be inconsistent about.

## In scope
- Command tree, global flags, config file and env precedence, output formats (human and JSON).
- Server connection over the Protocol, with version negotiation and typed errors.
- Operator commands: world status, session list, snapshot trigger, content activation, broadcast.
- Account administration in `closed` and `invite` modes, which ADR-0006 makes an Operator duty since
  there is no self-service password reset in Phase 1.

## Out of scope
- `andara-cli play` — `EPIC-03`, because ADR-0003 makes the Text Interface a Protocol client and it
  belongs with the Protocol it exercises.
- Content commands — `EPIC-05`, where the pipeline they drive lives.
- Game Master in-world powers — game commands over the same Protocol, belonging with the epic that
  defines them.
- Any command that bypasses the Protocol.

## Done when
Every operational procedure in `docs/runbooks/` is executable through `andara-cli`, with no step
that says "connect to the database".

## Stories
`AW-CLI-001`
