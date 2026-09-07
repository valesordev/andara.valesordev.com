---
id: AW-INF-002
title: Local stack — Redpanda, datastores, observability, and TLS with one command
epic: EPIC-01
component: infra
type: infra
status: ready
size: M
depends_on: [AW-INF-001, AW-INF-004]
blocks: [AW-INF-003, AW-SRV-002]
assignee: claude-code
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
- Local CA and server certificate provisioning, with the CA trusted by `andara-cli` out of the box.
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
5. **Given** a running stack **when** `andara-cli play` connects **then** it does so over TLS against
   the locally provisioned CA with no insecure flag and no certificate warning.
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
| `ANDARA_DATA_DIR` | `./.local/data` | host path for volumes |

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

## Data / state impact

Local only. Volumes under `ANDARA_DATA_DIR` hold the Redpanda log, Redis, and Postgres data.
`make down VOLUMES=1` is the documented reset and `ANDARA_DATA_DIR` is in `.gitignore`.

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
