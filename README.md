# Andara's World

An online multiplayer role-playing game in the MUD tradition — text-first world simulation, deep
systems, a persistent world — with a modern rendered client to follow.

The world simulation is **server-authoritative**. The client renders and sends intent; it holds no
authority over game state, ever.

## Getting started

```
make bootstrap && make up && make check
```

That is the entire onboarding path. If it fails, that is a bug in `AW-INF-001` or `AW-INF-002`, not
a gap in your setup.

`make help` lists every target. Every workflow in this repo is a target — a documented sequence of
shell commands that is not a target is a defect.

## Where things are

| Path | What |
|------|------|
| `docs/roadmap.md` | Phase → epic → milestone map. The planning source of truth. |
| `docs/glossary.md` | Domain vocabulary. Every term used in a story lives here. |
| `docs/adr/` | Architecture decisions. Never deleted; superseded ones are marked. |
| `docs/epics/` | Themed groupings of stories. |
| `docs/stories/` | The unit of work. Status lives in frontmatter; files never move. |
| `docs/specs/` | Protocol, schema, and SLO definitions. |
| `docs/runbooks/` | On-call procedures. Every alert has one. |
| `BACKLOG.md` | **Generated.** Run `make backlog`; never hand-edit. |
| `server/` | `andara-server` — simulation, gateway, projections (Go). `server/sim` is dependency-free. |
| `admin/` | `andara-cli` — operator, builder, **and player** tooling (Go). |
| `agents/` | Python Behavior Agent SDK and runtime — NPC logic, outside the tick. |
| `client/` | `andara-client` — Phase 2, deferred (TypeScript, WebGL). |
| `gen/` | Generated protobuf code. Committed; regenerate with `make proto`. |
| `deploy/` | Compose, Kafka topic definitions, Helm, Kubernetes. |
| `LICENSING.md` | Which license covers which path, and the Open Canon split for the world itself. |
| `CONTRIBUTING.md` | What to contribute here, what not to, and what `make check` enforces. |

## Who writes what

Work splits into two lanes: *architecture* is what runs **around** the game — specifications,
stories, ADRs, infrastructure, CI, runbooks — and *implementation* is what runs **in** it, the Go
and TypeScript application source and its tests. Claude Code works both, in that order. Every
story declares its lane; `docs/status.md` reports the two separately. See `CLAUDE.md` §2.

## Architecture

Kafka is the ordering authority. The Gateway parses and authorizes an Intent and produces a Command to its
Zone's partition; the simulation consumes its partitions at 10 Hz, validates and applies in offset order,
and emits Events. The event log folds into a compacted current-state topic, and the indexes are built from
*that* — Redis for hot reads, Postgres for tabular — so rebuilding one costs live-state time rather than
world-age time. Indexes serve tooling and out-of-session queries, never the in-game read path. Clients,
`andara-cli`, and Python Behavior Agents running inside the server realm all speak the same gRPC service.
Content is authored outside this repository and published to compacted topics. Sharding, when it is
needed, is a consumer-group rebalance.

Nothing acknowledged to a player is ever lost: a Command is durable on two brokers before the ack, and all
in-memory state is derived from the log.

The decisions behind that are in `docs/adr/` — start with `ADR-0002` — and the numbers they imply are in
`docs/specs/slo/`.

## Current state

Phase 1, milestone M0. The planning skeleton and automation surface exist; no game code has been written.
ADR-0001 through ADR-0007 are accepted and ADR-0008 (tick rate) is proposed, so nothing in the backlog is
ADR-blocked.

`BACKLOG.md` shows what is ready. The M1 stories — protobuf schema, Kafka topics, simulation core, command
pipeline, gRPC gateway, `andara-cli play` — are groomed to `ready`. M2 through M4 stories are `draft`:
scoped, sequenced, and dependency-linked, with interface contracts written when their milestone
approaches rather than guessed at now.

## License

The code is [Apache-2.0](LICENSE); the documentation under `docs/` is
[CC BY-SA 4.0](LICENSES/CC-BY-SA-4.0.txt). Andara's World itself — the name, the world and
its setting, and its canon — is a creative work of [solo7.media](https://solo7.media) under
the Open Canon split: world and setting CC BY-SA 4.0, canon works CC BY-NC-ND 4.0. The full
map, with the reasoning, is in [`LICENSING.md`](LICENSING.md).
