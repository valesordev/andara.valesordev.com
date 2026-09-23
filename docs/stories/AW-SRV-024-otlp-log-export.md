---
id: AW-SRV-024
title: Export logs over OTLP so the log sink carries what stderr carries
epic: EPIC-07
component: server
type: bug
status: done
size: S
depends_on: [AW-SRV-001]
blocks: [AW-INF-010, AW-SRV-033]
lane: implementation
risk: low
---

## Context

`AW-INF-002` built the local stack on the premise its collector config states: "the server exports
traces, metrics, and logs over OTLP" — one receiver, three exporters, Loki fed from the OTLP logs
pipeline. The server never did the third. `server/telemetry` wires `otlptracegrpc` for spans and a
`slog.JSONHandler` to stderr for logs, and nothing bridges the two. Loki in the compose stack has
carried zero lines since it was provisioned; `AW-INF-002` AC-7 was recorded "partial" on 2026-09-11
against a synthetic line pushed by hand, and on 2026-09-17 a real Session's `session opened` line was
in `docker logs` and nowhere else. A log sink that has never received a log is the vacuous pass this
repo hunts (`AW-INF-001` PR #17 found two of the same shape).

This story adds the bridge. It does not change what is logged, at what level, or with which fields —
those are `AW-SRV-001`'s and `CLAUDE.md` §7's — only where it goes: stderr *and* the collector, the
same records, so the cluster's stdout-reading path (`AW-INF-008`) and the compose stack's OTLP path
see identical lines.

## User story

As a developer, I want to grep the log sink for a Session's correlation ID and get the server's lines
back, so that "what happened to this player" is one query and not `docker logs | grep`.

## Scope

### In scope
- An `slog.Handler` that fans out to the existing JSON stderr handler and an OTLP log exporter
  (`otlploggrpc`, the `otelslog` bridge), sharing the endpoint, resource attributes, and
  insecure/TLS settings the trace exporter already uses.
- Trace correlation: `trace_id` and `span_id` on every record emitted inside a span, as the stderr
  handler does today, and as OTLP log record fields — not only as attributes — so Loki's
  trace-to-logs link works.
- Backpressure: the exporter is batched and bounded; when the collector is down, records are dropped
  after the buffer fills, counted, and the process neither blocks nor grows.
- `telemetry.otlp_endpoint: ""` disables the log exporter with the trace exporter, as today.

### Out of scope
- Log content, levels, or field names. `ANDARA_LOG_FORMAT=text` being accepted and ignored is a
  separate `AW-SRV-001` follow-up and stays one.
- Metrics over OTLP. Prometheus scrape of `/metrics` is the metrics path (`AW-INF-003`, `AW-INF-008`).
- Shipping stdout with a sidecar in compose. Rejected: it would make the local and cluster paths
  carry different records, and the collector config already names the OTLP path as the design.

## Acceptance criteria

1. **Given** the compose stack **when** a Session is opened (`make stack-smoke`) **then** within 15 s
   `{service_name="andara-server"} | json | session_id="<id>"` in Loki returns the `session opened`
   line with `level`, `msg`, `session_id`, `trace_id`, `client_name` — the same fields as the stderr
   line, byte-for-byte equal values.
   *(Corrected 2026-09-18: the query is `{service_name="andara-server"} | session_id="<id>"` — over
   OTLP, Loki holds the message as the body, the level as `severity_text`, and every other field as
   structured metadata under its own name; there is no JSON body to parse. The values are equal.)*
2. **Given** that line **when** Loki's trace-to-logs link is followed **then** Tempo returns the
   `andara.game.v1.Game/OpenSession` trace with the same `trace_id`.
3. **Given** `telemetry.otlp_endpoint: ""` **when** the server boots **then** stderr logging is unchanged
   and no exporter is created — no connection attempt, no `otlp export failed` line.
4. **Given** the collector unreachable at boot **when** the server runs for 60 s **then** it is serving
   (readiness unaffected), stderr carries every line, and `andara_log_export_dropped_total` counts what
   the bounded queue discarded; **given** the collector returns **then** new lines arrive in Loki and
   RSS is within 10% of the pre-outage figure.
5. **Given** `make test` **when** it runs **then** a unit test asserts the fan-out handler emits every
   record to both handlers with equal attributes and that a `WithAttrs`/`WithGroup` on the fan-out
   reaches both.
6. **Given** the `stack` workflow **when** it runs `make stack-smoke` **then** the smoke asserts AC-1
   through the Loki API, replacing the synthetic push in `AW-INF-002`'s record.

## Interface contract

### Configuration

No new keys. `telemetry.otlp_endpoint` (`ANDARA_OTLP_ENDPOINT`) governs both exporters;
`telemetry.log_level` governs both handlers.

### Telemetry package

```go
// CONTRACT SKETCH — not an implementation
// telemetry.Init returns the same Telemetry; Log is now slog.New(fanout{stderr, otelslog})
// when OTLPEndpoint != "", else slog.New(stderr) as today.
// Shutdown flushes the log exporter before the trace exporter; bounded by the existing timeout.
type fanoutHandler struct{ handlers []slog.Handler }   // Enabled = any; Handle = all, first error returned
```

### Error taxonomy

| Condition | Effect |
|-----------|--------|
| collector unreachable | stderr unaffected; exporter retries with backoff; queue full → drop + count |
| exporter init fails at boot | fatal, same as the trace exporter today (a misconfigured endpoint is a configuration error) |

## Data / state impact

None.

## Observability requirements

- **Metrics:** `andara_log_export_dropped_total` (counter, no labels); `andara_log_export_queue_size`
  (gauge). No per-record labels.
- **Logs:** the exporter's own failures go to stderr only at `warn`, rate-limited to one line per
  minute — an exporter that logs its failure to itself is a loop.
- **Traces:** none new; the point is that existing spans and logs share `trace_id`.
- **Alerts:** none. A dropped-logs rate is a cause, not a symptom.

## Test plan

- **Unit:** fan-out semantics (AC-5); `otlp_endpoint: ""` builds no exporter (AC-3); record → OTLP log
  record mapping carries `trace_id`/`span_id` as record fields.
- **Integration (compose, `stack` workflow):** AC-1, AC-2 in `make stack-smoke`; AC-4 with
  `docker stop andara-otel-collector-1` for 60 s in the workflow.
- **Manual/operator:**
  ```
  make up && make stack-smoke
  curl -sG http://localhost:3100/loki/api/v1/query_range \
    --data-urlencode 'query={service_name="andara-server"} | json | msg="session opened"' --data-urlencode 'since=5m'
  # expect: one stream, the session_id from stack-smoke's output
  ```

### Verification record (2026-09-18; §8 clean, moved to `done` the same day)

- **AC-1, AC-2** `make stack-smoke`: the smoke's `session opened` line found in Loki by
  `session_id` within 15 s, its `session_id`, `trace_id`, `client_name`, and level equal to the
  stderr line's, and its `trace_id` resolving in Tempo to the `andara.game.v1.Game/OpenSession`
  trace. **AC-3** `TestSetup_NoEndpointNoExporter`. **AC-4** on the stack: a sixty-second
  `docker compose stop otel-collector` with Sessions opening throughout — `/readyz` 200 throughout,
  stderr complete, one rate-limited `otlp log export failed` line, nothing dropped at that rate
  (the queue holds 2,048; dropping under a flood is `TestLogExport_BoundedQueueDropsAndCounts`),
  RSS +2.3% from a warm heap against +2.9% for the same load with the collector up, and the smoke
  green again once the collector returned. **AC-5** `TestFanout_BothHandlersSeeEverything`.
  **AC-6** the smoke asserts AC-1 and AC-2 through the Loki and Tempo APIs; the stack workflow
  runs it and the outage.
- The record mapping — body, attributes, resource, and `trace_id`/`span_id` as record fields —
  is `TestLogExport_RecordCarriesTraceContext`.
- **For AW-INF-010:** `AW-INF-002` AC-7's synthetic push is superseded by the smoke's real query.
- **Scoped after `done` (2026-09-23, Brian):** OTLP log export is the path for environments with no
  stdout shipper — compose. On Kubernetes logs go through the container stream only, never both.
  `AW-SRV-033` makes the log exporter switchable apart from traces; this story's behavior stays the
  default.

## Definition of done

CLAUDE.md §8, plus: `AW-INF-002`'s record for AC-7 is superseded by a real query, recorded in
`AW-INF-010`.

## Open questions

- **Resolved 2026-09-18 (review pass):** the `otelslog` bridge and the SDK exporter, with the
  package's own bounded processor so the drop count is real (the correction below). The supported
  path; the fan-out and the processor are the only custom code.
- **Resolved 2026-09-18 (review pass):** 2,048 records, 1 s batches. Nothing player-visible; AC-4
  measured the outage behaviour at that size.
- **Corrected 2026-09-18:** the queue is the package's own `sdklog.Processor`, not the SDK's
  `BatchProcessor` — which also drops on a full queue but does not say how many, and the count is
  the point of AC-4. The bridge and the exporter are the SDK's.
- **Corrected 2026-09-18:** the exporter's own failure is reported at `warn` on stderr once a
  minute, as specified, and a first RSS measurement that started from an idle heap showed +47% —
  the Argon2id buffers of the Sessions the test opened warming the heap, not the queue; the same
  load with the collector up grew it 2.9%, and from a warm heap the outage grew it 2.3%. The
  workflow measures from a warm heap for that reason.
