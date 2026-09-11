---
id: AW-INF-002
title: Local stack — Redpanda, datastores, observability, and TLS with one command
epic: EPIC-01
component: infra
type: infra
status: review
size: M
depends_on: [AW-INF-001, AW-INF-004]
blocks: [AW-INF-003, AW-SRV-002]
lane: architecture
risk: medium
---

## Context

CLAUDE.md §8 requires instrumentation to be verified against a real backend, not merely registered.
ADR-0002 raises the stakes: Kafka is now the ordering authority and sits in the critical path of every
Command, so "works locally" has to mean "works against a real log with real offsets and a real
rebalance," not against an in-memory stand-in.

ADR-0003 adds a second reason the local stack has to be honest. TLS is mandatory on the wire; if
`make up` does not provision certificates, developers learn to pass an insecure flag and eventually
ship it.

## User story

As a developer, I want `make up` to give me a running server against a real broker, real projections,
a real trace backend, and real TLS, so that what passes locally is what passes in production.

## Scope

### In scope
- Compose definition bringing up: `andara-server`, **Redpanda**, **Redis**, **Postgres**, a schema
  registry, an OTLP collector, a metrics store, a trace backend, and a dashboard UI.
- Topic creation on startup by invoking the topic-as-code definitions from `AW-INF-004`, so local and
  production topics come from one source.
- Local CA and server certificate provisioning, plus a generated `andara-cli` config naming the
  endpoint and the CA, so the CLI trusts the stack without a per-invocation flag.
- `make up`, `make down`, `make logs`, `make ps`.
- A pre-provisioned dashboard showing the tick SLIs from `AW-SRV-002` and consumer lag from
  `AW-INF-004`.
- Health checks on every service, so `make up` returns only when the stack is usable.

### Out of scope
- Kubernetes and Helm — `AW-INF-003`.
- Production Kafka topology, replication, and tiered storage — `AW-INF-005`.
- ClickHouse. Deferred per ADR-0002 until there is an analytical query Postgres handles badly.
- Seeded world content — `AW-SRV-012`.

## Acceptance criteria

1. **Given** a clean machine with a container runtime and a completed `make bootstrap` **when** a
   developer runs `make up` **then** the command exits 0 only after every service reports healthy, and
   prints the URL of each exposed UI.
2. **Given** a running stack **when** `make up` is run again **then** it exits 0 without recreating
   healthy services and without data loss.
3. **Given** a running stack **when** `make down` runs **then** every service stops and it exits 0;
   **and given** an already-stopped stack **then** `make down` still exits 0.
4. **Given** `make up` completed **when** the topics are listed **then** every topic named in
   `AW-INF-004` exists with the declared partition count and cleanup policy — specifically
   `andara.commands.v1` with 64 partitions and the three content topics with `cleanup.policy=compact`.
5. **Given** a running stack and `ANDARA_CONFIG` pointing at the config `make up` generated **when**
   `andara-cli play` connects **then** it does so over TLS against the locally provisioned CA, with no
   insecure flag and no certificate warning. `AW-CLI-001` AC-10 makes the insecure flag impossible to
   pass rather than merely discouraged.
6. **Given** a running stack **when** a developer issues one command **then** a trace for it is
   retrievable by Session correlation ID with spans for `command.execute`, the Kafka produce, and the
   tick's apply.
7. **Given** a running stack **when** a developer greps the log sink for that same Session correlation
   ID **then** the matching structured log lines are returned.
8. **Given** a running stack **when** the developer opens the pre-provisioned dashboard **then** tick
   duration, tick overrun count, simulation lag, and per-partition consumer lag are plotted with live
   data and no manual datasource configuration.
9. **Given** a port already bound on the host **when** `make up` runs **then** it exits non-zero naming
   the conflicting port and the environment variable that overrides it.
10. **Given** a running stack **when** `make down VOLUMES=1` runs **then** volumes are removed and the
    next `make up` starts from an empty log.
11. **Given** the stack has been up and down five consecutive times **then** no orphaned containers,
    networks, or volumes remain.
12. **Given** a running stack **when** the Redpanda container is stopped **then** the server enters the
    read-only degradation mode from `AW-INF-005` rather than crashing, and `make up` recovers it.

## Interface contract

### Make targets

| Target | Arguments | Behavior |
|--------|-----------|----------|
| `up` | `PROFILE=full\|min` | starts the stack, blocks until healthy, prints service URLs |
| `down` | `VOLUMES=0\|1` | stops the stack; `VOLUMES=1` also removes volumes |
| `logs` | `SVC=<name>` | tails logs for one service, or all |
| `ps` | — | prints service, status, and exposed ports |

`PROFILE=min` starts the server, Redpanda, and Redis only — no observability, no Postgres — for fast
test loops. `PROFILE=full` is the default.

### Environment variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `ANDARA_GRPC_PORT` | `8443` | gRPC / Connect endpoint, TLS |
| `ANDARA_HTTP_PORT` | `8080` | health and metrics, plaintext, loopback only |
| `ANDARA_KAFKA_BROKERS` | `localhost:9092` | comma-separated |
| `ANDARA_SCHEMA_REGISTRY` | `http://localhost:8081` | schema registry |
| `ANDARA_REDIS_ADDR` | `localhost:6379` | hot projection |
| `ANDARA_POSTGRES_DSN` | `postgres://andara@localhost:5432/andara` | tabular projection |
| `ANDARA_TLS_CERT_FILE` / `ANDARA_TLS_KEY_FILE` | `./.local/tls/…` | server certificate |
| `ANDARA_TLS_CA_FILE` | `./.local/tls/ca.pem` | CA the CLI trusts |
| `ANDARA_METRICS_PORT` | `9090` | metrics UI |
| `ANDARA_DASHBOARD_PORT` | `3000` | dashboard UI |
| `ANDARA_TRACE_PORT` | `4317` | OTLP receiver |
| `ANDARA_DATA_DIR` | `./.local/data` | host path for local material |

Ports are overridden by their own variables, separately from the client-facing addresses above, so
that changing where the stack listens does not require rewriting a DSN:

| Variable | Default | Service |
|----------|---------|---------|
| `ANDARA_KAFKA_PORT` | `9092` | Redpanda Kafka API |
| `ANDARA_SCHEMA_REGISTRY_PORT` | `8081` | schema registry |
| `ANDARA_REDPANDA_ADMIN_PORT` | `9644` | Redpanda admin and metrics |
| `ANDARA_REDIS_PORT` | `6379` | Redis |
| `ANDARA_POSTGRES_PORT` | `5432` | Postgres |
| `ANDARA_OTLP_HTTP_PORT` | `4318` | OTLP HTTP receiver |
| `ANDARA_LOKI_PORT` | `3100` | Loki |
| `ANDARA_TLS_DIR` | `./.local/tls` | directory the CA and certificate live in |

`make up` also writes `./.local/cli.yaml` — an `andara-cli` config (`AW-CLI-001`) carrying
`server.address` and `server.tls_ca` for this stack — and prints the `ANDARA_CONFIG` export that
activates it. It is written into the repository's `.local/`, never into `$XDG_CONFIG_HOME`: a local
stack has no business editing a developer's global configuration.

Every port has an override. `make up` validates availability before starting anything and fails fast
naming both the port and its variable.

### Server telemetry configuration exercised by this story

| Key | Env | Default |
|-----|-----|---------|
| `telemetry.otlp_endpoint` | `ANDARA_OTLP_ENDPOINT` | `localhost:4317` (empty disables export) |
| `telemetry.service_name` | `ANDARA_SERVICE_NAME` | `andara-server` |
| `telemetry.environment` | `ANDARA_ENV` | `local` |
| `telemetry.log_format` | `ANDARA_LOG_FORMAT` | `json` |
| `telemetry.log_level` | `ANDARA_LOG_LEVEL` | `info` |

### Server obligations this stack imposes

The stack asserts three things about `andara-server` that no other story states. They are contracts,
not implementation hints — the stack does not work without them, and two of them fail silently.

| Obligation | Where it is exercised | Consequence if absent |
|------------|----------------------|-----------------------|
| `GET /readyz` on `ANDARA_HTTP_PORT`, returning 200 **only** when the World is loaded and the Tick Loop is running — not merely when the process is alive | compose healthcheck | `make up` returns before the World exists, and the "blocks until healthy" guarantee in AC-1 is a lie. `AW-INF-003` makes the same distinction for Kubernetes readiness, and for the same reason: a pod still replaying its log tail has nothing to serve. |
| `GET /metrics` on `ANDARA_HTTP_PORT`, Prometheus exposition format | `deploy/compose/prometheus/prometheus.yaml` scrapes it | Every `andara_*` panel on the dashboard is empty and nothing says why. |
| Logs exported **over OTLP** to `ANDARA_OTLP_ENDPOINT`, in addition to stdout | the collector's log pipeline into Loki | AC-7 fails. `telemetry.log_format: json` above describes the stdout encoding and is easy to read as stdout-only; a server that only writes to stdout puts nothing in the log sink, and the Session correlation ID a developer greps for is simply not there. |

`ANDARA_HTTP_PORT` is bound to loopback by the compose file: health and metrics are an operator
surface, not a player one.

## Data / state impact

Local only. `make down VOLUMES=1` is the documented reset.

**Amendment (implementation):** this section originally put the Redpanda log, Redis, and Postgres
data in bind mounts under `ANDARA_DATA_DIR`. They are Docker named volumes instead. Redpanda runs as
uid 101 and Postgres as uid 999, so a bind mount into a developer-owned directory fails to write on
Linux and behaves differently again on macOS — the stack would come up unhealthy on a clean machine,
which is the one thing this story exists to prevent. `ANDARA_DATA_DIR` remains the host directory for
local material, and `make down VOLUMES=1` removes the named volumes and that directory together, so
the documented reset is unchanged.

The local CA private key is generated per machine, never committed, and never reused across machines.
It is in `.gitignore` alongside the data directory.

## Observability requirements

This story delivers the backend other stories' instrumentation is verified against.

### Metrics
- Every service exposes a scrape endpoint and appears as `up == 1`.
- `andara_build_info{version, commit, env, content_version}` — gauge, always 1. Cardinality: one
  series per deployed build. `content_version` is here because ADR-0004 decoupled content from code,
  so reproducing a bug now requires naming both.
- Redpanda broker and consumer-group lag metrics are scraped, not just exposed.

### Logs
Structured JSON. Required fields on every server line: `ts`, `level`, `msg`, `service`, `env`,
`session_id` (empty outside a Session path), `trace_id`.

### Traces
OTLP export, 100% sampled locally. Verified present by this story: `command.execute` (parent),
`log.produce`, `sim.tick`, `sim.apply`. Per-entity spans are explicitly not emitted.

### Alerts
None. Local stacks do not alert. Alert rules are `AW-INF-005` and require an SLO first.

## Test plan

- **Unit:** none — no application logic.
- **Integration:** a CI job that runs `make up`, waits for health, asserts every declared topic exists
  with its declared configuration, asserts `andara_build_info` is scrapeable, asserts a synthetic trace
  round-trips, stops the broker and asserts read-only degradation, then runs `make down` and asserts no
  residual containers or volumes.
- **Manual/operator:**
  ```
  make up
  andara-cli play                 # expect: TLS connects, no insecure flag
  make logs SVC=andara-server
  make down VOLUMES=1 && make ps  # expect: no services
  ```

## Verification record — 2026-09-07

Executed against a running stack on a clean machine.

| AC | Result |
|----|--------|
| 1 | `make up` returns only after all eight services report healthy, then prints every URL. |
| 2 | Second `make up` recreates nothing; a record produced before it survives (high-watermark unchanged). |
| 3 | `make down` exits 0; a second `make down` on a stopped stack also exits 0. |
| 4 | All eight declared topics exist. `andara.commands.v1` has 64 partitions; `andara.state.v1`, `andara.accounts.v1`, and the three content topics carry `cleanup.policy=compact`. `make topics-diff` reports no drift. |
| 5 | **Pending `AW-SRV-005`.** The CA, the certificate, and its SANs (`localhost`, `127.0.0.1`, `andara-server`) are provisioned and verify against each other, and `make up` generates `.local/cli.yaml` naming the endpoint and the CA. There is no server or `andara-cli` to connect yet. |
| 6 | **Partial.** A synthetic OTLP trace with `command.execute` as parent and `log.produce` as child, carrying `session_id`, round-trips through the collector and is retrievable from Tempo by trace ID. The real spans arrive with `AW-SRV-005` and `AW-SRV-002`. |
| 7 | **Partial.** A synthetic OTLP log line is retrievable from Loki filtered by its Session correlation ID. |
| 8 | **Partial.** Datasources and the dashboard are provisioned from files and load with no manual configuration; all six panels are present. The `andara_*` panels have no data until the server emits, which is the point of provisioning them now. The broker-side lag query returns live data. |
| 9 | `make up` with 8081 held by another process: `port 8081 is already in use (schema registry). Override it with ANDARA_SCHEMA_REGISTRY_PORT=<port>`. Exits non-zero before starting anything. |
| 10 | `make down VOLUMES=1` removes all seven volumes and `ANDARA_DATA_DIR`; the next `make up` recreates all eight topics and the log is empty. |
| 11 | Five consecutive `make up` / `make down` cycles leave no orphaned containers and no orphaned networks. |
| 12 | **Pending `AW-SRV-010`.** Read-only degradation is server behavior; there is no server to degrade. |

Three findings worth carrying forward:

- **Redpanda's file-descriptor limit caps the cluster's partition count.** The container default of
  1024 descriptors allows 204 partitions; this declaration asks for 210. The broker comes up healthy
  and then refuses the last three topics with `INVALID_PARTITIONS: ... hardware constraints`. The
  compose file raises `nofile` to 65535. `andara.commands.v1` is 64 partitions permanently, so this
  is a standing constraint, not a one-off.
- **Consumer-lag metrics are off by default.** Redpanda computes
  `redpanda_kafka_consumer_group_lag_*` only when `enable_consumer_group_metrics` includes
  `consumer_lag`. Without it the dashboard's lag panel is silently empty — the worst failure mode for
  an observability panel. The setting is in the declaration and applied at `make up`.
- **`unclean.leader.election.enable` has no Redpanda equivalent.** Raft replication cannot elect a
  leader that is missing committed records, so the setting the zero-RPO claim rests on is asserted
  against real Kafka and not applied locally. `deploy/kafka/topics.yaml` splits `broker.assert` from
  `broker.local` for exactly this reason. This is a real local/production divergence, and it is the
  kind `AW-INF-005` must test for.

Also worth stating: Loki indexes only `service_name` and `deployment_environment`.
`session_id`, `trace_id`, and `tick` are structured metadata — queryable, but not stream labels. A
Loki stream label is the same cardinality hazard CLAUDE.md §7 rejects for metrics, wearing a
different hat.

## Definition of done

CLAUDE.md §8, plus:
- `make bootstrap && make up && make check` verified on a machine that has never run this repo.
- Topics come from `AW-INF-004`'s definitions, not from a compose-file duplicate.
- The dashboard is provisioned from a file in the repo, not clicked together in a UI.

## Open questions

- `[ASSUMPTION]` Docker Compose is the local orchestrator; podman-compose is best-effort.
- `[ASSUMPTION]` Redpanda locally, real Kafka in production, per ADR-0002 §7. Redpanda is Kafka-API
  compatible and single-binary; the risk is behavioral divergence under rebalance and tiered storage,
  neither of which M1 exercises. `AW-INF-005` must test against real Kafka before production.
- `[ASSUMPTION]` Observability stack is OTLP Collector + Prometheus + a trace backend + Grafana.
  Concrete choices are implementation details behind OTLP.
- **Follow-ups recorded 2026-09-11** (not reopened; this story stays at `review`): `AW-SRV-006` wants a
  MinIO service so the `s3` snapshot store is exercised locally, and `AW-SRV-016` wants a `pypiserver`
  so `andara-sdk` installs the way a Builder installs it. Both are one compose service each and land
  with the story that needs them.
