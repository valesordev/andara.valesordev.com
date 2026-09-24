---
id: AW-SRV-031
title: Submit idempotency — a client retry after an ambiguous outcome is the same Command
epic: EPIC-03
component: server
type: feature
status: done
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
3. *(Rewritten 2026-09-21 at the flip to `review`, after the review of PR #38: the producer library
   cannot say whether a failed record was ever sent, so the fate has two answers, not one.)*
   **3a. Given** a Submit answered `DEADLINE_EXCEEDED` whose record failed in the producer client
   with no produce request from this process having reached a socket since it was enqueued
   **when** the Session retries **then** the retry is a new produce (the original's fate is known:
   not written) and exactly one record lands.
   **3b. Given** the same, but a produce request *did* reach a socket in that interval **when** the
   Session retries inside the window **then** it is answered `DEADLINE_EXCEEDED` with reason
   `outcome_unknown` — terminal: no `RetryInfo`, the client stops and the player `look`s — and at
   most one record lands. 3b is a sound over-approximation of "sent": it can call a never-sent
   record unknown under concurrent produce traffic, never a sent one not-written.
4. **Given** a Submit rejected `INVALID_ARGUMENT` **when** retried with the same `client_ref` **then**
   the same rejection is returned without re-parsing, counted as `deduplicated`.
5. **Given** a retry arriving while the original is still in flight **when** the original completes
   **then** both calls return the same outcome and one record lands.
6. **Given** two different `raw` texts with one `client_ref` inside the window **when** the second is
   submitted **then** it is rejected `INVALID_ARGUMENT` with reason `duplicate_client_ref` and nothing
   is produced.
7. **Given** a key older than `ingress.idempotency_window` **when** it is retried **then** it is a new
   Command; the table holds at most `ingress.max_pending` keys per Session and evicts the oldest
   *resolved* key first — a key still in flight is never evicted and never expires, whatever its
   age, because its retry must find it (reconciled 2026-09-21; the sketch said "oldest-first") —
   so a Session cannot grow the table without bound; with every key in flight a new ref is refused
   `pending_full`.
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
- **Added at the flip to `review` (2026-09-21), as built:**

  | Condition | gRPC code | `ErrorInfo.reason` | `RetryInfo` | outcome label |
  |-----------|-----------|--------------------|-------------|---------------|
  | same `client_ref`, different `raw`, inside the window | `INVALID_ARGUMENT` | `duplicate_client_ref` | — | `rejected_ref` (its own series, not `rejected_parse`) |
  | the record's fate settled unknown (AC-3b) | `DEADLINE_EXCEEDED` | `outcome_unknown` | none — terminal | `deadline` |
  | the produce deadline, fate still open | `DEADLINE_EXCEEDED` | `produce_deadline` | — | `deadline` |

  The client rule that follows (`AW-CLI-004` carries it): `produce_deadline` → retry with the same
  `client_ref` **on the same Session**; `outcome_unknown` → stop and `look`. The fate surfaces as
  `*ingress.Unsettled` returned by `KafkaProducer.Produce` (the "returned future" above), and
  `SubmitRequest.client_ref`'s comment in `game.proto` now says it is the Idempotency Key.

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

- **Resolved 2026-09-21 (Brian):** a 30 s window. A human retries within seconds; a client library
  within its own backoff. Longer only costs memory bounded by `max_pending` per Session. Built so;
  `ingress.idempotency_window` moves it.
- **Resolved 2026-09-21 (Brian):** the same `client_ref` with different text is a client bug worth a
  typed rejection rather than a silent dedup — a silent answer would hide the bug behind a
  correct-looking response. Built so: `INVALID_ARGUMENT` `duplicate_client_ref`, a sampled `warn`,
  counted as `outcome="rejected_ref"` (ruled on the review of PR #38: its own series, so a client
  release that starts reusing refs is not a rise in typos).

**Merged 2026-09-21 as PR #38 (`fb4902e`); `status: review`.** AC-3 and AC-7 reconciled above; the
`outcome_unknown` and `rejected_ref` rows added to the contract; `AW-CLI-004` has since landed the
client rule (PR #39, the retry bound to its Session). Carried to `AW-SRV-014`: a *produced* Submit's
dedup and the AC-2/AC-3 fates observed on the running server.

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
  wakes waiters; `at` is the resolution time, so the window counts from when the outcome was known.
  Only a *resolved* entry expires: one in flight lives until its outcome is known, however long
  the queue wait, the transit hold, and the produce took (the queue wait alone is bounded by
  `grpc.max_request_timeout`, 30 s — the window's own length), because a retry arriving past the
  window must find it and wait, not run the Command beside it (review of PR #38, Codex P1). Every
  path resolves an entry — a refused `enter`, the caller's context before its turn, the pipeline's
  outcome, the `Unsettled` goroutine — and `forget` bounds the rest by the Session's life.
- **What is remembered.** `kept` is true for an offset and for a `parse`/`authorize` rejection
  (`*command.Error` other than `in_transit`): the Command's fate. It is false for a transient
  refusal — `pending_full`, `world_read_only`, `in_transit`, the caller's context ending before its
  turn — which is dropped from the keys at once; a retry that was waiting on it runs the Command
  itself (`await` reports *again*). `rate_limited` is checked before the lookup and makes no entry.
  Caching `UNAVAILABLE` would answer the retry the client was told to make with the refusal again,
  for the whole window. One consequence: an `authorize` rejection for *no Character bound* is the
  Command's fate and is remembered, so a Submit that failed just before `AW-SRV-014`'s bind is still
  refused if retried with the same ref inside the window; a fresh ref per typed line makes it moot.
- **The outcome-unknown case.** `KafkaProducer.Produce` returns `*Unsettled` when its wait ended —
  the produce deadline, or the caller's context — with the record still live in the client:
  `ErrDeadline` (or the context's error) on the surface via `Unwrap`, and `Settled()`/`Outcome()`
  underneath. The pipeline passes it through untouched; `Ingress.settle` sees it with `errors.As`
  and keeps the entry open until the fate is known. The fate is classified from the record's
  promise and a process-wide counter of produce requests written (`retryHook.OnBrokerWrite` with
  a nil error): the promise succeeded → **landed**, resolved with the offset, `produced_total`
  incremented; the promise failed and no produce request from this process reached a socket since
  the record was enqueued → **not written** (`ErrNotWritten`), the entry dropped, the retry a new
  Command; the promise failed and one did → **outcome unknown** (`ErrOutcomeUnknown`), resolved
  `kept`, so a retry inside the window is answered `DEADLINE_EXCEEDED` with reason
  `outcome_unknown` — a distinct, terminal reason (added on the review of PR #38): the client
  stops retrying and the player `look`s, whereas `produce_deadline` is what it retries on. The
  rule as it is, not as AC-3 wished it: franz-go's promise does not say whether a failed record
  was ever sent — a written-then-failed batch and a never-written one arrive at the promise
  identical, and the field that knows is unexported — so the counter is the discriminator, a
  sound over-approximation of "sent": it is loaded before `TryProduce`, so any write of this
  record's batch bumps it, and an errored `conn.Write` is not counted because a produce request is
  one frame per `Write`, so an error means no frame the broker could process. It is imprecise in
  the safe direction only: under concurrent produce traffic another Session's request turns a
  never-sent record into *unknown*, and the retry is told `outcome_unknown` rather than becoming a
  new produce. AC-3 as written holds in a quiet process, which is what its integration test
  exercises; the review owns its rewrite at §8. When the promise had already fired when the wait
  ended (the record timed out in the client, or was dropped by the client swap) the fate is
  settled at once; otherwise a goroutine waits for it — the promise fires within the client's
  delivery timeout, which is `ingress.produce_deadline`.
- Taxonomy: `ErrDuplicateClientRef` → `INVALID_ARGUMENT` `duplicate_client_ref`, outcome
  `rejected_ref`; `ErrOutcomeUnknown` → `DEADLINE_EXCEEDED` `outcome_unknown`, outcome `deadline`;
  `ErrDeadline`'s comment corrected; `ReadOnlyMessage` no longer `PLACEHOLDER`. `memoryProducer` never returns
  `Unsettled` (no outcome-unknown case in memory).
- Config: `ingress.idempotency_window` / `ANDARA_INGRESS_IDEMPOTENCY_WINDOW` / `--ingress-idempotency-window`,
  `30s`, validated `> ingress.produce_deadline`; `keys.yaml`, `values.schema.json`, `_env.tpl`;
  `Options.IdempotencyWindow` and `Options.Tracer` on the ingress; boot logs it on
  `command ingress configured`.
- Observability: `andara_ingress_submits_total{outcome="deduplicated"}` and `{outcome="rejected_ref"}`
  pre-seeded;
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
  stepped clock, eviction oldest-first, `forget`), in-flight keys never evicted, an in-flight key
  outliving the window (at the table and through the ingress: the retry waits, one record lands),
  AC-8, transient-not-remembered (incl. a waiter that runs the Command), the three fates at the seam through the fake log's `Unsettled`, the
  dedup trace shape; `producer_test.go` the fate discriminator alone (promise failed with the
  counter unmoved → `ErrNotWritten`; bumped by an unrelated write → `ErrOutcomeUnknown`; promise
  succeeded → landed). Integration `TestKafka_RetryAfterAmbiguousTimeoutIsTheSameCommand` (AC-2: the
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
  no dedup. Scraped: `submits_total{deduplicated}=1`, `{rejected_authz}=3`, `{rejected_ref}=1`
  (the duplicate; re-run after the review's relabel), `idempotency_keys` 1 while the Session lived and 0 after `CloseSession`. Loki:
  the `debug` `submit deduplicated` line with `session_id`, `client_ref=r1`, `trace_id`; the sampled
  `warn` line. Tempo: the retry's trace is `Game/Submit` → one `command.execute` with
  `deduplicated=true`, `pre_log=true`, no children. `command ingress configured` reports
  `idempotency_window=30s`. Prometheus has the new series.
- **Not reachable live until `AW-SRV-014` binds a Character:** a *produced* Submit's dedup (AC-1's
  offset answer) and the outcome-unknown fates (AC-2, AC-3) on the running server — shown by the
  integration suite against the stack's broker with end-offset assertions. `AW-SRV-014` inherits
  the live observation with the rest of its Event-delivery record.

### §8 pass (2026-09-24, architecture) — done

Against `origin/main` `033f2c6` and the compose stack. Every AC has its test, and every test runs
in CI:
- The unit tests (`idempotency_test.go`, `producer_test.go`) run in `make test`.
- AC-2 and AC-3a (`TestKafka_RetryAfterAmbiguousTimeoutIsTheSameCommand`,
  `…DroppedRecordIsANewCommand`) run in `make test-integration`. That passed locally today, and the
  dropped-record test did not skip.
- AC-3b's "no `RetryInfo`" holds by construction: `errors.go` attaches `RetryInfo` to
  `UNAVAILABLE` only.

The rest of §8:
- `ingress.idempotency_window` is documented in every required place.
- The metrics, logs and span were seen live in the record above. The produced-Submit dedup was
  closed live on `AW-SRV-014`'s record, and the ambiguous fates are enumerated in `AW-CLI-007`'s
  Definition of done (PR #68, item 3).
- Idempotency Key is in the glossary. No migration, no open `[ASSUMPTION]`.
- All four story DoD lines hold. The `PLACEHOLDER` marker is gone from `server/`.
