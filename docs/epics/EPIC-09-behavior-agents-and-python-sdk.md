---
id: EPIC-09
title: Behavior agents and Python SDK
phase: 1
component: server
milestone: M4
status: ready
adr_gates: []
adr_refs: [ADR-0005, ADR-0003, ADR-0007]
---

## Goal
NPCs act on their own, driven by Python running in separate processes **inside the server realm** — clients
in protocol terms, in-cluster workloads in deployment terms — without any Behavior being able to stall the
World or break replay determinism.

## Why now
Last of Phase 1. Behavior Agents are gRPC clients, so they need the Gateway, the Event stream, and
Agent identity — all of which arrive earlier.

## In scope
- Agent identity and authorization: the `agent` role, in-cluster workload identity rather than long-lived
  credentials, distinct rate limits from player Sessions, and a network policy permitting Agents to reach
  the gRPC endpoint and nothing else.
- Perception-scoped Event subscription for NPCs, reusing the sim's existing scoping.
- The Python SDK: `class Behavior` with event handlers, entity proxies, a typed action API generated
  from the same protobuf definitions as the client.
- Agent runtime: deployment shape, health, scaling, and the failure policy when an Agent dies holding
  NPCs.
- NPC memory as World state, so a recovery restores it along with everything else.

## Out of scope
- Specific NPC content. This epic delivers the substrate; Builders supply behavior.
- Any Python inside `server/sim`. Enforced by `depguard`, not by review.
- Neural-net and LLM integration itself. The architecture makes it possible; the models are a later
  product decision.
- The in-tick Reflex Rule layer. **Not built in Phase 1** — reactive-one-tick-late is accepted, which at
  10 Hz costs 100 ms (ADR-0005, ADR-0008). No general execution runs inside the tick at all.

## Done when
An Agent that hangs for thirty seconds affects no tick, its NPCs simply stop acting, an Event says so,
and a replay of that period reproduces the World exactly — because replay replays the Commands the
Agent submitted, never the Python that produced them.

## Stories
`AW-SRV-009`, `AW-SRV-016`
