---
id: AW-SRV-016
title: Python behavior SDK and per-pack agent runtime
epic: EPIC-09
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-009]
blocks: []
lane: implementation
risk: medium
---

## Context

ADR-0005 chose Python for Behaviors, executed out of process. `AW-SRV-009` builds the server-side
boundary; this story builds the thing a Builder writes and the runtime that runs it.

**Builders write Behaviors (2026-09-11).** So the runtime is not "a service Developers deploy with code
in the image"; it is `andara-agent`, a fixed image that loads a pack's Behavior blobs from the content
store at start and on every pointer move, in one process per pack. The SDK is what those blobs import,
published as a versioned package pinned to a protocol version, installable by someone with no
repository access.

## User story

As a builder, I want to write an NPC as a Python class with event handlers, so that giving a character
behavior feels like writing a program rather than filling in a configuration file.

## Scope

### In scope
- Package `andara-sdk`: `Behavior` base class with `on_attach`, `on_event`, typed `on_<event>`
  handlers, `on_detach`; `NPC` proxy with a typed action API generated from `gen/python`; `Memory`
  proxy over `andara.core.Memory` slots that makes "this survives restart, `self.*` does not" explicit.
- Runtime `andara-agent`: authenticate (`WORKLOAD_JWT` or `API_KEY`), fetch the pack's active
  version, load `text/x-python` blobs into an isolated module namespace, claim the NPCs whose Templates
  name a Behavior in this pack, subscribe, dispatch per NPC on an `asyncio` task each, heartbeat, reload
  on pointer move.
- Containment inside the process: per-Behavior exception isolation; a blocking handler stalls only its
  NPC; import of the SDK's `unsafe` surface (raw gRPC stub) is denied to pack code.
- Replay harness: `andara-sdk replay --events recording.pb --behavior town.merchant`.
- Local dev: `andara-agent --pack ./town --server localhost:8443` against `make up`.
- `Dockerfile.agent`, published with the server image; the Helm `Deployment` template lands in
  `AW-INF-003`'s chart as `agents[]` values.

### Out of scope
- The server protocol — `AW-SRV-009`. The compiler that validates Templates reference existing
  Behaviors — `AW-CLI-006` (`E_UNRESOLVED` on a missing Behavior name).
- Model and vector-store integration. Allowed by construction; not built.
- A language-level sandbox. Containment is the process, the network policy, and the pack scope
  (ADR-0005); this story does not pretend otherwise.

## Acceptance criteria

1. **Given** a Behavior with `on_character_arrived` **when** an Event its NPC perceives arrives **then**
   the handler is called with a typed `CharacterArrived` and an `NPC` proxy, not raw bytes.
2. **Given** a handler awaiting `npc.say("…")` **when** it returns **then** a `Say` Command is submitted
   for that NPC with a `client_ref` linking it to the triggering `event_id`.
3. **Given** a handler that raises **when** it raises **then** the exception is logged with `behavior`,
   `npc_id`, `event_id`, `andara_behavior_errors_total{behavior}` increments, the NPC stays attended,
   and other NPCs on the Agent are unaffected.
4. **Given** a handler blocking for thirty seconds **when** it blocks **then** other NPCs' handlers keep
   dispatching and heartbeats keep the leases alive.
5. **Given** an invalid action — a Direction not in the closed set, a target not perceived **when**
   constructed **then** the SDK raises locally; **given** a server rejection **then** `CommandRejected`
   is delivered to the Behavior's `on_rejected`, not swallowed.
6. **Given** a recorded Event stream **when** fed through the replay harness **then** the Behavior runs
   with no server and the harness prints the Commands it would have submitted.
7. **Given** a clean machine with no repository access **when** `pip install andara-sdk==<v>` runs
   **then** it installs with `gen/python` pinned to one protocol version, and `andara-agent` refuses to
   start against a server outside that version's range, naming both.
8. **Given** the pack's pointer moves **when** the runtime observes it **then** Behaviors are reloaded
   from the new version's blobs within `agent.reload_debounce`; NPCs stay attended across the reload
   and `on_detach`/`on_attach` are called in that order.
9. **Given** pack code that imports `andara_sdk.unsafe` or opens a socket **when** loaded **then** the
   load fails naming the module and line; the NPCs of that Behavior are left unattended rather than
   driven by code that escaped the API.
10. **Given** `npc.memory[3] = b"…"` **when** set **then** a `SetMemory` Command is submitted and the
    value is readable after a server recovery; **given** `self.x = …` **then** documentation and a
    `Behavior.__setattr__` warning on first use say it will not survive restart.

## Interface contract

```python
# CONTRACT SKETCH — not an implementation
class Behavior:
    async def on_attach(self, npc: NPC) -> None: ...
    async def on_event(self, npc: NPC, ev: Event) -> None: ...      # fallback
    async def on_character_arrived(self, npc: NPC, ev: CharacterArrived) -> None: ...   # typed, one per payload
    async def on_rejected(self, npc: NPC, rej: CommandRejected) -> None: ...
    async def on_detach(self, npc: NPC) -> None: ...

class NPC:
    id: str; room: RoomView; visible: Sequence[EntityView]      # from perceived Events only
    memory: Memory                                              # slots → SetMemory; survives restart
    async def say(self, text: str) -> None: ...
    async def move(self, direction: Direction) -> None: ...      # Direction is the closed enum
```

### Runtime

| Item | Value |
|------|-------|
| entry | `andara-agent --pack <id> [--server addr] [--credential-file f]` |
| Behavior discovery | blobs with `media_type: text/x-python`; a `Behavior` subclass registers as `<pack>.<ClassName>` unless `name=` given |
| Template binding | `andara.core.Behavior{name}` on the NPC's Template must resolve to a registered Behavior in this pack |
| dispatch | one `asyncio.Task` per NPC; Events for an NPC are processed in `event_id` order |
| reload | on `ActiveVersion` change; `agent.reload_debounce` 2 s |
| exit codes | `0` · `1` config · `3` server unreachable · `4` protocol version · `5` credential |

### Configuration (`ANDARA_AGENT_*`)

`PACK`, `SERVER`, `CREDENTIAL_FILE`, `RELOAD_DEBOUNCE` (`2s`), `MAX_CONCURRENCY` (`64` handlers in
flight), `LOG_LEVEL`.

### Packaging

`andara-sdk` on the private index `make up` serves (`pypiserver` in the local stack) and, for Builders,
a wheel attached to each server release; version `MAJOR.MINOR` tracks `protocol_version`.

## Data / state impact

Behavior instance state is Agent-local and lost on restart; NPC memory is World state via `SetMemory`.
The SDK makes the distinction an API boundary (AC-10), because the failure mode is an NPC that quietly
forgets everything after a deploy.

## Observability requirements

- **Metrics (runtime):** `andara_behavior_dispatches_total{behavior}`,
  `andara_behavior_duration_seconds{behavior}`, `andara_behavior_errors_total{behavior}`,
  `andara_agent_reloads_total{outcome}`, `andara_agent_leases_held`. Behavior name is authored and
  bounded; NPC ID is rejected.
- **Logs:** structured JSON with `behavior`, `npc_id`, `event_id`, `pack`, `version`.
- **Traces:** a decision is a trace rooted at the runtime, `trace_id` propagated on the Command.
- **Alerts:** none in the SDK; `NPCsUnattended` (`AW-SRV-009`) is the symptom.

## Test plan

- **Unit (pytest):** handler dispatch and typing; exception containment; concurrency under a blocking
  handler; local action validation; the `unsafe` import guard.
- **Integration:** against `make up` with the `town` fixture pack — end-to-end say/move; pointer-move
  reload (AC-8); kill-and-recover with memory (AC-10); clean-environment install (AC-7).
- **Manual/operator:**
  ```
  pip install andara-sdk==0.1.0
  andara-sdk replay --events testdata/market.pb --behavior town.Merchant   # expect: Commands printed
  andara-agent --pack town --server localhost:8443                        # expect: "claimed 12 NPCs"
  ```

## Definition of done

CLAUDE.md §8, plus: the replay harness and the reload test in CI; `docs/sdk/python.md` written for a
Builder, including the `self.*` versus `memory` rule.

## Open questions

- **Resolved 2026-09-11 (Brian): Builders write Behaviors.**
- `[ASSUMPTION]` `async` handlers, because ADR-0005's premise is that a Behavior may take a long time.
- `[ASSUMPTION]` Python 3.12, `asyncio`, `grpclib`-free — the generated Connect client from
  `gen/python`.
- `[ASSUMPTION]` Import denial of `andara_sdk.unsafe` and sockets is a loader check, not a sandbox;
  ADR-0005 says the sandbox is the process and the network policy.
