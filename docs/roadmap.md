# Roadmap — Andara's World

Planning source of truth. Phase → Epic → Milestone. Story status lives in story frontmatter;
`BACKLOG.md` is the generated view. This file changes when scope or sequencing changes, not when a
story closes.

ADR-0001 through ADR-0010 are all `accepted` (ADR-0008 and ADR-0010 on 2026-09-10). Nothing in Phase 1
is ADR-blocked, and as of 2026-09-11 every Phase 1 story is `ready` or beyond. What follows reflects the
architecture those decisions describe, which is meaningfully larger in Phase 1 than the drafted
alternative — see "What the decisions cost" below.

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
   a deploy. **As of 2026-09-07 activation requires a second approver**, so demonstrating this
   criterion needs two identities — one publishing, one approving. That is a change to how the
   criterion is exercised, not a weakening of it; it is called out here because a single-person
   rehearsal will now fail, and it should.
5. An Operator can run the full lifecycle — deploy, inspect, intervene, roll back — through
   `andara-cli`, with no direct datastore or Kafka access.
6. Tick duration, tick overrun, and simulation lag SLOs exist, are measured, and have runbooks.

Until all six hold, `CLT` stories stay in `draft`.

---

## Architecture in one paragraph

Kafka is the ordering authority. The Gateway parses and authorizes an Intent and produces a Command to
the Zone's Partition; the simulation consumes its Partitions at 10 Hz, validates and applies in offset
order, and emits Events. A replica of the simulation consumes the same log and writes a compacted current-state topic, and
the indexes are built from *that* — Redis for hot reads, Postgres for tabular, ClickHouse later — so rebuilding one costs
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
Content blobs, version manifests, active pointer. Publish-time validation, per-pack Builder
authorization, and second-approver activation. Content reload at a tick boundary. The Content Language
specification (`AW-CLI-005`), its compiler (`AW-CLI-006`), and the `andara-cli content` commands
(`AW-CLI-002`, `AW-CLI-003`). Postgres projection for rosters and Builder queries.
Epics: `EPIC-05`, `EPIC-06`, `EPIC-10` (Postgres).

#### M4 — Playable vertical slice *(gate: Phase 1 exit criteria 1–6 all hold)*
Python Behavior Agents driving NPCs — **written by Builders** (decided 2026-09-11), so Behavior code
is pack content and one Agent deployment runs one pack. Items. One complete gameplay loop `[NEEDS
BRIAN — which loop: combat, trade, exploration, social?]`. SLOs with error budgets and runbooks. Protocol
frozen at v1.
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
| `EPIC-11` | In-game building for admins and builders | `SRV` |
| `EPIC-20` | Client bootstrap and Connect protocol client | `CLT` |
| `EPIC-21` | Art pipeline and asset delivery | `CLT` |
| `EPIC-22` | Rendered world presentation | `CLT` |

`EPIC-11` is not a client epic and does not wait on the Phase 2 gate for a technical reason — it waits
because Phase 1 has no capacity for it. It exists now so that Phase 1 stories touching the content
path can check whether they are foreclosing it.

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
| `ADR-0008` | Tick rate | accepted | 10 Hz, 100 ms interval, 50 ms budget; mechanics measured in Ticks |
| `ADR-0009` | Content authoring language | accepted | A purpose-built text language compiling to canonical protobuf |
| `ADR-0010` | Game Object type system | accepted | Templates in single inheritance containing Components; server-defined component types; Rooms and Zones carry Components |

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

None of these affect an interface contract; every story carrying one is `ready` with the mechanism in
place and the words or values left to Brian.

- **What players see during a deploy or recovery interruption** — the `ServerStopping` Event and its
  lead time exist (`AW-INF-007`); the message does not.
- **What players see on relocation** when a Room is removed by a content change (`AW-SRV-012`).
- **Which gameplay loop M4 delivers** — combat, trade, exploration, or social.
- **The spawn Room** for new Characters (`AW-SRV-014`, one values-file line) and what a Character *is*
  beyond name and position.
- **What an unattended NPC looks like** to players (`AW-SRV-009`).
- **The Content Language syntax review** (`AW-CLI-005` AC-10) — Brian reads `town.aw` as the Builder
  in the room.
- **The wording of the read-only error** every player will eventually see (`AW-SRV-010`).

### Resolved on 2026-09-11

- **Hash mismatch on recovery: refuse to start.** No automatic search for an older round; the operator
  picks one with `andara-cli snapshot list` and `recover --verify --round` (`AW-SRV-007`).
- **Builder authority is scoped per pack** (`Account.builder_packs`, `AW-SRV-013`).
- **Linkdead is visible to other players** — a Room-scoped Event and a `look` marker (`AW-SRV-015`).
- **Builders write Behaviors.** Behavior code is pack content; one Agent deployment per pack with an
  identity scoped to that pack (`AW-SRV-008`, `AW-SRV-009`, `AW-SRV-016`).

### Resolved on 2026-09-10

- **Tick rate** — ADR-0008 accepted at 10 Hz / 50 ms budget.
- **Game Object type system** — ADR-0010 accepted; component types are server-defined; Rooms and
  Zones carry Components (`AW-SRV-021`).
- **The canonical Direction set** — closed, the classic twelve with reverses.
- **Platform** — kind v1.36.1 on Brian's box; `Admin` on the same listener, restricted by network.

### Resolved on 2026-09-07

- **The content authoring format** — a purpose-built text language, ADR-0009.
- **Whether the Text Interface is permanent** — it is, and it is an Operator and Developer tool that a
  human can also play through. `andara-cli` never renders anything (ADR-0003, `AW-CLI-004`).
- **Whether Builders have repository access** — none do; server changes go through GitHub issues.
- **Whether activation needs a second approver** — it does (`AW-SRV-013`).
- **Whether `andara-cli` stores credentials** — it does, and acting as another identity always records
  who was really acting (`AW-CLI-001`, `AW-SRV-008`).
- **Whether in-game building is a design goal** — it is, for Admins and Builders, not players.
  `EPIC-11`, not Phase 1.

---

## Explicitly out of scope for Phase 1

Named here so they stop reappearing in grooming:

- Rendered client, art pipeline, asset CDN.
- Multi-process sharding. The architecture supports it; activating it before there is a measurement
  that demands it is premature.
- ClickHouse. Added when there is an analytical query Postgres handles badly.
- Cross-region deployment, multi-cluster, disaster recovery beyond single-cluster restore.
- Email-based account flows, including password reset. Gates the open-registration transition.
- In-game building. Confirmed a design goal on 2026-09-07 and given `EPIC-11`, but for Admins and
  Builders only, and not in Phase 1.
- Anti-cheat beyond what server authority provides for free.
