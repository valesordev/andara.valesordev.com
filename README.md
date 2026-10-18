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

## Cluster

`dev`, and `local` once AW-INF-060 lands, run on one kind cluster that belongs to this repo:
cluster `andara`, kubeconfig context `kind-andara`, defined by `deploy/kind/cluster.yaml` with the
node image pinned in `deploy/platform/versions.yaml` (ADR-0013). It has one control plane and three
workers.

```
make bootstrap                        # bin/kind and bin/kubectl, pinned and checksum-verified
make cluster-up                       # create it; a rerun only adds a missing kind-andara context
make cluster-down CONFIRM=andara      # delete it, and no other cluster
make cluster-rebuild CONFIRM=andara   # export the certificates, delete, create
```

The edge is published on loopback only: 80 as `127.0.0.1:8080` and 443 as `127.0.0.1:8443`. The
workstation's nginx holds :80 and :443. `cluster-up` refuses while anything else holds 8080 or 8443
(the compose stack's `ANDARA_HTTP_PORT` and `ANDARA_GRPC_PORT`); stop it with `make down` first.
`cluster-up` leaves your current kubectl context where it was, so pass `--context kind-andara`.

A rebuild deletes everything on the cluster. The two Secrets it keeps, `andara-dev/andara-edge-tls`
(the dev edge certificate) and `cert-manager/andara-ca-tls` (the private CA root), are exported to
`.local/tls/` first, mode 0600. That directory is gitignored, and `make clean` keeps it. A Secret
the cluster doesn't have leaves its earlier export in place. If an export fails, the rebuild stops
before deleting anything, and `.local/tls/` holds whatever the last good export wrote.
`scripts/cluster_secrets.py restore` puts them back. Until `platform-up` (AW-INF-054) runs it, a
rebuild ends by listing the steps it doesn't do yet.

With more than one kind cluster, `make kind-load` and `make kind-platform` need
`KIND_CLUSTER=<name>`.

## Where things are

| Path | What |
|------|------|
| `docs/roadmap.md` | Phase → epic → milestone map. The planning source of truth. |
| `docs/glossary.md` | Domain vocabulary. Every term used in a story lives here. |
| `docs/adr/` | Architecture decisions. Never deleted; superseded ones are marked. |
| Stories, epics, sprints | GitHub issues on the [Andara's World](https://github.com/orgs/valesordev/projects/2) Project. Status, lane, sprint, and order are Project fields. |
| `docs/sprints/` | Sprint close-outs and operator demos. |
| `docs/feedback/` | Archive of cross-role feedback from before stories moved to GitHub. |
| `docs/specs/` | Protocol, schema, and SLO definitions. |
| `docs/runbooks/` | On-call procedures. Every alert has one. |
| `server/` | `andara-server` — simulation, gateway, projections (Go). `server/sim` is dependency-free. |
| `admin/` | `andara-cli` — operator, builder, **and player** tooling (Go). |
| `agents/` | Python Behavior Agent SDK and runtime — NPC logic, outside the tick. |
| `client/` | `andara-client` — Phase 2, deferred (TypeScript, WebGL). |
| `gen/` | Generated protobuf code. Committed; regenerate with `make proto`. |
| `deploy/` | Compose, Kafka topic definitions, Helm, Kubernetes. |
| `LICENSING.md` | Which license covers which path, and the Open Canon split for the world itself. |
| `CONTRIBUTING.md` | What to contribute here, what not to, and what `make check` enforces. |

## Who writes what

Work splits into three lanes:
- *implementation* is what runs **in** the game: the Go and TypeScript application source and its
  tests.
- *architecture* is what runs **around** it and specifies it: ADRs, specs, contracts, and their
  review.
- *sre* is what runs around it and builds, ships, operates, or observes it: CI, deployment,
  dashboards, alerts, SLOs, and runbooks.

Each lane is its own Claude Code role, and project management plans the work without building
any. Every story declares its lane, and the Project's views show each lane separately. See
`CLAUDE.md` §2.

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

The Project shows what is ready. The M1 stories — protobuf schema, Kafka topics, simulation core, command
pipeline, gRPC gateway, `andara-cli play` — are groomed to `ready`. M2 through M4 stories are `draft`:
scoped, sequenced, and dependency-linked, with interface contracts written when their milestone
approaches rather than guessed at now.

## License

The code is [Apache-2.0](LICENSE); the documentation under `docs/` is
[CC BY-SA 4.0](LICENSES/CC-BY-SA-4.0.txt). Andara's World itself — the name, the world and
its setting, and its canon — is a creative work of [solo7.media](https://solo7.media) under
the Open Canon split: world and setting CC BY-SA 4.0, canon works CC BY-NC-ND 4.0. The full
map, with the reasoning, is in [`LICENSING.md`](LICENSING.md).
