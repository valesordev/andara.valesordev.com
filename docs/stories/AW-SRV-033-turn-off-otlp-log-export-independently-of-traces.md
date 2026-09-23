---
id: AW-SRV-033
title: Turn off OTLP log export independently of traces
epic: EPIC-07
component: server
type: feature
status: ready
size: S
depends_on: [AW-SRV-024]
blocks: []
lane: implementation
risk: low
---

## Context

`AW-SRV-024` made the server export logs over OTLP so the compose stack's Loki had something in
it: compose runs no stdout shipper, and the collector's logs pipeline was empty. One key,
`telemetry.otlp_endpoint`, governs both exporters. On Kubernetes that is wrong. The cluster's node
agent (Grafana `k8s-monitoring`, `podLogsViaLoki`) already ships every container's output to Loki,
so once `AW-INF-008` points `dev`/`prod` at the Alloy receiver for traces, every log line is written
twice: once from the container stream (`container="server"`) and once through the receiver.

**Decided 2026-09-23 (Brian): a log reaches the sink one way per environment, never both. On
Kubernetes that way is stdout, collected by the node agent. The server does not send logs to the
receiver there.** Traces still go over OTLP. So the server needs to turn off OTLP *log* export
without turning off traces, and the chart needs to set that for every Kubernetes install.

## User story

As an operator, I want the server's logs to reach Loki once, from the container stream, so that
the log bill and every query count each line once and the Kubernetes log path is the one I
already know how to operate.

## Scope

### In scope
- A server key `telemetry.otlp_logs` that switches the OTLP log exporter independently of the
  trace exporter. stderr logging is untouched by it.
- The chart half, in the same PR, because the schema admits the key only once the server reads it:
  `server.telemetry.otlp_logs: false` in the chart's `values.yaml`. Every chart install is Kubernetes,
  so the chart's default carries the rule and no environment file has to repeat it.
- `server/README.md`: the configuration table and the logging section say which key governs which
  exporter.

### Out of scope
- Compose keeps OTLP log export (the server default, below). Moving compose to a stdout shipper and
  deleting the OTLP log path would be a separate `INF` story, and is not planned.
- Trace export failures logging no JSON line (no `otel.SetErrorHandler`) — found by `AW-INF-008`, a
  separate `AW-SRV-024` follow-up.
- stderr versus stdout. The server logs to stderr; the kubelet captures both streams and the node
  agent ships both, so "stdout" in the decision means the container's output, and nothing moves.

## Acceptance criteria

1. **Given** `telemetry.otlp_endpoint` set and `telemetry.otlp_logs: false` **when** the server boots
   and logs **then** no OTLP log exporter is built — no log record reaches the endpoint, and
   `andara_log_export_dropped_total` and `andara_log_export_queue_size` are not registered, as with an
   empty endpoint today — while stderr carries every line and spans still export to the endpoint.
2. **Given** `telemetry.otlp_logs` unset **when** the server boots with an endpoint **then** behavior is
   `AW-SRV-024`'s, unchanged: the `stack` workflow's `make stack-smoke` still finds the `session
   opened` line in the compose Loki and follows it to the trace in Tempo.
3. **Given** `telemetry.otlp_endpoint: ""` **when** the server boots **then** neither exporter is built,
   whatever `telemetry.otlp_logs` says — the endpoint is still the master switch.
4. **Given** `ANDARA_OTLP_LOGS=maybe` **when** the server boots **then** it exits with code 1 and a
   configuration error naming `ANDARA_OTLP_LOGS`, as for any malformed bool key.
5. **Given** `make values-schema-check` and `make helm-test` **when** they run **then** the key is in
   the schema (no longer a pending warning), and every environment renders `ANDARA_OTLP_LOGS=false`
   into the `andara-config` ConfigMap.
6. **Given** the chart installed in `andara-dev` with `AW-INF-008`'s endpoint **when** one Session is
   opened **then** Loki returns exactly one `session opened` line for that `session_id` across
   `{namespace="andara-dev"}` — the container stream's — not two. (On the box, with `make
   observe-check`'s credentials; recorded, not CI.)

## Interface contract

### Configuration

| Key | Env | Type | Default | Meaning |
|-----|-----|------|---------|---------|
| `telemetry.otlp_logs` | `ANDARA_OTLP_LOGS` | bool | `true` | Export log records over OTLP to `telemetry.otlp_endpoint`. Has no effect when the endpoint is empty. |

Registered in `deploy/helm/andara/keys.yaml` (groomed with this story; pending until the server
reads it). The server default is `true` so that compose, which has no other log path, keeps
`AW-SRV-024`'s behavior with no change to its environment. The chart overrides it to `false`.

| `otlp_endpoint` | `otlp_logs` | Traces | Logs over OTLP | stderr |
|-----------------|-------------|--------|----------------|--------|
| `""` | any | off | off | every line |
| set | `true` | on | on | every line |
| set | `false` | on | off | every line |

### Telemetry package

```go
// CONTRACT SKETCH — not an implementation
// newLogExport returns (nil, nil, nil, nil) — export disabled, nothing registered —
// when cfg.OTLPEndpoint == "" || !cfg.OTLPLogs. Setup is otherwise unchanged: the
// fan-out handler exists only when a log exporter does.
```

## Data / state impact

None. A rollout that flips the key changes where log lines are delivered, not what they say; the
container stream carried every line before and after.

## Observability requirements

- **Logs:** unchanged in content. With `otlp_logs: false` the Loki copy is the node agent's, labelled
  `namespace`, `pod`, `container`; the JSON body still carries `trace_id`, which is what Loki's
  trace link reads.
- **Metrics:** the two `andara_log_export_*` series exist only where OTLP log export is on. No new
  series.
- **Traces, alerts:** none.

## Test plan

- **Unit:** the three rows of the truth table (AC-1, AC-3) — no log exporter and no
  `andara_log_export_*` registration when off, a tracer provider with an exporter when the endpoint is
  set; malformed `ANDARA_OTLP_LOGS` (AC-4) in `server/config`'s table tests.
- **Integration (compose, `stack` workflow):** AC-2 is the existing `make stack-smoke`, unchanged.
- **Chart (`make helm-test`):** AC-5 — `ANDARA_OTLP_LOGS` is `"false"` in the ConfigMap for `local`,
  `dev`, `prod`.
- **Manual/operator (box):**
  ```
  make helm-install ENV=dev && make stream-soak ENV=dev SOAK=1m
  make observe-check ENV=dev          # prints the session_id of the latest `session opened`
  # Grafana Explore, Loki: count_over_time({namespace="andara-dev"} |= "<session_id>" |= "session opened" [1h])
  # expect: 1
  ```

## Definition of done

CLAUDE.md §8, plus: `AW-INF-008`'s record of the double write is closed by a line pointing here.

## Open questions

- **Resolved 2026-09-23 (Brian): stdout on Kubernetes, one log path per environment.** Recorded in
  Context.
