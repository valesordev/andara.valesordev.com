---
id: AW-SRV-031
title: Submit idempotency — a client retry after an ambiguous outcome is the same Command
epic: EPIC-03
component: server
type: feature
status: ready
size: S
depends_on: [AW-SRV-010]
blocks: [AW-CLI-004]
lane: implementation
risk: medium
---

## Context

`AW-SRV-010` AC-7 says a player must never move twice because a retry succeeded after an ambiguous
timeout, and its error taxonomy told the client to rely on the idempotent producer. That was a
grooming error, found on the review of PR #34 (Codex, 2026-09-19): Kafka's idempotent producer
deduplicates the *library's* retries of one record under one producer sequence. A client that
receives `DEADLINE_EXCEEDED` — or whose own RPC deadline fired first, with the record still live in
the producer — and submits again creates a second record with a second sequence. Both land. The
player moves twice.

The produce deadline is 2 s against a produce that takes milliseconds, so the ambiguous case is
reached only during an outage's first moment or a client-side timeout; it is rare and it is exactly
when a player retries. The fix is the ordinary one: an idempotency key the client sends and the
Gateway remembers. `SubmitRequest.client_ref` already exists and is already echoed on the Events the
Command causes, so the key is there — it only needs to mean something at the ingress.

## User story

As a player, I want to retry a command that timed out without fear of doing it twice, so that a
flaky connection costs me a moment and not a mistake.

## Scope

### In scope
- `(session_id, client_ref)` as the idempotency key of a Submit, remembered by the ingress for
  `ingress.idempotency_window` after the Submit's outcome is known.
- A retry with a known key returns the original outcome: the same `SubmitResponse` (partition,
  offset) for a produce that landed, the same typed error for a rejection. A retry whose original is
  still in flight waits for it, bounded by the caller's deadline.
- The outcome-unknown case: a Submit answered `DEADLINE_EXCEEDED` keeps its key open until the
  record's fate is known — the producer's delivery callback still fires — so a retry inside the
  window gets the real outcome, not a second record.
- A Submit with an empty `client_ref` is not deduplicated and is documented as such; `andara-cli`
  and the client always send one.

### Out of scope
- Dedup across a Gateway restart or across pods — the window is per process. A retry that lands on
  another pod (or after a restart) is a new Command; `AW-SRV-015`'s resume keeps a Session on one
  pod, and the window is short.
- Dedup by content. Two different Intents with one `client_ref` are a client bug; the second is
  rejected `duplicate_client_ref` rather than silently answered with the first's outcome.

## Acceptance criteria

1. **Given** a Submit whose produce landed at offset *N* **when** the same Session submits the same
   `client_ref` again within the window **then** the response is `{partition, offset N}`, nothing is
   produced, and `andara_ingress_submits_total{outcome="deduplicated"}` increments.
2. **Given** a Submit answered `DEADLINE_EXCEEDED` whose record later lands at offset *N* **when** the
   Session retries the `client_ref` **then** the response is offset *N* and the topic's end offset
   advanced by exactly one across both calls.
3. **Given** a Submit answered `DEADLINE_EXCEEDED` whose record was dropped with the producer client
   on entering the read-only state **when** the Session retries **then** the retry is a new produce
   (the original's fate is known: not written) and exactly one record lands.
4. **Given** a Submit rejected `INVALID_ARGUMENT` **when** retried with the same `client_ref` **then**
   the same rejection is returned without re-parsing, counted as `deduplicated`.
5. **Given** a retry arriving while the original is still in flight **when** the original completes
   **then** both calls return the same outcome and one record lands.
6. **Given** two different `raw` texts with one `client_ref` inside the window **when** the second is
   submitted **then** it is rejected `INVALID_ARGUMENT` with reason `duplicate_client_ref` and nothing
   is produced.
7. **Given** a key older than `ingress.idempotency_window` **when** it is retried **then** it is a new
   Command; the table holds at most `ingress.max_pending` keys per Session and evicts oldest-first,
   so a Session cannot grow the table without bound.
8. **Given** an empty `client_ref` **when** submitted twice **then** two records land; the README
   says so.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation; server/ingress
// Keyed by (sessionID, clientRef). An entry is created before the pipeline runs and
// resolved by the pipeline's outcome or, for a deadline, by the producer's delivery callback.
type outcome struct { resp *gamev1.SubmitResponse; err error; raw string; done chan struct{}; at time.Time }
// Submit: lookup → wait or return; miss → run; deadline → keep open until the callback.
```

- `ReasonDuplicateClientRef = "duplicate_client_ref"` on `INVALID_ARGUMENT`, `ErrorInfo` domain
  `andara.command`.
- `andara_ingress_submits_total{outcome}` gains `deduplicated`.
- `KafkaProducer.Produce` keeps reporting `ErrDeadline` at the deadline and additionally surfaces
  the record's eventual fate to the ingress (a `Settled` callback or a returned future), which is
  what AC-2 and AC-3 read.
- The taxonomy row in `AW-SRV-010` changes from "the client should retry and rely on idempotence" to
  "the client retries with the same `client_ref` and gets the original outcome".

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `ingress.idempotency_window` | `ANDARA_INGRESS_IDEMPOTENCY_WINDOW` | `30s` | how long a resolved key is remembered; must exceed `ingress.produce_deadline` |

## Data / state impact

None on the log. The table is per-process Session state, bounded per Session by `ingress.max_pending`
and per key by the window.

## Observability requirements

- **Metrics:** `andara_ingress_submits_total{outcome="deduplicated"}`; `andara_ingress_idempotency_keys`
  — gauge, per process.
- **Logs:** `debug` on a dedup hit with `session_id`, `trace_id`, `client_ref`; `warn` on
  `duplicate_client_ref`, sampled like the rate-limit line.
- **Traces:** `command.execute` on a dedup hit carries `deduplicated=true` and no child spans.
- **Alerts:** none.

## Test plan

- **Unit:** AC-1, AC-4, AC-5, AC-6, AC-7 against the fake log; eviction order.
- **Integration (Redpanda):** AC-2 with the dialer fault that loses one produce response
  (`AW-SRV-010`'s `TestKafka_AmbiguousTimeoutNoDuplicate` extended with a client retry); AC-3 with the
  dialer refusal; end-offset assertions on both.
- **Manual/operator:** `andara-cli play` with `--client-timeout 50ms` against a paused broker, retry
  `north`, `docker compose unpause`, observe one `CharacterArrived`.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-010`'s taxonomy row and README paragraph are corrected in this story;
`AW-CLI-004` inherits "always send `client_ref`; retry `DEADLINE_EXCEEDED` with the same one".

## Open questions

- `[ASSUMPTION]` A 30 s window. A human retries within seconds; a client library within its own
  backoff. Longer only costs memory bounded by `max_pending` per Session.
- `[ASSUMPTION]` The same `client_ref` with different text is a client bug worth a typed rejection
  rather than a silent dedup. A silent answer would hide the bug behind a correct-looking response.
