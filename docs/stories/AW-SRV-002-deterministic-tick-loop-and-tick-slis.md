---
id: AW-SRV-002
title: Deterministic tick loop driven by partition consumers, with tick SLIs
epic: EPIC-02
component: server
type: feature
status: in-progress
size: M
depends_on: [AW-SRV-001, AW-INF-002, AW-INF-004]
blocks: [AW-SRV-003, AW-SRV-004, AW-INF-010, AW-INF-011]
lane: implementation
risk: high
---

## Context

The Tick Loop is the heartbeat of the World and the source of the three SLIs CLAUDE.md §7 names as
first-class from the first server story: tick duration, tick overrun count, and simulation lag.

ADR-0002 changes what drives it. The loop's input is no longer an in-process queue — it is a Kafka
consumer over the Partitions this process owns. Commands are durable *before* the tick sees them, by
construction, because consuming is the only way they arrive. That removes the durability write from
the tick's critical path entirely, and it is the property that makes ADR-0001's sharding a rebalance
rather than a migration.

It also introduces the one genuinely subtle problem in this story. "Apply whatever records were
available when the tick started" is timing-dependent, and therefore not replayable. ADR-0002 §4 closes
it: each tick **records** the offset range it applied, and replay reads those boundaries rather than
re-deciding them.

## User story

As an operator, I want the tick loop to publish its duration, overruns, and lag continuously, so that
I can see the World falling behind before players tell me it has.

## Scope

### In scope
- The Tick Loop: drain buffered records for the owned Partitions, advance one Tick, emit Events, sleep
  to schedule.
- Kafka consumer for `andara.commands.v1`, consumer group `andara-sim-<env>`, buffering off the tick
  goroutine so a broker stall is not a tick stall.
- Tick boundary recording: the offset range applied per Partition, emitted as `TickCompleted`.
- Offset checkpointing tied to the state it produced, so recovery resumes at a known-good point.
- Idempotent re-apply: records between the checkpoint and a crash are re-applied deterministically.
- Mutable World state, Zone-scoped, mutated only by the tick that owns that Zone's Partition.
- Cross-Zone effects as Commands produced to the target Zone's Partition — always, including
  same-process (ADR-0001 §4).
- Deterministic scheduling via a `TickClock` interface: a real implementation and a stepped test one.
- Deterministic iteration and a seeded, snapshot-able PRNG. No map-order dependence anywhere.
- The three tick SLIs, plus consumer lag, plus `docs/specs/slo/tick-health.md`.
- Replay-equality harness: same starting state, same recorded offset ranges → identical State Hash.

### Out of scope
- Command parsing, authorization, and the Gateway — `AW-SRV-003`, `AW-SRV-005`, `AW-SRV-010`. This
  story consumes already-logged, already-typed Commands.
- Event delivery to Sessions — `AW-SRV-011`. This story emits through the seam from `AW-SRV-004`.
- Snapshots — `AW-SRV-006`. Until then a restart replays the log from its beginning, which is correct
  and slow. Snapshots are an RTO optimization over a working recovery path, not the recovery path.
- Multi-process partition assignment. This process is assigned all 64.

## Acceptance criteria

1. **Given** a World loaded from fixture content and an empty Partition **when** the Tick Loop runs
   1000 ticks **then** the tick number advances by exactly 1000 and the State Hash after each tick
   matches a golden sequence.
2. **Given** the same starting state and the same recorded offset ranges **when** the loop is replayed
   in a separate process **then** the final State Hash is identical.
3. **Given** the same starting state and offset ranges **when** replayed on `darwin/arm64` and
   `linux/amd64` **then** the final State Hash is identical on both.
4. **Given** a completed tick **when** its `TickCompleted` record is inspected **then** it names the
   tick, the `state_version`, the exact per-Partition offset range applied, and the State Hash.
5. **Given** a recorded `TickCompleted` sequence **when** the same Commands are replayed with
   *different* broker timing **then** the tick boundaries are taken from the records, not re-derived,
   and the State Hash sequence is identical. This is the test that would fail if boundaries were
   re-decided at replay.
6. **Given** a Tick Rate of `N` Hz **when** the loop runs for 10 seconds of stepped test time **then**
   exactly `10N` ticks have executed. Ticks are never skipped to catch up without incrementing the
   overrun counter.
7. **Given** a tick whose processing exceeds the Tick Budget **when** it completes **then**
   `andara_tick_overruns_total` increments and a `warn` line records the tick and actual duration.
8. **Given** sustained overload where processing takes 3× the Tick Budget **when** the loop runs
   **then** `andara_simulation_lag_seconds` rises monotonically and the loop does not spin, deadlock,
   or drop input.
9. **Given** the broker becomes unreachable **when** the loop runs **then** the tick continues at its
   scheduled rate applying nothing, `andara_tick_input_starved_total` increments, and the World remains
   readable and playable in the sense that Sessions stay connected. It does not crash.
10. **Given** a process killed between applying a Command and checkpointing its offset **when** it
    restarts **then** that Command is re-applied and the resulting State Hash equals the pre-crash
    hash. Re-apply is safe precisely because apply is a pure function.
11. **Given** an effect originating in Zone A targeting an Entity in Zone B **when** it is applied
    **then** it is produced as a Command to Zone B's Partition and resolves on a later tick — never as
    a synchronous call, including when both Zones are owned by this process.
12. **Given** a panic inside one Zone's tick **when** the loop runs **then** the panic is contained,
    the Zone is marked faulted with an Event, `andara_tick_zone_faults_total` increments, offsets for
    that Partition stop advancing, and other Zones continue ticking.
13. **Given** the sim core source **when** the import-boundary lint runs **then** it reports zero
    violations, specifically including `time.Now`, `math/rand` globals, and any Kafka client package.
    The consumer lives outside `server/sim`; the core is handed records.
14. **Given** a running local stack **when** a developer opens the provisioned dashboard **then** tick
    duration p50/p99, overrun rate, lag, and per-Partition consumer lag are plotted with live data.
15. **Given** a shutdown signal **when** the loop receives it **then** it completes the in-flight tick,
    checkpoints its offsets, emits `SimulationStopped`, and exits 0 within the drain timeout;
    **and given** the drain timeout expires **then** it exits 1 naming the tick that would not
    complete.
16. **Given** the default configuration **when** the loop runs **then** the tick interval is 100 ms and the
    overrun threshold is 50 ms (ADR-0008), and the `andara_tick_duration_seconds` histogram has a bucket
    boundary at exactly `0.05` so the SLI is measured rather than interpolated.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation

package sim

type Tick uint64
type Offset int64

// TickClock is the only notion of time inside the sim core. sim never imports
// "time" except for time.Duration.
type TickClock interface {
    Now() Tick
    Sleep(d Duration)
}

// TickInput is what the consumer hands the core. The core does not know Kafka
// exists; it is given ordered records and told which offsets they came from.
type TickInput struct {
    Records  []LoggedCommand           // ordered within each partition
    Applied  map[int32]OffsetRange     // partition -> [from, to] this tick will apply
}

type Engine struct { /* ... */ }

func NewEngine(w *World, cfg Config) *Engine

// Step advances exactly one tick over the given input and returns the Events it
// produced, ending with TickCompleted. Pure with respect to (state, input, seed).
// Called by the loop in production; called directly by replay tests.
func (e *Engine) Step(in TickInput) (Tick, []Event)

// Replay drives Step from recorded TickCompleted boundaries rather than deriving
// them, which is what makes recovery exact (ADR-0002 §4).
func (e *Engine) Replay(boundaries []TickCompleted, records RecordSource) error

func (e *Engine) StateHash() [32]byte

type Config struct {
    TickRate     int
    TickBudget   Duration
    MaxPerTick   int       // records applied per tick; excess deferred, never dropped
    DrainTimeout Duration
    Seed         uint64    // part of snapshot state; part of StateHash
    Partitions   []int32   // assigned to this process
}
```

The consumer, the offset checkpointer, and the producer all live **outside** `server/sim`. The core is
handed `TickInput` and returns Events. That is the seam that keeps `depguard` satisfied and keeps
`Step` a pure function.

*Corrected 2026-09-17 during implementation:*

- **`TickClock` cannot live in `sim`.** `depguard` denies `time` to the core outright — durations enter
  it as Tick counts — so the clock (`tickloop.Clock`: real, and `SteppedClock` for tests) is the
  loop's, and `Step` takes no time at all.
- **`Step` returns a `StepResult`**, not `(Tick, []Event)`: the Events, the cross-Zone Commands to
  produce (AC-11 has to come out of `Step` somewhere), the records it did not apply after a Zone
  fault, the faults, and the `TickCompleted`. It also returns an error — an offset gap or an unowned
  Partition refuses the whole input and moves nothing.
- **`TickInput.Applied` is derived, not supplied.** `Step` reads the range from the records it is
  handed and validates contiguity against the state; a caller stating the range separately is a
  second thing that can disagree.
- **`TickCompleted` carries one offset per Partition — the next-to-read, Kafka's committed-offset
  convention — not a range.** A tick's applied range is between consecutive records, and the record
  can be handed straight to a consumer commit. Every owned Partition is named, moved or not.
- **Events and the `EventSink` are the minimal shape AW-SRV-004 fills in** — `Event{ID, Tick, Zone,
  Type, Envelope}` and `Publish(Event)`; Scope and redaction are 004's. The Event ID counter is in
  the State Hash from this commit, because adding it later is a history migration.
- **The handler seam.** `sim.Config.Handlers` maps a `LoggedCommand` arm to an `Apply`; AW-SRV-003
  registers `look` and `move`. With none registered a record advances its offset and is rejected
  `unsupported_command` — a binary behind its content, not an unknown verb.
- **`sim.tick_budget_ms` must not exceed the tick interval**; a budget past it can never warn before
  lag accrues, which is the budget's whole purpose.
- **`sim.source` (`kafka` | `memory`) was added**, mirroring `auth.store`, so a developer without a
  broker has a ticking World.
- **Recovery at boot replays the recorded boundaries** (`tickloop.Recover`) before going live, so a
  restart is exact rather than a free-running re-batch. A missing boundary — a tick applied but never
  published, which the synchronous publisher lost during a broker outage before it became
  asynchronous — is `ErrBoundaryGap`: refused, with the ticks named. Recovery policy for that case is
  AW-SRV-007's; the operator's path today is a snapshot newer than the gap or an empty log.
- **The per-Zone histogram is `andara_zone_tick_duration_seconds{zone}`**, a separate name: a
  labeled variant of `andara_tick_duration_seconds` would double-count the unlabeled SLI series.
- **`SimulationStopped` carries `event_id` 0** and consumes no ID: it is a lifecycle notification,
  not World history, and a recovered process's next real Event takes the ID it would have taken.
- **`andara_tick_publish_failures_total{kind}`** was added: publishing is asynchronous, so a
  delivery failure has to be counted somewhere the tick is not waiting.
- **`andara-cli world status`** has no story and no RPC; the manual plan reads `/metrics` and the
  dashboard instead, and the command is owed to a CLI story.

### Configuration

| Key | Env | Default | Notes |
|-----|-----|---------|-------|
| `sim.tick_rate` | `ANDARA_TICK_RATE` | `10` | ticks per second (ADR-0008) |
| `sim.tick_budget_ms` | `ANDARA_TICK_BUDGET_MS` | `50` | overrun threshold — **half** the 100 ms interval, so overruns warn before lag accrues (ADR-0008) |
| `sim.max_per_tick` | `ANDARA_MAX_PER_TICK` | `1024` | records applied per tick; excess deferred |
| `sim.drain_timeout_ms` | `ANDARA_DRAIN_TIMEOUT_MS` | `5000` | graceful shutdown budget |
| `sim.seed` | `ANDARA_SIM_SEED` | derived from World state | overriding is a debugging affordance |
| `sim.partitions` | `ANDARA_SIM_PARTITIONS` | `0-63` | assigned Partitions |
| `sim.checkpoint_every_ticks` | `ANDARA_CHECKPOINT_EVERY_TICKS` | `100` | offset commit cadence |

`checkpoint_every_ticks` is the re-apply window: a crash re-applies at most this many ticks of
Commands. Because re-apply is deterministic, that is a startup-cost knob, not a correctness one.

### Error taxonomy

`ErrZoneFaulted` — Command targets a quarantined Zone.
`ErrDrainTimeout` — shutdown exceeded the budget; exit 1.
`ErrOffsetGap` — the consumer observed a gap in a Partition's offsets; refuse to apply and exit 1
rather than silently skip history.

## Data / state impact

Introduces mutable World state and the State Hash every later persistence story asserts against.
Nothing is written to durable storage by this story beyond consumer offsets; snapshots are
`AW-SRV-006`.

World state includes, and `StateHash` covers: the current tick, the PRNG seed and its advanced state,
per-Partition applied offsets, and `state_version`. Omitting any of these makes recovery
non-deterministic in a way invisible until it matters. `state_version` is present from the first commit
even though nothing is serialized yet — adding it later means migrating a live World's history.

## Observability requirements

### Metrics
- `andara_tick_duration_seconds` — histogram. No labels. Buckets place the Tick Budget on a boundary.
- `andara_tick_duration_seconds{zone}` — per-Zone histogram. Cardinality bounded by Zone count. This is
  the metric ADR-0001 requires to see a starving Zone before sharding is needed.
- `andara_ticks_total`, `andara_tick_overruns_total` — counters, no labels.
- `andara_simulation_lag_seconds` — gauge. The player-visible symptom.
- `andara_tick_applied_records_total` — counter, no labels.
- `andara_tick_deferred_records` — gauge. Backlog beyond `max_per_tick`.
- `andara_tick_input_starved_total` — counter. Ticks that applied nothing because input was unavailable.
- `andara_consumer_lag{partition}` — gauge. Cardinality 64, bounded and intentional.
- `andara_checkpoint_age_ticks` — gauge. Directly predicts re-apply cost on restart.
- `andara_tick_zone_faults_total` — counter, label `zone`.

Entity ID, Room ID, Session ID, and Command ID are rejected as labels.

### Logs
- `info` once per second: tick, lag, applied offsets, consumer lag. Not once per tick.
- `warn` per overrun: `tick`, `duration_ms`, `budget_ms`, `zone` when attributable.
- `warn` on input starvation, with the broker error.
- `error` on Zone fault: `tick`, `zone`, `panic`, stack.
Required fields: `ts`, `level`, `msg`, `service`, `env`, `tick`, `trace_id`.

### Traces
- `sim.tick` — span per tick, head-sampled, tail-sampled at 100% on overrun. Attributes: `tick`,
  `record_count`, `event_count`, `overrun`.
- `sim.zone_tick` — child per Zone. Per-Entity spans are explicitly not emitted.

### Alerts
`docs/specs/slo/tick-health.md` already exists, with targets derived from ADR-0008. This story
**validates them against first measurement** and either confirms them or comes back with why not:

- **SLI:** fraction of ticks completing within the Tick Budget.
- **Target / window:** 99.9% over rolling 28 days — ~2,400 overrunning ticks/day at 10 Hz.
- **Error budget and exhaustion policy:** stated in the SLO doc.
- **Alert:** `SimulationLagging` on `andara_simulation_lag_seconds` sustained above threshold. Players
  experience lag, not overruns. Runbook `docs/runbooks/simulation-lagging.md` ships in this story.

No alert on overrun count or consumer lag directly — both are causes. Consumer lag is a dashboard
panel and a diagnostic step inside the runbook.

## Test plan

- **Unit:** stepped-clock tests for schedule adherence, overrun accounting, lag accumulation, deferral,
  drain on shutdown, cross-Zone produce-not-call, Zone panic containment, offset-gap refusal, PRNG
  determinism under identical seeds and divergence under different ones.
- **Integration:** against a throwaway Redpanda — replay equality across two processes (AC-2) and two
  architectures (AC-3); the timing-independence test (AC-5) run with artificially varied fetch batching;
  crash-between-apply-and-checkpoint (AC-10); broker-kill starvation (AC-9); overload asserting
  monotonic lag with no spin. Replay equality runs in CI on every sim-core change.
- **Manual/operator:**
  ```
  make up
  curl -s http://127.0.0.1:8080/metrics | grep -E '^andara_(ticks_total|simulation_lag|consumer_lag|checkpoint_age)'
  ANDARA_TICK_BUDGET_MS=1 make up          # force overruns — needs Commands that cost something (AW-SRV-003)
  docker compose stop redpanda             # expect: starvation counted, schedule kept, no crash
  make measure-tick DURATION=60            # p99 CPU and RSS of the loop over the fixture
  ```

### Verification record (2026-09-17)

- **AC-1** `TestLoop_GoldenHashSequence`: 1,000 ticks over the scripted log against the committed
  golden. **AC-2, AC-5** `TestEngine_ReplayFromBoundaries` (uneven batches replayed from boundaries
  alone) and, on the broker, `TestKafka_ApplyThenReplay` (the broker's own batching) and
  `TestKafka_CrashAndRecover` (a loop killed with `SIGKILL` in a separate process, the next process
  recovering from its boundaries and continuing at the tick after the last one recorded, the whole
  sequence replayed by a third engine). **AC-3** the `determinism` CI job runs the golden and replay
  tests on `ubuntu-latest`, `ubuntu-24.04-arm`, and `macos-latest` — darwin/arm64 for real.
  **AC-4** every boundary's fields asserted in `TestStep_RejectionsAndOffsets` and on the broker.
- **AC-6, AC-16** `TestLoop_ScheduleAdherence`: 100 ticks in ten stepped seconds, `0.05` on a bucket
  boundary. **AC-7, AC-8** `TestLoop_OverrunAndLag`: 3× load, lag monotone over forty ticks, every
  record applied. **AC-9** `TestLoop_Starvation` on the stepped clock, and on the stack: a ten-second
  `docker compose stop redpanda` kept 10 Hz, counted 85 starved ticks, dropped nothing, and the
  restart afterwards replayed every tick including the outage's.
- **AC-10** `TestKafka_CrashAndRecover` (above): the re-applied ticks hash to the recorded values or
  recovery halts. **AC-11** `TestStep_CrossZoneIsProduced`. **AC-12** `TestStep_ZoneFaultIsContained`
  and `TestLoop_ZoneFault`, including that a replayed fault hashes identically. **AC-13** `make lint`
  runs `depguard` over `server/sim` including its tests. **AC-14** every panel's query on
  `andara-tick-health` returned live series from the compose Prometheus. **AC-15**
  `TestLoop_DrainTimeout` names the wedged tick; the process test sees `tick loop draining` and
  `tick loop stopped`.
- **The first measurement:** p99 tick under 1 ms, 10 mCPU, 94 MiB at the idle floor; a burst of
  20,000 rejected Commands at 1,024 per tick stayed under 1 ms per tick. The SLO's targets hold
  with two orders of magnitude of margin there and are to be re-validated on the sizing fixture
  once handlers spend the budget. `measurements.yaml` keeps its placeholder: sizing production on an
  idle loop would be worse than one. What will spend the budget first is not a handler:
  `WorldState.CanonicalBytes` re-serializes every Entity every tick, so the hash is O(Entities) per
  tick, and the sizing fixture's 10,000 Entities will show it. An incremental hash is the fix when
  it does.
- **AC-5's teeth:** the fixture's `look` handler stamps the tick into the Entity it spawns, so the
  same records in a different batching reach a different hash — asserted by the negative in
  `TestEngine_ReplayFromBoundaries`. A replay that re-derived boundaries would fail the positive.
- **AC-9 in CI:** the stack workflow stops Redpanda for eight seconds and asserts the loop kept
  ticking, counted starvation, stayed healthy, and stopped counting once the broker returned.
- **Waiting on other stories:** the timing-independence test with artificially varied fetch batching
  is covered by uneven in-process batches and the broker's own; a fetch-batch knob on the consumer
  is not exposed. Overruns on the stack need Commands that cost something (AW-SRV-003).

## Definition of done

CLAUDE.md §8, plus:
- Replay equality and the timing-independence test both run in CI and gate merges. A determinism
  regression must be impossible to merge.
- `docs/specs/slo/tick-health.md` exists and the alert rule references it.
- `docs/runbooks/simulation-lagging.md` exists and resolves its alert.
- All four tick SLIs plus consumer lag are on the local dashboard without manual configuration.

## Open questions

- **Tick Rate is decided: 10 Hz with a 50 ms budget** (ADR-0008). The remaining work is confirming the
  SLO target against real measurement, which this story's DoD requires.
- ADR-0008 is explicit that combat rounds and every other periodic mechanic are a *number of Ticks*, not
  "every tick". Nothing in this story may hard-code a game mechanic to the tick interval.
- `[ASSUMPTION]` A Zone panic quarantines the Zone rather than crashing the process. Once `AW-SRV-007`
  exists, crash-and-recover becomes a defensible alternative; revisit then.
- `[ASSUMPTION]` `max_per_tick` deferral is FIFO across Partitions round-robin, so one busy Zone cannot
  starve another. Worth confirming against how it feels in play.
- `[ASSUMPTION]` A Zone fault freezes the Zone's whole Partition. Partition→Zone is many-to-one in
  general, so every other Zone on that Partition waits too; with 64 Partitions and a handful of Zones
  they rarely share, but a per-Zone quarantine that lets the Partition's other Zones continue is a
  different rule and Brian's call once AW-SRV-003 makes faults possible.
- `[ASSUMPTION]` The default seed is derived from the World's topology (`sim.DeriveSeed`: the first
  eight bytes of the SHA-256 of `CanonicalBytes(world)`), so two processes loading the same content
  agree without anyone choosing.
- `[ASSUMPTION]` `SimulationStopped` is not World history (event_id 0), so that a recovered process
  reuses no Event ID. AW-SRV-004 owns Event IDs and may want it otherwise.
- **Lost-boundary policy — `[ASSUMPTION]`, and for AW-SRV-007.** A Tick Boundary Record lost during
  an outage makes exact replay past it impossible. franz-go fails everything buffered behind a failed
  record on the same Partition, so an outage longer than the delivery timeout (one minute) would
  have left `…, N, [gap], M, …` on the topic and a World that refuses to boot — worse than the
  synchronous publisher it replaced. The invariant now enforced: **once one boundary is lost, this
  process publishes no more.** It keeps ticking and Events keep flowing (AC-9 holds); the next restart
  recovers exactly to the last delivered boundary and re-batches after it; `Recover` still refuses a
  gap as the backstop. The alternative — exit on a lost boundary so Kubernetes restarts into exact
  recovery — has the cleaner invariant and kills the sim on every long outage. Brian's call; the
  seventy-second outage is the scenario to decide against.
- **For AW-SRV-004:** `sim.Event` and `EventSink` are the minimal shape; Scope, redaction, buffering,
  and the drop rule are yours, on top of them.
