---
id: EPIC-03
title: Command pipeline and gRPC gateway
phase: 1
component: server
milestone: M1
status: ready
adr_gates: []
adr_refs: [ADR-0002, ADR-0003, ADR-0007]
---

## Goal
A human runs `andara-cli play`, is bound to a Session over gRPC, types `look` and `north`, and sees
the World respond — with the Command having been durably ordered in Kafka before it was applied.

## Why now
M1's gate. The Command Pipeline is now **split across the log boundary** (ADR-0002 §2), which makes
the Gateway half of the pipeline rather than a layer above it:

```
Gateway:  parse ──▶ authorize ──▶ produce to andara.commands.v1 ──▶ ack "accepted"
Tick:     consume ──▶ validate ──▶ apply ──▶ emit Events ──▶ stream to Sessions
```

`parse` is stateless and `authorize` needs only Session and Account knowledge, so both run before the
produce — which also keeps a hostile client's garbage out of the log. `validate` and `apply` need
authoritative World state, so both run inside the tick after the consume.

## In scope
- The five pipeline stages as separable, individually testable units, split across the log boundary.
- Verb table, Intent parsing, argument binding, and a typed error taxonomy per stage.
- `look` and `move`, the two commands M1's gate requires.
- gRPC service: Session establishment, Protocol version negotiation, Command submission.
- Server-streaming Event subscription with per-Session backpressure that never reaches the tick.
- `andara-cli play` as the Text Interface.

## Out of scope
- Authentication — `EPIC-08`. Sessions are anonymous against a stub permission model.
- Every other verb. Combat, inventory, and social verbs belong to the epics that define them.
- Perception scoping, which is computed in the sim — `EPIC-02`. The Gateway filters nothing.

## Done when
Two terminals connected simultaneously see each other's movement, a deliberately stalled client does
not increase tick duration, and a Command rejected at `authorize` never appears in the log.

## Stories
`AW-SRV-003`, `AW-SRV-005`, `AW-SRV-010`, `AW-SRV-011`, `AW-CLI-004`
