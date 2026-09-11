# andara-server

The world simulation, session gateway, and persistence adapters. Go, deployed to Kubernetes.

`server/sim` is the load-bearing constraint of this codebase. It imports no network, no datastore,
no filesystem, no wall clock, and no global randomness. This is enforced by `depguard` in
`.golangci.yml`, not by convention — see `ADR-0001` for why the seam exists and `ADR-0002` for why
determinism is a production dependency rather than a preference.

```
server/sim/        simulation core — types, BuildWorld, PartitionFor, CanonicalBytes
server/content/    ZoneDefinition source adapters (dir now; Kafka in AW-SRV-012)
server/boot/       load orchestration, /livez /readyz /metrics
server/config/     flag > env > file > default
server/telemetry/  JSON logs, Prometheus registry, boot traces
```

## Configuration

Every key is settable by YAML config file (`--config` / `ANDARA_CONFIG`), by environment
variable, and (where it is a process flag) by flag. Precedence is **flag > env > file > default**.

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `content.source` | `ANDARA_CONTENT_SOURCE` | `kafka` | `kafka` or `dir`. Kafka is AW-SRV-012; until then the process exits 1 naming the source. Local compose uses `dir`. |
| `content.path` | `ANDARA_CONTENT_PATH` | `./content` | Directory of Zone Definition JSON files. Used only when `content.source=dir`. |
| `content.strict_orphans` | `ANDARA_STRICT_ORPHANS` | `false` | When `true`, Rooms with no inbound Exit in their Zone are errors. |
| `http.port` | `ANDARA_HTTP_PORT` | `8080` | `/livez`, `/readyz`, `/metrics`. Plaintext, operator surface. |
| `telemetry.otlp_endpoint` | `ANDARA_OTLP_ENDPOINT` | `localhost:4317` | Empty disables export. |
| `telemetry.service_name` | `ANDARA_SERVICE_NAME` | `andara-server` | |
| `telemetry.environment` | `ANDARA_ENV` | `local` | |
| `telemetry.log_format` | `ANDARA_LOG_FORMAT` | `json` | |
| `telemetry.log_level` | `ANDARA_LOG_LEVEL` | `info` | |

`--validate-only` loads and validates, prints every finding, and exits without serving. Exit `0`
on a valid World, `1` on any fatal finding.

### In Kubernetes

The chart (`deploy/helm/andara`, AW-INF-003) sets every key above through `server.<key>` in a
values file — `server.content.source: dir` renders `ANDARA_CONTENT_SOURCE=dir` into the
`andara-config` ConfigMap. The mapping lives in `deploy/helm/andara/keys.yaml`, which `make
values-schema-check` holds against this package: **a new `ANDARA_*` read anywhere under `server/`
fails `make check` until `keys.yaml` lists it**, and then `make values-schema` regenerates the
values schema and the env template. Keys that are groomed but not yet read here are in
`keys.yaml` already and stay out of the schema until the code lands — a values file cannot set
a key this binary would ignore.

`ANDARA_SIM_PARTITIONS` is the one variable the chart sets that is not a value: the `partitions`
init container derives it from the pod ordinal (`p mod replicaCount == ordinal`, over 0–63) and
the server container sources it before exec. Probes: startup and readiness on `/readyz`, liveness
on `/livez`, all on `http.port`. `/livez` must never depend on Kafka or a datastore.

Dir-mode files are protobuf JSON (`formatVersion`, one Zone per `.json` file). That is the test
and CLI surface, not the Builder language (ADR-0009).

`Zone.Partition` is `PartitionFor(ZoneID)`: FNV-1a 32 of the ID, modulo 64. Callers that produce
to `andara.commands.v1` (AW-SRV-010) must use this function, not a Kafka client default.
`World.PartitionOf(RoomRef)` answers the same question for a Room, which has no Partition of its
own — it inherits its Zone's, because a Zone is the unit of simulation authority. The mapping is
pinned by golden vectors in `server/sim/partition_test.go`: under ADR-0002 changing it is a
migration, not a refactor, because repartitioning a keyed topic reorders history.

`CanonicalBytes(*World)` serializes topology in a stable order — zones by ID, rooms by ID, exits by
Direction — with free-text fields escaped so the encoding is injective. It is how AW-SRV-001 AC-10
is asserted, and it is deliberately not ADR-0002's State Hash, which covers mutable state and
arrives with `AW-SRV-002`.
