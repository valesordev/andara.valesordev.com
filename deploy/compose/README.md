# Local stack

`make up` starts it, `make down` stops it. Nothing here is run with `docker compose`
directly — `scripts/stack.sh` provisions TLS, checks ports, waits for health, and applies
the topic declaration, and skipping that is how a local stack starts lying to you.

```
make up                  # full stack; blocks until every service is healthy
make up PROFILE=min      # server + Redpanda + Redis only, for fast test loops
make ps
make logs SVC=redpanda
make down                # stop; volumes retained
make down VOLUMES=1      # stop and reset to an empty log
```

## What is running, and why it is real

| Service | Port | Why it is here |
|---------|------|----------------|
| Redpanda | 9092 (Kafka), 8081 (schema registry), 9644 (admin) | ADR-0002 makes Kafka the ordering authority. An in-memory stand-in would prove nothing about offsets, ordering, or rebalance. |
| Redis | 6379 | Hot projection (ADR-0002 §5.5). Built from `andara.state.v1`, never on the in-game read path. |
| Postgres | 5432 | Tabular projection: rosters, Builder queries. |
| OTLP collector | 4317 / 4318 | One ingress for traces, metrics, and logs, so a Session correlation ID looks the same in all three. |
| Prometheus | 9090 | Scrapes the server, the collector, and Redpanda. |
| Tempo | — | Trace backend. Reached through Grafana. |
| Loki | 3100 | The log sink you grep by Session correlation ID. |
| Grafana | 3000 | Provisioned dashboard and datasources, from files in this directory. |
| andara-server | 8443 (gRPC/TLS, not listening yet), 8080 (health, metrics, loopback) | Started once `cmd/andara-server` has Go sources. Local stack uses `content.source=dir` with `testdata/content/valid` until AW-SRV-012. |

CLAUDE.md §8 requires instrumentation verified against a real backend rather than merely
registered. That is what this stack is for.

## TLS is not optional

ADR-0003 puts TLS on the wire. `make up` provisions a local CA and a server certificate in
`.local/tls/`, and `andara-cli` trusts that CA out of the box. A local stack that skips
this teaches developers to pass an insecure flag, and one of them eventually ships it.

The CA private key is generated per machine, is never committed, and is never reused
across machines. `make tls FORCE=1` reissues.

## Ports

Every port has an environment-variable override, and `make up` checks availability before
starting anything — it names both the port and the variable rather than letting Docker
fail halfway through.

| Variable | Default |
|----------|---------|
| `ANDARA_GRPC_PORT` | 8443 |
| `ANDARA_HTTP_PORT` | 8080 |
| `ANDARA_KAFKA_PORT` | 9092 |
| `ANDARA_SCHEMA_REGISTRY_PORT` | 8081 |
| `ANDARA_REDPANDA_ADMIN_PORT` | 9644 |
| `ANDARA_REDIS_PORT` | 6379 |
| `ANDARA_POSTGRES_PORT` | 5432 |
| `ANDARA_TRACE_PORT` | 4317 |
| `ANDARA_OTLP_HTTP_PORT` | 4318 |
| `ANDARA_METRICS_PORT` | 9090 |
| `ANDARA_DASHBOARD_PORT` | 3000 |
| `ANDARA_LOKI_PORT` | 3100 |

## Topics come from one declaration

`deploy/kafka/topics.yaml`, applied by `make topics-apply` (which `make up` calls). There
is deliberately no topic configuration in the compose file: local and production topics
come from the same declaration, or they drift, and the properties that drift are the ones
that are permanent.

`make topics-diff` reports drift and exits non-zero.

## Two things that will bite you

**Redpanda's file-descriptor limit caps partitions.** `andara.commands.v1` alone is 64
partitions, permanently. The container default of 1024 file descriptors caps the whole
cluster at 204 partitions, and the failure arrives as `INVALID_PARTITIONS: ... hardware
constraints` after the broker is already healthy. The compose file raises `nofile` to
65535; do not remove it.

**Consumer-lag metrics are off by default.** Redpanda computes
`redpanda_kafka_consumer_group_lag_*` only when `enable_consumer_group_metrics` includes
`consumer_lag`. That setting is in the declaration under `broker.local` and is applied at
`make up`. Without it the dashboard's lag panel is silently empty.

## Where the local broker and the production broker differ

Redpanda locally, real Kafka in production (ADR-0002 §7). Two differences matter:

- `unclean.leader.election.enable` has no Redpanda equivalent — Raft replication cannot
  elect a leader missing committed records. The declaration asserts it against real Kafka
  and does not try to set it locally.
- Rebalance and tiered-storage behavior are where the two can genuinely diverge. M1
  exercises neither; `AW-INF-005` must test against real Kafka before production.
