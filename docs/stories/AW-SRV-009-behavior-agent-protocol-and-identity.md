---
id: AW-SRV-009
title: Behavior agent protocol, identity, and runtime boundary
epic: EPIC-09
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-008, AW-SRV-011]
blocks: [AW-SRV-016]
lane: implementation
risk: high
---

> `status: draft` — unblocked by ADR-0005 and scoped. Groomed to `ready` when M4 approaches.

## Context

ADR-0005 decided that Python Behaviors run **out of process** as Behavior Agents: an Agent subscribes to
perception-scoped Events, decides in Python taking as long as it needs, and submits Commands through the
same gRPC service a player's client uses. The simulation never executes Python.

That decision is what preserves replay determinism — the log records what an Agent decided, not how, so
recovery replays Commands and never re-runs Python — and it is what makes LLM and neural-net use
possible at all, since a model call can never live inside a tick.

This story builds the server side of that boundary. `AW-SRV-016` builds the Python SDK on top of it.

## User story

As a builder, I want NPCs to act on their own without any one of them being able to stop the world, so
that I can write behavior without being able to take the server down.

## Scope

### In scope
- Agent identity: the `agent` role from ADR-0006, using **in-cluster workload identity (mTLS / service
  account)** rather than player-style credentials — Agents are processes inside the server realm
  (ADR-0005), so there are no long-lived Agent passwords to leak — plus the mapping from one Agent
  Session to the many NPCs it drives.
- Perception-scoped Event subscription for NPCs, reusing the scoping already computed in `AW-SRV-004`.
  A Builder's Python must not be able to see the World.
- Agent-specific rate limits, distinct from player Sessions (`AW-SRV-010` already reserves the knob).
- NPC ownership and takeover: what happens when an Agent dies holding NPCs, and how another claims them.
- NPC memory as **World state**, so a recovery restores it along with everything else. The constraint
  it carries: memory must be expressible in protobuf, which bounds what an LLM-driven NPC can retain.
- The idle-NPC policy: an NPC whose Agent is gone continues to exist and simply stops acting.

### Out of scope
- The Python SDK — `AW-SRV-016`.
- Any Python inside `server/sim`, enforced by `depguard`.
- Model and vector-store integration. The architecture makes it possible; the models are a later product
  decision.
- The in-tick Reflex Rule layer. **Decided 2026-09-07: not built in Phase 1.** Brian confirmed
  reactive-one-tick-late is acceptable, which at 10 Hz costs 100 ms (ADR-0008). Every reactive effect
  goes through the one-tick path, and no general execution runs inside the tick at all.
- Agent `Deployment`, network policy, and resource limits — `AW-INF-003` and `AW-INF-006`.

## Acceptance criteria (known now; completed at grooming)

1. **Given** an Agent that hangs for thirty seconds **when** the World keeps ticking **then** tick
   duration is unaffected, its NPCs simply stop acting, and an Event records that they are unattended.
2. **Given** the same period **when** it is replayed from a snapshot **then** the World reproduces
   exactly, because replay replays the Commands the Agent submitted and never the Python that produced
   them. This is the acceptance criterion that would be impossible under an embedded interpreter.
3. **Given** an Agent subscribed for an NPC **when** an Event occurs that the NPC could not perceive
   **then** the Agent does not receive it.
4. **Given** an Agent that dies **when** its Session drops **then** its NPCs remain in the World,
   unattended, and are claimable by another Agent without duplication — two Agents must never both drive
   one NPC.
5. **Given** an Agent submitting faster than its rate limit **when** the limit is exceeded **then**
   excess Commands are rejected and the Agent Session survives.
6. **Given** an NPC with memory **when** the World is recovered **then** that memory is restored, because
   it is World state rather than Agent state.
7. **Given** a running Agent process **when** its network egress is inspected **then** it can reach the
   gRPC endpoint and nothing else — no Kafka, no Redis, no Postgres, no projection. Asserted by test, not
   only by policy: "clients inside the realm" is safe only if the realm actually restricts them.

## Interface contract

To be written at grooming. It will cover: Agent authentication and the NPC claim protocol, the
subscription shape for many NPCs on one Session, the ownership and takeover state machine, and the
memory representation.

Committed now: an Agent's Commands go through `parse → authorize → produce → validate → apply`, exactly
as a player's do. There is no Agent-privileged path into the simulation.

## Data / state impact

NPC memory as World state is the significant decision here, and ADR-0005 recommends it: memory in the
Agent would drift from a recovered World, while memory in World state is snapshotted and consistent.
The cost is that it must be expressible in protobuf, which constrains what an LLM-driven NPC can
remember — a real limitation worth confirming rather than discovering.

## Observability requirements

- **Metrics:** `andara_agents_connected` (gauge), `andara_npcs_attended` / `andara_npcs_unattended`
  (gauges), `andara_agent_commands_total` (counter, label `outcome`),
  `andara_agent_decision_latency_seconds` (histogram — how long an Agent took, measured from the Event
  it reacted to). NPC instance ID is rejected as a label; behavior name is bounded and acceptable.
- **Traces:** an Agent's decision appears as its own trace rooted at the Agent, linked to the Event that
  triggered it and to the Command it produced. Not a span inside `sim.tick`, which is the point.
- **Alerts:** `NPCsUnattended` — symptom-based: the world's NPCs have stopped behaving, which players
  notice. Tied to an SLO written in this story. No alert on Agent decision latency, which is a cause.

## Test plan

The hang test from AC-1 asserting no tick impact; the replay-determinism test from AC-2 covering a period
containing Agent activity; a scoping test asserting an Agent cannot see beyond its NPC's perception; a
concurrent-claim test asserting no double ownership.

## Definition of done

CLAUDE.md §8, plus: the replay-determinism test including Agent activity gates merges, and the
`depguard` boundary confirms no Python dependency entered `server/sim`.

## Open questions

- `[ASSUMPTION]` One Agent Session drives many NPCs, rather than one Session per NPC. Per-NPC Sessions
  would be simpler to reason about and would not scale past a few hundred NPCs.
- `[NEEDS BRIAN]` What an unattended NPC should do — stand still, or fall back to a minimal idle
  behavior. A design call with visible consequences.
