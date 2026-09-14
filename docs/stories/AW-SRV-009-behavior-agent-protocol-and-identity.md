---
id: AW-SRV-009
title: Behavior agent protocol, identity, and runtime boundary
epic: EPIC-09
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-008, AW-SRV-011, AW-SRV-013, AW-SRV-022]
blocks: [AW-SRV-016]
lane: implementation
risk: high
---

## Context

ADR-0005 decided that Python Behaviors run **out of process** as Behavior Agents: an Agent subscribes to
perception-scoped Events, decides in Python taking as long as it needs, and submits Commands through the
same gRPC service a player's client uses. The simulation never executes Python.

**Decided 2026-09-11 (Brian): Builders write Behaviors.** That fixes the deployment unit: Behavior code
is content, published in a pack (`AW-SRV-013`) as `text/x-python` blobs, and **one Agent deployment runs
one pack's Behaviors**. Its identity is scoped to that pack (`Account.agent_pack_id`, `AW-SRV-008`), so a
Builder's Python can drive only NPCs whose Template comes from that Builder's pack, and a compromised or
hostile Behavior degrades its own pack's NPCs and nothing else. The process boundary and the network
policy contain what it can reach; the pack scope contains what it can be.

This story builds the server side of that boundary. `AW-SRV-016` builds the runtime and SDK on top of it.

## User story

As a builder, I want NPCs to act on their own without any one of them being able to stop the world, so
that I can write behavior without being able to take the server down.

## Scope

### In scope
- Agent identity: `agent` Accounts with `WORKLOAD_JWT` credentials (projected Kubernetes service-account
  tokens validated against the cluster issuer) in the cluster, `API_KEY` locally; both from `AW-SRV-008`.
- NPC claim protocol: `ClaimEntities` / `ReleaseEntities` / lease heartbeat, one lease per NPC, one
  Session driving many NPCs, no double ownership.
- Multi-NPC subscription: one `Subscribe` stream per Agent Session carrying the union of its NPCs'
  perception, each envelope tagged with which NPCs perceived it.
- Agent-scoped `authorize`: an Agent may act only as an NPC it holds a lease on, and only if that NPC's
  Template is from its pack.
- Attendance as World state: `MarkAttendance` Commands and `NpcUnattended` / `NpcAttended` Events so
  players and replay see the same thing.
- NPC memory as World state: `andara.core.Memory` Component with bounded, typed slots.
- Agent rate limits (`ingress.agent_rate_limit`, reserved by `AW-SRV-010`).

### Out of scope
- The Python runtime and SDK — `AW-SRV-016`.
- Any Python inside `server/sim`, enforced by `depguard`.
- Reflex rules in the tick — decided 2026-09-07, not Phase 1.
- Agent `Deployment`, `NetworkPolicy`, resource limits — `AW-INF-003` and `AW-INF-006`; this story
  provides the test that proves the policy holds (AC-7).

## Acceptance criteria

1. **Given** an Agent that hangs for thirty seconds **when** the World keeps ticking **then** tick
   duration is unaffected; when its lease expires (`agent.lease_ttl`) a `MarkAttendance{attended=false}`
   is produced for each NPC and `NpcUnattended` is emitted with Room scope.
2. **Given** the same period **when** it is replayed from a snapshot **then** the World reproduces
   exactly, because replay replays the Commands the Agent submitted and never the Python.
3. **Given** an Agent holding NPC `A` **when** an Event occurs that `A` could not perceive **then** the
   Agent's stream does not carry it; **given** NPCs `A` and `B` in different Rooms **then** an Event in
   `A`'s Room arrives once with `perceived_by=[A]`.
4. **Given** an Agent that dies **when** its lease expires **then** its NPCs remain, unattended, and a
   second Agent of the same pack claims them; **given** two Agents claiming the same NPC concurrently
   **then** exactly one succeeds and the other gets `ALREADY_EXISTS` naming the holder.
5. **Given** an Agent over `ingress.agent_rate_limit` **when** it submits **then** excess Commands are
   `RESOURCE_EXHAUSTED` and the Session survives.
6. **Given** an NPC with `andara.core.Memory` slots set by its Behavior via `SetMemory` **when** the
   World is recovered **then** the slots are restored, because they are Zone state.
7. **Given** a running Agent pod **when** `make agent-egress-test` runs from inside it **then** it can
   reach the gRPC endpoint and cannot reach Kafka, Redis, Postgres, MinIO, or the projector ports.
8. **Given** an Agent of pack `town` **when** it claims an NPC whose Template is from `andara.core` or
   another pack **then** `PERMISSION_DENIED` and an audit record.
9. **Given** an Agent holding no lease on NPC `A` **when** it submits a Command as `A` **then**
   `authorize` rejects it before the log and audits it.
10. **Given** `SetMemory` with a value over `agent.memory_slot_bytes` or a slot beyond
    `agent.memory_slots` **when** submitted **then** `INVALID_ARGUMENT` names the bound.

## Interface contract

```protobuf
// CONTRACT SKETCH — additions to andara/game/v1/game.proto
rpc ClaimEntities(ClaimEntitiesRequest) returns (ClaimEntitiesResponse);     // entity_ids → held[], refused{id, holder}[], lease_ttl
rpc ReleaseEntities(ReleaseEntitiesRequest) returns (ReleaseEntitiesResponse);
rpc Heartbeat(HeartbeatRequest) returns (HeartbeatResponse);                 // renews every lease on the Session
message SubscribeRequest { /* … */ repeated string entity_ids = 3; }          // Agent: union of held NPCs
message EventEnvelope   { /* … */ repeated string perceived_by = 4; }         // set only on Agent streams

// additions to andara/log/v1/log.proto LoggedCommand oneof
MarkAttendance mark_attendance = 17;   // entity_id, attended, agent_account_id
SetMemory      set_memory = 18;        // entity_id, slot (uint32), value (bytes, canonical)
Say            say = 19;               // actor_id, text — the first social verb, one Command for players and NPCs alike

// additions to andara/game/v1/event.proto payload oneof
NpcUnattended { string entity_id = 1; }  NpcAttended { string entity_id = 1; }
```

`andara.core.Memory` is a server-defined Component (ADR-0010 §7): `repeated MemorySlot slots` sorted by
`slot`, each `{uint32 slot; bytes value;}`, bounded by config. Behavior-local Python state is not this
and is lost on restart; the SDK makes the distinction explicit (`AW-SRV-016`).

### Lease state machine (Gateway)

```
unheld ─Claim─▶ held(agent, expires=now+ttl) ─Heartbeat─▶ held(…, renewed)
held ─Release─▶ unheld            held ─ttl elapses─▶ unheld + MarkAttendance{false}
```

Leases are Gateway memory (ADR-0001 single process); attendance is World state via the Command, so
what players see is in the log.

### Authorize (extends `AW-SRV-008`)

An `agent` Principal may submit a Command with `actor_id = E` iff `E` is held by this Session **and**
`Template(E).pack == Principal.AgentPackID` (`EntityState.template`, `AW-SRV-022`). Everything else is `ErrNotAuthorized`, audited.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `agent.lease_ttl` | `ANDARA_AGENT_LEASE_TTL` | `30s` | heartbeat every `ttl/3` |
| `agent.max_entities_per_session` | `ANDARA_AGENT_MAX_ENTITIES` | `500` | |
| `agent.memory_slots` | `ANDARA_AGENT_MEMORY_SLOTS` | `16` | per NPC |
| `agent.memory_slot_bytes` | `ANDARA_AGENT_MEMORY_SLOT_BYTES` | `1024` | |
| `auth.k8s_issuer` / `auth.k8s_jwks_url` | `ANDARA_AUTH_K8S_ISSUER` / `…_JWKS_URL` | — | `WORKLOAD_JWT` validation |
| `ingress.agent_rate_limit` | (exists) | `100/s` | |

### Error taxonomy

`ALREADY_EXISTS` (lease held), `PERMISSION_DENIED` (pack scope, no lease), `RESOURCE_EXHAUSTED` (rate,
entity cap), `INVALID_ARGUMENT` (memory bounds), `FAILED_PRECONDITION` (claim of a non-NPC).

## Data / state impact

`attended` and `Memory` are Zone state, hashed and snapshotted; `state_version` bumps by one with a
zero-fill migration. Memory must be expressible in bounded bytes — a real limit on what an LLM-driven
NPC retains, stated here rather than discovered.

## Observability requirements

### Metrics
- `andara_agents_connected{pack}` — gauge; pack count is bounded by content.
- `andara_npcs_attended` / `andara_npcs_unattended` — gauges.
- `andara_agent_commands_total{outcome}`, `andara_agent_claims_total{outcome}` — counters.
- `andara_agent_decision_latency_seconds` — histogram; from the Event's tick to the Command's
  `accepted_at`, computed at ingress from `client_ref` echo.
NPC instance ID is rejected as a label.

### Logs
- `info` on claim, release, lease expiry with `agent_account_id`, `pack`, `entity_id`, `session_id`.

### Traces
- An Agent's decision is its own trace rooted at the Agent, linked via `trace_id` on the Command.

### Alerts
- `NPCsUnattended` on `andara_npcs_unattended / (attended + unattended) > 0.2` for 5 m: the world's NPCs
  have stopped behaving. SLO `docs/specs/slo/npc-attendance.md` and runbook
  `docs/runbooks/npcs-unattended.md` ship here.

## Test plan

- **Unit:** lease state machine including expiry races; authorize matrix for agent scope; memory bounds.
- **Integration:** hang test (AC-1); replay determinism over a period with Agent activity (AC-2);
  perception scoping with two NPCs (AC-3); concurrent claim (AC-4); kill-and-recover with memory (AC-6);
  `make agent-egress-test` on kind (AC-7).
- **Manual/operator:**
  ```
  andara-cli agent status                    # expect: agents by pack, attended/unattended counts
  kubectl delete pod -l app=andara-agent-town
  # in play, same Room as a town NPC: expect "<npc> seems distracted" within 30 s, then recovery
  ```

## Definition of done

CLAUDE.md §8, plus: the replay-with-Agent test in CI; `agent-egress-test` in the kind CI job; the SLO
and runbook.

## Open questions

- **Carried from `AW-INF-004` on 2026-09-14:** registry compatibility is `BACKWARD` — new readers read
  old data, which replay requires. `FULL` would additionally forbid schema changes that break *old*
  readers of *new* data. That only matters once something older than the server reads the log, which
  is this story's Behavior Agents version-skewing from it. Decide here whether `andara.events.v1`'s
  subjects move to `FULL` when the first agent ships, or whether agents are held to the server's
  protobuf generation.

- **Resolved 2026-09-11 (Brian): Builders write Behaviors.** One Agent deployment per pack, identity
  scoped to the pack.
- `[NEEDS BRIAN]` What an unattended NPC looks like to players — the `NpcUnattended` Event exists; the
  wording and any idle animation are design. Inert by default.
- `[ASSUMPTION]` `WORKLOAD_JWT` in the cluster honors ADR-0005's "workload identity"; `API_KEY` exists
  for `make up` where there is no issuer.
- `[ASSUMPTION]` One Session drives many NPCs (up to 500); per-NPC Sessions would not scale.
