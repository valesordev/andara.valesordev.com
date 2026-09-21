---
id: AW-SRV-031
title: Submit idempotency — a client retry after an ambiguous outcome is the same Command
epic: EPIC-03
component: server
type: feature
status: in-progress
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
`AW-CLI-004` inherits "always send `client_ref`; retry `DEADLINE_EXCEEDED` with the same one"; the
`PLACEHOLDER` marker on `ingress.ReadOnlyMessage` comes off — Brian accepted the wording as written
on 2026-09-19 (`AW-SRV-010`), and the constant is no longer provisional.

## Open questions

- `[ASSUMPTION]` A 30 s window. A human retries within seconds; a client library within its own
  backoff. Longer only costs memory bounded by `max_pending` per Session. *Built as assumed;
  `ingress.idempotency_window` moves it.*
- `[ASSUMPTION]` The same `client_ref` with different text is a client bug worth a typed rejection
  rather than a silent dedup. A silent answer would hide the bug behind a correct-looking response.
  *Built as assumed: `INVALID_ARGUMENT` `duplicate_client_ref`, a sampled `warn`, counted under
  `rejected_parse` — the story contracts no outcome label for it, and adding one silently is the
  thing not to do; split it if the warn line proves the wrong signal.*

### As built (2026-09-21)

- `server/ingress`: the key lives on the Session's ingress state (`session.keys`, a `table` in
  `idempotency.go`): `lookup(ref, raw, now, window, max)` sweeps expired entries, answers a hit,
  refuses a different `raw` with `ErrDuplicateClientRef`, or makes an `entry` — at `max` keys
  evicting the oldest *resolved* one, never one still in flight (its retry must find it); with
  every key in flight the Session has `max_pending` Submits pending and the new ref is refused
  `pending_full`. The entry holds the Intent's SHA-256, not its text — the text is bounded only by
  the message limit and a key outlives its Submit by the window. The entry is made **before** the
  Submit takes its place in the Session's queue, so
  a retry finds it while the original is in flight (AC-5) and waits on it without queueing; a wait
  is bounded by the retry's own context. `resolve(e, resp, err, kept, now)` records the outcome and
  wakes waiters; `at` is the resolution time, so the window counts from when the outcome was known,
  and an entry never resolved cannot outlive the window from its making either.
- **What is remembered.** `kept` is true for an offset and for a `parse`/`authorize` rejection
  (`*command.Error` other than `in_transit`): the Command's fate. It is false for a transient
  refusal — `pending_full`, `world_read_only`, `in_transit`, the caller's context ending before its
  turn — which is dropped from the keys at once; a retry that was waiting on it runs the Command
  itself (`await` reports *again*). `rate_limited` is checked before the lookup and makes no entry.
  Caching `UNAVAILABLE` would answer the retry the client was told to make with the refusal again,
  for the whole window.
- **The outcome-unknown case.** `KafkaProducer.Produce` returns `*Unsettled` when its wait ended —
  the produce deadline, or the caller's context — with the record still live in the client:
  `ErrDeadline` (or the context's error) on the surface via `Unwrap`, and `Settled()`/`Outcome()`
  underneath. The pipeline passes it through untouched; `Ingress.settle` sees it with `errors.As`
  and keeps the entry open until the fate is known. The fate is classified from the record's
  promise and a counter of produce requests that left the process (`retryHook.OnBrokerWrite` with
  a nil error): the promise succeeded → **landed**, resolved with the offset, `produced_total`
  incremented; the promise failed and no produce request was written since the record was enqueued
  → **not written** (`ErrNotWritten`), the entry dropped, the retry a new Command; the promise
  failed and one was → **still unknown**, resolved `kept` with `ErrDeadline`, so a retry inside the
  window is told `DEADLINE_EXCEEDED` again rather than becoming a second record. That third case
  is a refinement of AC-3's premise: a record dropped with the producer client was *never sent*
  only when nothing carrying it went out; franz-go fails an in-flight batch with the same
  `ErrClientClosed` as a never-sent one and `kgo.Record` does not say which, so the counter is the
  discriminator, conservative by construction. When the promise had already fired when the wait
  ended (the record timed out in the client, or was dropped by the client swap) the fate is
  settled at once; otherwise a goroutine waits for it — the promise fires within the client's
  delivery timeout, which is `ingress.produce_deadline`.
- Taxonomy: `ErrDuplicateClientRef` → `INVALID_ARGUMENT` `duplicate_client_ref`; `ErrDeadline`'s
  comment corrected; `ReadOnlyMessage` no longer `PLACEHOLDER`. `memoryProducer` never returns
  `Unsettled` (no outcome-unknown case in memory).
- Config: `ingress.idempotency_window` / `ANDARA_INGRESS_IDEMPOTENCY_WINDOW` / `--ingress-idempotency-window`,
  `30s`, validated `> ingress.produce_deadline`; `keys.yaml`, `values.schema.json`, `_env.tpl`;
  `Options.IdempotencyWindow` and `Options.Tracer` on the ingress; boot logs it on
  `command ingress configured`.
- Observability: `andara_ingress_submits_total{outcome="deduplicated"}` pre-seeded;
  `andara_ingress_idempotency_keys` gauge (in flight or resolved inside the window; `forget`
  releases a Session's); `submit deduplicated` at `debug` with `session_id`, `client_ref`,
  `trace_id`; `client_ref reused for a different command` at `warn`, sampled with the rate-limit
  line; a dedup hit's `command.execute` span carries `deduplicated=true`, `pre_log=true`, and has no
  children (the ingress starts it; the pipeline never runs).
- Docs: README (config row, idempotency paragraph, taxonomy rows, metrics, logs, spans), glossary
  **Idempotency Key**, `AW-SRV-010`'s taxonomy row and AC-7 note corrected, `AW-CLI-004`'s
  inherited line.
- Test plan as built: unit `idempotency_test.go` — AC-1, AC-4 (parse and authorize, one audit
  record for two calls), AC-5 (three callers, one pending, one record), AC-6, AC-7 (window by the
  stepped clock, eviction oldest-first, `forget`), in-flight keys never evicted, AC-8,
  transient-not-remembered (incl. a waiter that runs the Command), the three fates at the seam through the fake log's `Unsettled`, the
  dedup trace shape. Integration `TestKafka_RetryAfterAmbiguousTimeoutIsTheSameCommand` (AC-2: the
  response to the produce dropped, the caller's 100 ms deadline fires first, the retry is answered
  the offset the idempotent producer's retry landed at, end offset +1, the record at that offset
  carries the `client_ref`) and `TestKafka_RetryAfterDroppedRecordIsANewCommand` (AC-3: dial refused,
  the discovering Submit `DEADLINE_EXCEEDED`, the key released as not written, the retry after
  recovery produces, end offset +1; skips if the probe noticed the outage before the Submit
  enqueued — a microsecond race the sibling read-only test also lives with). The manual step in
  the test plan (`andara-cli play --client-timeout`) is `AW-CLI-004`'s to run; the operator
  transcript here is `.local/probe retry`.

### Verification record (2026-09-21, compose stack, image built from this branch)

- `make check` clean; `server/ingress` under `-race` ×3; the Kafka integration suite against the
  stack's Redpanda, the two new tests ×3 without a skip.
- **Live, from the running server** (`.local/probe retry`, an operator Session, no Character bound
  so every Submit is the `authorize` rejection — which is the Command's fate and so is remembered):
  `look r1` → `PERMISSION_DENIED not_authorized` in 12 ms; `look r1` again → the same error in 0 ms;
  `north r1` → `INVALID_ARGUMENT duplicate_client_ref`; `look` with no ref twice → two rejections,
  no dedup. Scraped: `submits_total{deduplicated}=1`, `{rejected_authz}=3`, `{rejected_parse}=1`
  (the duplicate), `idempotency_keys` 1 while the Session lived and 0 after `CloseSession`. Loki:
  the `debug` `submit deduplicated` line with `session_id`, `client_ref=r1`, `trace_id`; the sampled
  `warn` line. Tempo: the retry's trace is `Game/Submit` → one `command.execute` with
  `deduplicated=true`, `pre_log=true`, no children. `command ingress configured` reports
  `idempotency_window=30s`. Prometheus has the new series.
- **Not reachable live until `AW-SRV-014` binds a Character:** a *produced* Submit's dedup (AC-1's
  offset answer) and the outcome-unknown fates (AC-2, AC-3) on the running server — shown by the
  integration suite against the stack's broker with end-offset assertions. `AW-SRV-014` inherits
  the live observation with the rest of its Event-delivery record.
