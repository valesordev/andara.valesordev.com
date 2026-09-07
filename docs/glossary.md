# Glossary — Andara's World

Domain vocabulary. **Every term used in a story or spec must exist here.** If a story needs a
noun that is not in this file, the same pass that writes the story adds the entry.

Terms marked `[NEEDS BRIAN]` are lore- or design-bearing and are placeholders only. Claude Code
does not invent lore, mechanics, or names. These must be filled before any story that depends on
them reaches `ready`.

Terms are grouped by layer. Within a group they are alphabetical.

---

## Simulation

**Entity** — Anything the simulation tracks with identity and state: a Character, an NPC, an Item
instance, a Container. Entities have an `EntityID` (opaque, stable for the entity's lifetime).

**Event** — An immutable, past-tense fact emitted by the simulation after a Command is applied
(`RoomEntered`, `ItemPickedUp`, `CharacterDespawned`). Events are the only legitimate output of the
simulation core. Events are **derived**: replaying the Command log regenerates them identically, which
is why they need not be durable before a player is told the outcome (ADR-0002).

**Intent** — A player's expressed desire as received from a client, before parsing and authorization.
`move north` typed into a terminal is an Intent. Intents are untrusted input.

**Simulation Core** — The transport-agnostic, dependency-free package (`server/sim`) that owns World
state and advances it. No network, no datastore, no filesystem, no wall clock, no global randomness.
Deterministic given a starting state, a tick number, and an ordered input sequence. Enforced by
`depguard`, not by convention.

**State Hash** — A hash over complete World state at a Tick. The determinism assertion point, the
identity recovery verifies against, and a field on every Tick Boundary Record.

**Tick** — One discrete advancement of the world simulation. The unit of simulation time. Tick
numbers are monotonic and are the simulation's only notion of time.

**Tick Boundary Record** — A `TickCompleted` record on the Event topic carrying the tick number, the
per-partition Offset range the tick applied, the `state_version`, and the State Hash. Replay reads
tick boundaries from these records rather than re-deriving them, which is what makes replay exact
rather than approximately exact (ADR-0002 §4).

**Tick Budget** — The overrun threshold and the tick-health SLI boundary: **50 ms**, deliberately half
the 100 ms interval so that overruns appear as a warning band well before Simulation Lag accrues. The
budget is per-process and shared across every Partition the process owns.

**Tick Loop** — The process loop that consumes Commands from its assigned Partitions, advances the
simulation by one Tick, and emits Events. First-class source of SLIs from the first server story.

**Tick Overrun** — A Tick whose processing exceeded the Tick Budget. Counted; a cause, not a symptom,
and therefore never alerted on directly.

**Tick Rate** — Ticks per second the world targets. **10 Hz**, a 100 ms interval (ADR-0008). Periodic
mechanics — combat rounds, regeneration, spawns — are expressed as a *number of Ticks*, never as "every
tick", so game pacing and engine rate stay independently tunable.

**Simulation Lag** — Wall-clock time by which the Tick Loop trails its ideal schedule. The player-
visible symptom, and therefore the thing that alerts.

**World** — The complete set of Zones, Entities, and simulation state under a single authority.

---

## World content

**Behavior** — The logic that drives an NPC or a scripted object. Written in Python and executed by a
Behavior Agent, outside the tick (ADR-0005).

**Behavior Agent** — A Python process running **inside the server realm** that behaves as a client: it
subscribes to perception-scoped Events, decides, and submits Commands over the same gRPC service a
player's client uses. Agents may take arbitrarily long, call models, and be non-deterministic; the log
records what they decided, not how, so replay never re-runs Python. They authenticate with in-cluster
workload identity under the `agent` role, and network policy permits them to reach the gRPC endpoint and
nothing else — no Kafka, no datastore, no Projection (ADR-0005).

**Container** — An Entity that can hold Item instances. A backpack, a chest, a corpse.

**Direction** — The label on an Exit. `[NEEDS BRIAN]` — the canonical set. Treated as an opaque string
until answered, which means typos cannot yet be rejected at load.

**Exit** — A directed edge from one Room to another, labeled with a Direction. Exits are
one-directional in the data model; a two-way passage is two Exits. Exit conditions (doors, locks,
skill checks) are `[NEEDS BRIAN]`.

**Item** — A thing that can be held, worn, used, or stored. Distinguish **Item Definition** (the
authored template) from **Item Instance** (a specific Entity in the World with its own ID and state).

**NPC** — A non-player Character controlled by a Behavior Agent rather than by a player Session.

**Reflex Rule** — A declarative condition/effect rule that would be evaluated inside the tick for effects
needing to resolve in the tick that caused them. **Not built in Phase 1** (ADR-0005): reactive-one-tick-
late is accepted, which at 10 Hz costs 100 ms. Named here because the term appears in ADR-0005 as the
design that gets built if a mechanic ever genuinely requires same-tick reaction.

**Room** — The atomic unit of location. Every Entity with a position is in exactly one Room. Rooms
have a stable `RoomID`, a description, and zero or more Exits.

**Zone** — An authored, named collection of Rooms shipped and versioned as a unit. The unit of content
authorship, of content validation, and of simulation authority — a Zone maps to exactly one Partition
(ADR-0001).

**Zone Definition** — The authored source of a Zone: its Rooms, Exits, spawn rules, and content
references. Resolved from the content store at load and validated before use.

---

## Content pipeline

**Active Pointer** — The record on `andara.content.active.v1`, keyed by pack ID, naming the Content
Version currently live. The only mutable thing in the content store. Rollback is one record
(ADR-0004).

**Content Blob** — An immutable content body on `andara.content.blobs.v1`, keyed by its SHA-256. Keys
never repeat, so compaction never removes one.

**Content Pack** — A versioned bundle of Zone Definitions, Item Definitions, NPC Definitions, and
dialogue that the server loads as a set. Authored outside the repository by Builders and published
through `andara-cli` (ADR-0004).

**Content Version** — An immutable manifest on `andara.content.versions.v1`, keyed by `packID@version`,
naming its Content Blobs, its parent version, its author, and its timestamp. The linked history a
Builder can walk and diff.

---

## Players, accounts, sessions

**Account** — The credential-bearing identity a human authenticates as. Owns up to five Characters, of
which exactly one may be live at a time; switching is a Despawn followed by a spawn, not a second Session. Credentials are Argon2id-hashed. Account state lives on `andara.accounts.v1` and is
**not** World state — a World rollback must not roll back credentials (ADR-0006).

**Character** — A player-controlled Entity in the World. Persistent across Sessions. Names are globally
unique and immutable.

**Despawn** — Removal of a Character from the World without deletion. Its state is persisted; it leaves
its Room and stops being a target. What happens when a Linkdead grace period expires, and — because
`linkdead_max` is a hard ceiling — the mechanism by which a disconnected Character can survive a fight it
would otherwise have lost.

**Invite Code** — A single-use, expiring, revocable, Account-scoped token required to register while
`auth.registration_mode` is `invite`. Issuance and redemption are both audited.

**Linkdead** — A Session whose stream dropped while its Character remains in the World. The Character is
marked linkdead and held for `session.linkdead_grace` (180 s); reconnecting within that window rebinds. On
expiry the Character is Despawned. The grace period must exceed the RTO, or a routine restart despawns
every player.

**In combat, the timer extends but does not stop.** A linkdead Character stays attackable and takes
damage; each combat interaction refreshes its remaining grace to at least
`session.linkdead_combat_extension` (60 s), and `session.linkdead_max` (300 s) caps total linkdead
duration regardless. Dropping mid-fight therefore means being hurt, possibly killed, and possibly
surviving by despawning out of it (ADR-0006).

**Player** — A human playing the game. Used for the human, not the Entity. When the Entity is meant,
say Character.

**Registration Mode** — Server configuration gating account creation: `closed`, `invite`, or `open`.
Changeable without a deploy; every change is an audit Event (ADR-0006).

**Session** — A live, authenticated gRPC connection bound to at most one Character. Has a `SessionID`
used as the correlation ID on every log line, span, and Command in its path.

---

## Roles

Story `As a <role>` lines use exactly these. "User" is not a role.

**Builder** — Authors world content: Zones, Rooms, Items, NPCs, dialogue, and Behaviors. Works through
`andara-cli` and the content store, **without repository access** (ADR-0004). Untrusted by the system
in the security sense.

**Developer** — Writes and ships `andara-server`, `andara-cli`, and `andara-client` code.

**Game Master** — Live-world authority: moderates players, intervenes in the running World, inspects
and adjusts state at runtime. Acts *in* the game. GM powers are `[NEEDS BRIAN]`.

**Operator** — Runs the service: deploys, scales, observes, responds to alerts, performs recovery.
Acts *on* the service, through `andara-cli` and infrastructure tooling.

**Player** — See above.

---

## Command handling

**Command** — A parsed, typed, authorized instruction, durably ordered in the Command Log and applied
on a specific Tick. Contrast Intent (untrusted, unparsed).

**Command Pipeline** — The fixed stage sequence every Intent passes through:
`parse → authorize → [LOG] → validate → apply → emit events`. The Log boundary sits between
`authorize` and `validate`: parse and authorize need only Session and Account knowledge and run at the
Gateway before the produce; validate and apply need authoritative World state and run inside the tick
after the consume (ADR-0002 §2). Rejection at any stage produces a typed error, not a silent drop.

**Command Verb** — The first token of an Intent, resolved against the verb table to a Command type.

---

## Transport and protocol

**Connect** — A gRPC-compatible protocol that speaks to browsers natively over HTTP/1.1 and HTTP/2
without a proxy. Served from the same handler as gRPC and gRPC-Web so Phase 2 inherits a browser path
for free (ADR-0003).

**Gateway** — The server component that terminates gRPC connections, authenticates Sessions, parses
and authorizes Intents, produces Commands to the log, and streams Events out. The Gateway knows about
connections; the Simulation Core does not.

**Protocol** — The versioned gRPC contract between clients and server, defined in protobuf. Two
services on one endpoint: `andara.game.v1.Game` and `andara.admin.v1.Admin`. Must be frozen at v1
before Phase 2 begins.

**Protocol Version** — An integer negotiated at Session establishment. The server declares a supported
range; a client outside it is rejected with a typed error, never silently degraded.

**Schema Registry** — The service fronting Kafka topics that validates producer schemas at publish
time. Necessary because Builders author content outside the repository, so the repository cannot be the
only place a schema is checked (ADR-0007).

**Text Interface** — The Phase 1 player surface: `andara-cli play`, a first-class gRPC client that
renders Events as prose in a terminal. Not telnet — a human cannot telnet into a gRPC server
(ADR-0003).

---

## Data layer

**Command Log** — `andara.commands.v1`, the Kafka topic that is the World's authority on order. Keyed
by `ZoneID`, 64 Partitions. Per-partition offset order *is* the order things happened.

**Compacted Topic** — A Kafka topic where only the latest record per key is retained. Used for the
Active Pointer, Account state, and — because their keys never repeat — for Content Blobs and Content
Versions, where compaction retains everything.

**Event Topic** — `andara.events.v1`, carrying derived Events and Tick Boundary Records. Feeds the State
Projector and audit. Retention must reach back at least as far as the oldest Snapshot.

**State Topic** — `andara.state.v1`, compacted, holding the current state of each aggregate keyed
`character:<id>`, `npc:<id>`, `item:<id>`, `room:<zone>/<id>`. What the indexes read, so a rebuild costs
time proportional to live state rather than to the World's age. Each record carries `tick`,
`source_offset`, `source_event_id`, `content_version_sha256`, and `state_digest` (ADR-0002 §5.5).

**State Projector** — The component that folds the Event Topic into the State Topic. It embeds the
Simulation Core in fold-only mode rather than reimplementing the fold, so there is one implementation of
how an Event changes state — and so its accumulated hash can be asserted against the State Hash in the
corresponding Tick Boundary Record. Divergence is an alert, not a discovery.

**Offset** — A record's position within a Partition. The unit of progress; checkpointed with the state
it produced.

**Partition** — A Kafka partition, and therefore the unit of simulation authority. A Zone maps to one
Partition by `hash(ZoneID) % 64`; a process owns a set of Partitions. Partition count is fixed at
creation and is the maximum future Shard count (ADR-0002 §6).

**Projection** — A read-optimized, non-authoritative index built by consuming the **State Topic**. Redis
for hot per-Character and per-Room reads, Postgres for tabular and admin queries, ClickHouse later for
analytics. Serves tooling, out-of-session queries, and — once sharded — cross-Shard reads. **Never serves
the in-game read path**, which reads in-memory state, and is never written to directly.

**Recovery Point Objective (RPO)** — Maximum acceptable World state loss after an unplanned restart.
**Zero acknowledged actions.** A Command is durable on at least two brokers before the player is acked,
and in-memory state is entirely derived from the log, so nothing acknowledged can be lost. The claim
depends on `acks=all`, `min.insync.replicas=2`, **and** `unclean.leader.election.enable=false` together
(`docs/specs/slo/recovery.md`).

**Recovery Time Objective (RTO)** — Maximum acceptable time from process death to a playable World.
**120 s p99 at M2, 60 s p99 at Phase 1 exit.** Dominated by Kubernetes failure detection and pod startup
rather than by replay. Must always stay below the Linkdead grace period, or a routine restart despawns
every player.

**Shard** — A process consuming a subset of Partitions. At launch there is one, owning all 64. Sharding
is a consumer-group rebalance, not a migration, and there is no handoff protocol because there is no
state transfer (ADR-0001).

**Snapshot** — A complete serialization of a Zone's state at a Tick, keyed to the Offsets that produced
it. Stored in object storage; its manifest is recorded on the Event Topic so recovery finds it by
reading the log it already reads. The replay origin.

**Write-Ahead Log (WAL)** — Here, the Command Log. Commands are durable before they are applied, by
construction: the tick only ever sees records it consumed.

---

## Engineering process

**ADR** — Architecture Decision Record. `docs/adr/ADR-NNNN-<slug>.md`. Never deleted; superseded ADRs
are marked, not removed.

**Epic** — A themed grouping of Stories that delivers a coherent capability. `EPIC-NN`.

**Story** — The unit of work handed to Cursor. `AW-<COMP>-<NNN>`. Format in CLAUDE.md §5.

**Milestone** — A demonstrable state of the product on the roadmap. Defined by what a human can
observably do, not by which stories are closed.
