---
id: AW-SRV-016
title: Python behavior SDK and agent runtime
epic: EPIC-09
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-009]
blocks: []
assignee: cursor
risk: medium
---

> `status: draft` — unblocked by ADR-0005 and scoped. Groomed to `ready` when M4 approaches.

## Context

ADR-0005 chose Python for Behaviors with "a decent OOP style", executed out of process. `AW-SRV-009`
builds the server-side boundary; this story builds the thing a Builder actually writes.

Out-of-process makes the SDK the easiest kind of Python to write well: it is a client library against a
network API, generated from the same protobuf definitions as the game client (ADR-0007). A Behavior can
be as slow, as stateful, and as non-deterministic as it likes — including calling a model — because the
log records what it decided rather than how.

## User story

As a builder, I want to write an NPC as a Python class with event handlers, so that giving a character
behavior feels like writing a program rather than filling in a configuration file.

## Scope

### In scope
- `class Behavior` with typed event handlers and a lifecycle (`on_attach`, `on_event`, `on_detach`).
- Entity proxy objects: reading an NPC's perceived surroundings as objects rather than as raw protobuf.
- A typed action API generated from the protobuf definitions, so an invalid action is a type error rather
  than a server rejection.
- The Agent runtime: connect, authenticate, claim NPCs, dispatch Events to Behaviors, submit Commands.
- Local development affordances: run a Behavior against a local stack, and a replay harness that feeds a
  recorded Event stream to a Behavior for testing without a server.
- Packaging and distribution to Builders who have no repository access.

### Out of scope
- The server-side protocol and identity — `AW-SRV-009`.
- Model and vector-store integration. The SDK must not prevent it and must not build it.
- Any deterministic guarantee about Behavior code, which is exactly what out-of-process execution makes
  unnecessary.

## Acceptance criteria (known now; completed at grooming)

1. **Given** a Behavior subclass with an `on_event` handler **when** an Event its NPC perceives arrives
   **then** the handler is called with a typed object, not a raw message.
2. **Given** a handler that submits an action **when** it returns **then** a Command is submitted for the
   correct NPC through the same pipeline a player uses.
3. **Given** a handler that raises **when** it raises **then** the exception is caught, logged with the
   Behavior name and the triggering Event, the NPC continues to exist unattended, and other Behaviors on
   the same Agent are unaffected.
4. **Given** a handler that blocks for thirty seconds **when** it blocks **then** other Behaviors on the
   same Agent continue to be dispatched. One slow NPC must not stall an Agent any more than it stalls the
   World.
5. **Given** an invalid action — a direction that is not an Exit, a target that is not present **when** it
   is constructed **then** the SDK surfaces it locally where possible, and otherwise the server's typed
   rejection is delivered back to the Behavior rather than swallowed.
6. **Given** a recorded Event stream **when** it is fed to a Behavior through the replay harness **then**
   the Behavior runs with no server, so a Builder can unit-test behavior.
7. **Given** a Builder with no repository access **when** they install the SDK **then** it installs from a
   published package with a pinned protocol version.

## Interface contract

To be written at grooming. Sketch of the shape ADR-0005 calls for:

```python
# CONTRACT SKETCH — not an implementation

class Innkeeper(Behavior):
    async def on_attach(self, npc: NPC) -> None:
        self.regulars: set[str] = set()

    async def on_character_arrived(self, npc: NPC, ev: CharacterArrived) -> None:
        if ev.character.name in self.regulars:
            await npc.say(f"Welcome back, {ev.character.name}.")
        else:
            self.regulars.add(ev.character.name)
            await npc.say("Sit anywhere you like.")
```

The `await` in a handler is the whole point: it may be a model call taking two seconds, and nothing in the
World waits on it.

## Data / state impact

Behavior instance state — `self.regulars` above — is Agent-local and is **lost when the Agent restarts**,
whereas NPC memory intended to survive belongs in World state (`AW-SRV-009`). The SDK must make that
distinction obvious in its API rather than leaving Builders to discover it, because the failure mode is an
NPC that quietly forgets everything after a deploy.

## Observability requirements

- **Metrics:** emitted by the Agent runtime, not by the server: `andara_behavior_dispatches_total`
  (counter, label `behavior`), `andara_behavior_duration_seconds` (histogram, label `behavior`),
  `andara_behavior_errors_total` (counter, label `behavior`). Behavior name is authored and bounded; NPC
  instance ID is rejected.
- **Logs:** structured, with `behavior`, `npc_id`, and the triggering `event_id`.
- **Traces:** a Behavior's decision is its own trace, linked to the triggering Event and the resulting
  Command, per `AW-SRV-009`.
- **Alerts:** none in the SDK. `NPCsUnattended` from `AW-SRV-009` is the symptom that matters.

## Test plan

Handler dispatch and typing; exception containment; concurrent dispatch under a blocking handler; the
replay harness; package installation from a clean environment with no repository access.

## Definition of done

CLAUDE.md §8, plus: the replay harness works without a running server, and the Agent-local versus World
state distinction is documented in the SDK's own README with a worked example.

## Open questions

- `[ASSUMPTION]` `async` handlers, because ADR-0005's entire premise is that a Behavior may take a long
  time and must not block its peers.
- `[NEEDS BRIAN]` Whether Builders write Behaviors at all, or whether Behaviors are a Developer artifact
  and Builders only reference them by name. ADR-0004 says Builders are untrusted, which the process
  boundary handles; the question is whether they should be writing code.
- `[ASSUMPTION]` Published as a versioned package pinned to a protocol version, so an SDK upgrade is a
  deliberate act.
