# Roadmap — Andara's World

Planning source of truth. Phase → Epic → Milestone. Story status lives in story frontmatter;
`BACKLOG.md` is the generated view. This file changes when scope or sequencing changes, not when a
story closes.

**All open architecture decisions were made on 2026-09-07.** ADR-0001 through ADR-0007 are `accepted`;
ADR-0008 (tick rate) is `proposed` pending Brian's confirmation of a recommendation he asked for. Nothing
in the backlog is ADR-blocked. What follows reflects the architecture those decisions
describe, which is meaningfully larger in Phase 1 than the drafted alternative — see "What the
decisions cost" below.

---

## Phase gate (non-negotiable)

**Phase 2 does not start until Phase 1 exit criteria are met.** See CLAUDE.md §1. The client carries
the art-content cost; building it against an unstable protocol pays that cost twice.

### Phase 1 exit criteria

1. A human runs `andara-cli play`, creates a Character, moves through a multi-Zone World, interacts
   with Items and NPCs, disconnects, reconnects within the linkdead grace period, and finds the World
   as they left it.
2. The Protocol is frozen at v1 in `docs/specs/protocol/`.
3. The server survives a deliberate process kill and returns to a playable World within the stated
   RTO, losing no more than the stated RPO, with a matching State Hash.
4. A Builder publishes a Content Pack version and rolls it back, without repository access and without
   a deploy.
5. An Operator can run the full lifecycle — deploy, inspect, intervene, roll back — through
   `andara-cli`, with no direct datastore or Kafka access.
6. Tick duration, tick overrun, and simulation lag SLOs exist, are measured, and have runbooks.

Until all six hold, `CLT` stories stay in `draft`.

---

## Architecture in one paragraph

Kafka is the ordering authority. The Gateway parses and authorizes an Intent and produces a Command to
the Zone's Partition; the simulation consumes its Partitions at 10 Hz, validates and applies in offset
order, and emits Events. The Event log folds into a compacted current-state topic, and the indexes are
built from *that* — Redis for hot reads, Postgres for tabular, ClickHouse later — so rebuilding one costs
live-state time rather than world-age time. Indexes serve tooling and out-of-session queries, never the
in-game read path. Clients, `andara-cli`, and Python Behavior Agents running inside the server realm all
speak the same gRPC service. Content is authored outside the repository and published to compacted
topics. Sharding, when needed, is a consumer-group rebalance.

Nothing acknowledged to a player is ever lost: a Command is durable on two brokers before the ack, and
all in-memory state is derived from the log.

---

## Phase 1 — Playable world over the text interface

### Epics

| Epic | Title | Component | Milestone | Constrained by |
|------|-------|-----------|-----------|----------------|
| `EPIC-01` | Foundations and delivery platform | `INF` | M0 | — |
| `EPIC-10` | Data layer — Kafka, schema registry, projections | `INF` + `SRV` | M1–M3 | ADR-0002, ADR-0007 |
| `EPIC-02` | World model and simulation core | `SRV` | M1 | ADR-0001, ADR-0002 |
| `EPIC-03` | Command pipeline and gRPC gateway | `SRV` | M1 | ADR-0003, ADR-0007 |
| `EPIC-07` | Observability, SLOs, and runbooks | `INF` | M1–M4 | — |
| `EPIC-04` | Snapshots and recovery | `SRV` | M2 | ADR-0002 |
| `EPIC-08` | Identity, accounts, and sessions | `SRV` | M2 | ADR-0006 |
| `EPIC-05` | Content pipeline and zone authoring | `SRV` + `CLI` | M3 | ADR-0004, ADR-0007 |
| `EPIC-06` | Operator and builder CLI | `CLI` | M3 | ADR-0003, ADR-0004 |
| `EPIC-09` | Behavior agents and Python SDK | `SRV` | M4 | ADR-0005 |

`EPIC-10` is new and sits early, because the log is now in the critical path of the very first
playable tick rather than being a durability concern added at M2.

### Milestones

Each milestone is defined by an observable capability, not by a story count.

#### M0 — Scaffold *(gate: `make bootstrap && make up && make check` on a clean machine)*
Repo scaffold, Makefile, CI, local stack, story tooling. No game code.
Epics: `EPIC-01`.

#### M1 — Walking skeleton *(gate: a human runs `andara-cli play`, sees a room, types `north`, sees a different room — with the Command having gone through Kafka)*
Protobuf schema and generated code. Redpanda locally with real topics. Simulation core with a Room
graph and a deterministic Tick Loop consuming its Partitions. Command Pipeline split across the log
boundary. gRPC Gateway with a streaming Event subscription. `andara-cli play`. Tick SLIs from day one.
State is not yet snapshotted — a restart replays the whole log, deliberately, until M2.
Epics: `EPIC-10` (first slice), `EPIC-02`, `EPIC-03`, `EPIC-07` (first slice).

#### M2 — Persistent world *(gate: `kill -9` the server; the World returns within 120 s with a matching State Hash, and every linkdead Character rebinds rather than despawning)*
Zone Snapshots every 60 s keyed to offsets, recovery from snapshot plus log tail, exercised in CI.
Accounts, Character rosters, Session lifecycle with the 180 s linkdead grace. The state projector and its
compacted topic, then the Redis index on top of it.
Epics: `EPIC-04`, `EPIC-08`, `EPIC-10` (state projector, Redis).

#### M3 — Authored world *(gate: a Builder with no repository access publishes a Zone, sees it live, and rolls it back)*
Content blobs, version manifests, active pointer. Publish-time validation and authorization. Content
reload at a tick boundary. `andara-cli content` commands and a human-authorable surface. Postgres
projection for rosters and Builder queries.
Epics: `EPIC-05`, `EPIC-06`, `EPIC-10` (Postgres).

#### M4 — Playable vertical slice *(gate: Phase 1 exit criteria 1–6 all hold)*
Python Behavior Agents driving NPCs. Items. One complete gameplay loop `[NEEDS BRIAN — which loop:
combat, trade, exploration, social?]`. SLOs with error budgets and runbooks. Protocol frozen at v1.
Epics: `EPIC-09`, remainder of `EPIC-07`.

### Sequencing rationale

- `EPIC-10` before `EPIC-02` finishes: the Tick Loop's input is a Partition consumer, so the log has
  to exist before the loop is real. Building the loop against an in-memory queue and swapping Kafka in
  later would mean M1 proves less than it appears to — the ordering, offset, and rebalance behavior
  that the whole architecture rests on would be untested.
- `EPIC-03` at M1 rather than after: the Command Pipeline is now split across the log boundary
  (parse and authorize at the Gateway, validate and apply in the tick), so the Gateway is not an
  add-on to the pipeline, it is half of it.
- `EPIC-04` after `EPIC-02`: snapshots checkpoint state that must exist first. Note that recovery
  *works* from M1 — replaying the log from the beginning is correct, just slow. Snapshots are an RTO
  optimization, which is a healthier way to build them than as a correctness mechanism.
- `EPIC-07` starts with `EPIC-02`, not after. Retrofitted tick SLIs are tick SLIs that never land.
- `EPIC-09` last: Behavior Agents are gRPC clients, so they need the Gateway, the Event stream, and
  Agent identity — all of which arrive earlier.

### What the decisions cost

Stated plainly, because it changes Phase 1's shape:

- **Kafka is in the critical path from M1.** A local stack now runs Redpanda; production runs a real
  cluster with a schema registry. Kafka availability bounds World availability, and that needs an SLO and
  a documented read-only degradation mode before the first deploy.
- **Deploys spend the write-availability budget.** ~40 per 28 days at a 99.9% target, which is what
  decides whether sharding is activated before or after launch.
- **The 64-partition choice is effectively permanent.** Repartitioning a keyed topic reorders history.
- **gRPC costs us telnet.** Onboarding a playtester means shipping a binary. Acceptable for a closed
  launch; revisit at the open-registration transition.
- **Protobuf everywhere means a codegen step and committed generated code**, and it means Builders
  author against a format that needs a human-friendly surface built on top of it.
- **Behavior Agents are a fleet to operate**, not a library to import.

Against that: entity handoff and cross-shard consistency — the two hardest problems in the drafted
sharded design — do not exist in this one. That is a good trade.

---

## Phase 2 — Rendered client (deferred)

Not started. Gated on Phase 1 exit criteria. Placeholder epics only; no stories, no estimates.

| Epic | Title | Component |
|------|-------|-----------|
| `EPIC-20` | Client bootstrap and Connect protocol client | `CLT` |
| `EPIC-21` | Art pipeline and asset delivery | `CLT` |
| `EPIC-22` | Rendered world presentation | `CLT` |

ADR-0003 serves Connect alongside gRPC, so the browser path needs no proxy. ADR-0004 flags that large
art binaries do not belong in Kafka — `EPIC-21` will need object storage with hashes in the manifest.

---

## Decision record

| ADR | Subject | Status | Decision |
|-----|---------|--------|----------|
| `ADR-0001` | Sharding model | accepted | One process owning all 64 partitions; sharding is a rebalance |
| `ADR-0002` | World state persistence | accepted | Kafka WAL, split read/write, projected read layers |
| `ADR-0003` | Transport and protocol | accepted | gRPC over HTTP/2 + TLS; Connect for browsers; `andara-cli play` |
| `ADR-0004` | Content pipeline | accepted | Authored outside the repo; blobs, versions, active pointer |
| `ADR-0005` | Behavior layer | accepted | Python Behavior Agents outside the tick |
| `ADR-0006` | Identity and accounts | accepted | Closed → invite → open; 5 characters, 1 live; 180 s linkdead grace, extended by combat to a 300 s ceiling |
| `ADR-0007` | Schema authority | accepted | Protobuf for wire, log, snapshot, and content |
| `ADR-0008` | Tick rate | **proposed** | 10 Hz, 100 ms interval, 50 ms budget; mechanics measured in Ticks |

### Service level targets

Proposed on 2026-09-07, reasoned from the architecture, pending validation against first measurement.
They constrain each other; `docs/specs/slo/README.md` lists the interlocks.

| Target | Value | Document |
|--------|-------|----------|
| Tick budget adherence | 99.9% within 50 ms, 28 d | `slo/tick-health.md` |
| Simulation lag | 99.9% under 500 ms, 28 d | `slo/tick-health.md` |
| **RPO** | **zero acknowledged actions lost** | `slo/recovery.md` |
| **RTO** | 120 s p99 at M2 → 60 s p99 at Phase 1 exit | `slo/recovery.md` |
| Snapshot cadence | 60 s per Zone | `slo/recovery.md` |
| World write availability | 99.5% pre-launch → 99.9% at launch | `slo/world-write-availability.md` |

The consequence most worth arguing about: at a 60 s RTO, a 99.9% write-availability budget allows roughly
**40 deploys per 28 days**, because until sharding is active a deploy is a full-World restart. Deploy
cadence — not load — is therefore the most likely thing to make ADR-0001's sharding urgent.

### Remaining open questions

Design questions that gate specific stories rather than architecture, tracked as `[NEEDS BRIAN]` in the
stories and glossary. The largest remaining:

- **What players see during a deploy or recovery interruption** (`AW-INF-007`, `slo/recovery.md`).
- **The content authoring format** — protobuf is not hand-authorable, and this decides how pleasant
  world-building feels (`AW-CLI-003`).
- **The canonical Direction set**, without which the loader cannot reject `norht` as a typo
  (`AW-SRV-001`).
- **Which gameplay loop M4 delivers** — combat, trade, exploration, or social.

---

## Explicitly out of scope for Phase 1

Named here so they stop reappearing in grooming:

- Rendered client, art pipeline, asset CDN.
- Multi-process sharding. The architecture supports it; activating it before there is a measurement
  that demands it is premature.
- ClickHouse. Added when there is an analytical query Postgres handles badly.
- Cross-region deployment, multi-cluster, disaster recovery beyond single-cluster restore.
- Email-based account flows, including password reset. Gates the open-registration transition.
- In-game building. `[NEEDS BRIAN]` on whether it is a design goal.
- Anti-cheat beyond what server authority provides for free.
