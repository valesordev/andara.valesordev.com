# Shared schema registry

`local` and `dev` share one schema registry in `andara-shared` (AW-INF-057, `ADR-0013` decision 8).
prod's isn't on this cluster (decision 10).

| | |
|---|---|
| Product | **Karapace 6.2.4** (`ghcr.io/aiven-open/karapace:6.2.4`, pinned by digest in `registry.yaml`) |
| Why Karapace | It speaks the Confluent API that `scripts/schemas.py` uses natively, so AW-INF-004's Redpanda findings re-measure like for like, and it keeps its schemas in a compacted topic, with no volume or database |
| Service | `andara-schema-registry.andara-shared.svc:8081` |
| URL variable | `ANDARA_SCHEMA_REGISTRY_URL`. When unset, `schemas.py` port-forwards to the Service for `dev` and for `local` with `BROKER=cluster` (compose's registry stays `local`'s default while `deploy/compose/` exists) |
| Storage | the topic `_schemas` on `andara-log`: 1 partition, RF 3, `compact`, declared in `deploy/kafka/topics.yaml`'s `platform_topics` |
| Broker principal | `andara-schema-registry`, platform-scoped (`platform_principals`), applied with the others by AW-INF-056 when SASL turns on |
| Installed by | `make shared-up` (after the platform topics), and removed by `make shared-down` |

## Subjects

Every subject carries the environment prefix of the topic it describes:
`dev.andara.commands.v1-value`, `local.andara.events.v1-andara.log.v1.Event`. A reference's subject
is `<prefix>.<import path>` (`dev.andara/content/v1/zone.proto`) and its name is the import path, so
the environments never share a subject. `make schemas-apply ENV=<local|dev> [BROKER=cluster]`
registers them. `make schemas-diff` checks its environment's subjects against
`deploy/kafka/schemas.yaml` and fails on any subject with no environment prefix.

## Network

Nothing in `server/`, `internal/`, `cmd/` or `admin/` reads the registry, and a test keeps it so.
The NetworkPolicy therefore admits no application namespace, only the toolbox in `andara-shared`;
`schemas.py`'s port-forward isn't governed by NetworkPolicy. This stands in for the registry's lack
of authentication. `make shared-netcheck` proves it, together with the Kafka listener and the object
store.

## What Karapace does differently from Redpanda

Re-measured on 2026-10-24 against AW-INF-004's findings on Redpanda (2026-09-10):

| Finding | Redpanda | Karapace 6.2.4 |
|---|---|---|
| A removed field under BACKWARD | accepted | accepted |
| A field renumbered 2 → 7 | accepted | **rejected** (`FIELD_TAG_ALTER`) |
| A scalar type change, a removed message | rejected | rejected |
| A reference at `version: -1` | rejected (422) | **accepted** |
| An import-path subject unencoded in the URL | 404 | 404 |
| A non-ASCII character in a comment | accepted | **HTTP 500**; `schemas.py` sends comments as ASCII |
| Stored text equals sent text | no (canonicalised) | no (canonicalised); `diff` uses the lookup |

`buf breaking` (`make proto-check`) stays the additive-only rule (ADR-0007). The registry is a second
opinion, and it still accepts a removed field.

## Observability

- **Metrics:** Prometheus on `GET /metrics` (port 8081): `karapace_*` and HTTP request series. Nothing
  scrapes it.
- **Logs:** Karapace logs to stdout. Every `schemas.py` line reads `schemas: <subject>
  registered|unchanged id=<n> run=<id>`, with a refused change at `error` naming the subject and the
  compatibility mode.
- **Alerts:** none. An unavailable registry affects only `schemas-apply`, never a player.
