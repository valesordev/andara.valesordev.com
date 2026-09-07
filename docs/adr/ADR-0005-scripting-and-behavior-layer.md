---
id: ADR-0005
title: NPC and quest behavior — Python behavior agents outside the tick
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

NPC Behaviors, quest logic, triggers, and dialogue need to drive the world. Brian has decided on
**Python**, with a decent OOP surface, chosen with an eye toward neural nets and LLMs later.

Python is the right language for that goal. The decision this ADR has to make is not *which language*
— that is settled — but *where the interpreter runs*, and that turns out to be the whole ADR, because
the obvious answer breaks two guarantees the rest of the architecture is built on.

### Why "embed CPython in the tick loop" cannot be the answer

**It breaks replay determinism, which recovery depends on.** ADR-0002 recovers the World by replaying
the Command log from a snapshot. That is only correct if applying a Command is a pure function of
state and input. Embedded Python is not: `set` iteration order varies with `PYTHONHASHSEED`, `id()` is
address-dependent, GC timing is observable, and any of these leaking into a Behavior's decision means
replay produces a different World than the one players were in. That failure appears only during a
recovery — the worst possible time to find it, and the one time nobody can afford to debug it.

**It cannot be cost-bounded cheaply.** Bounding a CPython Behavior means `sys.settrace` (a large
constant-factor slowdown on every line), or `RestrictedPython`, or a watchdog that cannot safely
interrupt C-level calls. Meanwhile the tick budget is shared across every NPC in every Zone the
process owns.

**And it defeats the stated goal.** An LLM call is 100 ms to 10 s of network I/O. It can never be
inside a deterministic replay path, and it can never be inside a tick. The reason Python was chosen
is the reason it must not run where a tick can wait on it.

## Decision

**Python Behaviors run out-of-process as Behavior Agents: separate processes deployed inside the server
realm, behaving as clients. An Agent submits Commands through the same path a player's client uses. The
simulation never executes Python.**

"Client, but inside the realm" is the precise framing, and it settles the trust question that
out-of-process execution alone would leave open. An Agent is a client in *protocol* terms — same gRPC
service, same Command pipeline, no privileged path — and an in-cluster workload in *deployment* terms.
That combination is what makes the design safe:

- Agents authenticate with in-cluster workload identity (mTLS / service account) rather than
  player-style credentials, so there are no long-lived Agent passwords to leak.
- Agents reach exactly one thing: the gRPC endpoint. **No direct Kafka access, no datastore access, no
  projection access**, enforced by network policy. An Agent that is compromised or badly written can
  submit bad Commands for its NPCs — which authorization already bounds — and can do nothing else.
- Agents scale as their own `Deployment`, independent of the simulation `StatefulSet`.

The residual risk to be explicit about: if Builders write Behaviors (ADR-0004 makes Builders untrusted
and repository-less), then untrusted Python runs inside the cluster. The process boundary contains
what it can *do to the World*; the network policy contains what it can *reach*. Neither contains
resource exhaustion on the Agent host, so Agents run under CPU and memory limits and a hostile Behavior
degrades its own Agent's NPCs and nothing else.

```
   ┌──────────────────────────── server realm ────────────────────────────┐
   │                                                                      │
   │   andara-server ──── Subscribe (perception-scoped) ────▶ ┌─────────┐ │
   │   (StatefulSet)                                          │ Behavior│ │
   │        ▲                                                 │  Agent  │ │
   │        └──────────── Submit (same RPC as a player) ───────│ (Python)│ │
   │                                                          └─────────┘ │
   │                                                          Deployment, │
   │   Kafka / Redis / Postgres  ✗ no route from Agents        cpu+mem     │
   │                                                           limited     │
   └──────────────────────────────────────────────────────────────────────┘
```

1. An Agent subscribes to Events, scoped by the perception rules in `AW-SRV-004` — an NPC perceives
   what its Character could perceive, no more. The scoping is already computed inside the sim, so this
   is free and is a real security property: a Builder's Python cannot see the World.
2. An Agent decides, in Python, taking as long as it needs. It may call a model, hit a vector store,
   or block on anything at all.
3. An Agent submits Commands over `andara.game.v1.Game`, authenticated as the NPC it drives, through
   parse → authorize → produce-to-log → validate → apply. The identical pipeline a player uses.
4. The log records the resulting **Commands**. Replay replays those Commands. **Replay never re-runs
   Python.** Determinism is preserved by construction, not by restricting what Python may do.

### What this buys

- **Determinism is unconditional.** Behaviors can be as non-deterministic as they like — sample from a
  model, read the clock, use randomness — and recovery is still exact, because the log holds what they
  decided rather than how they decided it.
- **The tick cannot be stalled by a Behavior.** There is no bound to enforce because there is no
  Behavior in the tick. A slow NPC is a slow NPC, not an outage.
- **LLM and neural-net use is natural**, which was the point. An NPC that takes two seconds to answer
  is more believable than one that answers instantly.
- **Sandboxing is a process boundary plus a network policy**, not a language-level sandbox that someone
  will eventually escape. Builders are untrusted (ADR-0004); this is the only containment worth relying
  on.
- **The OOP surface is easy.** `class Behavior` with `on_event` handlers, entity proxy objects, a
  typed action API generated from the same protobuf definitions as the client (ADR-0007). It is an SDK
  against a network API, which is the easiest kind of Python to write well.
- **Agents scale independently.** They are a Deployment, not part of the StatefulSet.

### What this costs

- **Minimum one tick of latency, plus a log round trip, on every NPC reaction.** At MUD tick rates
  this is imperceptible and arguably desirable. It would be disqualifying at 60 Hz.
- **No same-tick reflexes.** A trap that must fire in the same tick the Character steps on it cannot be
  a Python Behavior.
- **An Agent runtime to operate** — a fleet, its own health, its own scaling, its own failure modes.
  This is genuinely more operational surface than an embedded interpreter.
- **NPCs are now clients**, so they need identities and authorization (ADR-0006). Because they are
  in-realm, identity is workload identity rather than a credential, and a compromised Agent is a
  compromised set of NPCs — nothing wider.

### The in-tick reflex layer

For effects that genuinely must resolve in the tick that caused them, a small **declarative** layer —
condition/effect rules evaluated by Go, no general-purpose execution, incapable of unbounded work —
lives in the sim core. Traps, doors, and triggers are its scope.

**Decided 2026-09-07: not built in Phase 1.** Brian confirmed that reactive-one-tick-late is
acceptable, so every reactive effect — traps, doors, triggers — goes through the same one-tick path as
everything else. This removes a whole subsystem from Phase 1 and removes the only place general
execution would have run inside the tick. If a mechanic later genuinely requires same-tick reaction,
this section is the design that gets built then.

## Consequences

- `AW-SRV-009` changes shape completely: from "bounded execution substrate inside the tick" to
  "Agent protocol, identity, and runtime," plus a separate Python SDK story.
- Agents are an in-cluster workload with their own `Deployment`, network policy, and resource limits.
  That belongs in the Kubernetes work (`AW-INF-003`, `AW-INF-006`), not only in `AW-SRV-009`.
- The Command pipeline gains a second class of client. `authorize` must distinguish player Sessions
  from Agent Sessions, and rate limits differ.
- Behaviors become independently deployable and independently version-skewed from the server. The
  protobuf contract is what holds them together (ADR-0007).
- Behavior state — an NPC's memory — is a design question this ADR deliberately leaves open. If it
  lives in the Agent, it is outside the World snapshot and will drift from a recovered World. If it
  lives in World state, it is snapshotted and consistent but must be expressible in protobuf.
  **Recommendation: World state**, so that a recovery restores NPC memory along with everything else.
  `[NEEDS BRIAN]` for confirmation, because it constrains what an LLM-driven NPC can remember.
- **We are foreclosing** any Python inside `server/sim`. The `depguard` boundary is the enforcement.

## Revisit when

- A gameplay system genuinely requires same-tick reaction, which would mean building the Reflex Rule
  layer described above.
- Agent round-trip latency becomes perceptible, which means the Tick Rate has risen a long way.
- Agent fleet operations cost more than an embedded interpreter's determinism problems would have —
  a trade to re-evaluate honestly, not a decision to defend.
