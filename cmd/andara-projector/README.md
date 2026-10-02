# cmd/andara-projector

`main` for the projections that read the World's history into indexes. Implementation lane
(CLAUDE.md §2). One projection today, `state` (`AW-SRV-019`). `AW-SRV-017` (Redis) and
`AW-SRV-018` (Postgres) add theirs as subcommands of the same binary, which the chart's
`projectors.<name>` Deployments run as `andara-projector <name>`.

Wiring only: configuration, telemetry, content load, the snapshot store, `/livez` `/readyz`
`/metrics`. The projection is `server/projector`.

## `andara-projector state`

A replica of the simulation. It runs `sim.Engine` with the server's own handler table
(`sim.Handlers()`, never its own; the `state-projector-is-a-replica` depguard rule and
`TestReplicaHasNoApplyOfItsOwn` hold that). It replays the Commands on `andara.commands.v1` at the
boundaries the server recorded on `andara.events.v1`, checks its State Hash against every
`TickCompleted`, and writes the aggregates each tick touched to the compacted `andara.state.v1`.

```
make up && make topics-apply
ANDARA_KAFKA_BROKERS=localhost:9092 ANDARA_CONTENT_SOURCE=dir ANDARA_CONTENT_PATH=./testdata/content/valid \
  ./bin/andara-projector state                 # :8080 health and metrics; ready once caught up

./bin/andara-projector state --rebuild        # wipe the group, bootstrap from the newest complete round
./bin/andara-projector state --from-zero      # bootstrap from offset zero even when a round exists
```

It must load the same content as the server and use the same `sim.seed` (0 derives it from the
World, as the server does). In the chart it reads the server's ConfigMap for exactly that reason.

### Content: read-only, and a wait

The projector reads content exactly as the server does, and writes none of it (#326). It runs no
core boot: only the server publishes and activates `andara.core`. Nothing it does writes to
`andara.content.*` or `andara.audit.v1`. It reads the core the store holds, like any Builder pack.

On a store whose Active Pointers name no Zones yet, including an empty one, it waits rather than
exiting. That's the state every new environment starts in, and the server waits in it too
(AW-SRV-042):
- **Up, unready, logged once.** `/livez` 200 and `/readyz` 503, with the server's `warn` line
  once: `waiting for content: no Zones in effect; publish and activate a pack`, with `trace_id`.
- **Reloads on every pointer move.** Each Active Pointer move reloads the content. The first that
  builds a World with Zones logs `content in effect: leaving the wait` at `info`, with `zones` and
  `pack` (`pack@version`), and the projector goes on to bootstrap. It's Ready once caught up, with
  no restart.
- **A `dir` source has nothing to wait on.** One that builds no World still exits `1`.

### Bootstrap

| Situation | What it does |
|-----------|--------------|
| A committed checkpoint at tick C, and the newest complete round is at or before C | Restores the round and replays to C silently: the topic already holds it. Then produces from C+1. |
| No checkpoint (first start, or `--rebuild`), or the round is newer than C | Restores the newest round (tick 0 with no round) and starts reading boundaries after the round's tick, found by binary search over the boundary Partition rather than by reading the history before it. Writes every aggregate, tombstones every key the topic holds that the state does not, commits, and follows. |
| A checkpoint that records an unresolved divergence at T | Exits `2` at once, naming T and both hashes, **whatever rounds exist**. Bootstrapping from a newer round would replay past T and erase the evidence. `--rebuild` clears it, and its `info` line says it discarded the divergence at T. `--rebuild` also clears a checkpoint that doesn't parse, with a `warn` line, without reading it first. |

### Configuration

Beyond the shared keys it reads as the server does (`content.*`, `kafka.brokers`, `sim.seed`,
`snapshot.store` and its `fs_path`/`s3_*`, `http.port`, `telemetry.*` except the service name,
which is always `andara-projector-state`):

| Key | Env | Flag | Default | Notes |
|-----|-----|------|---------|-------|
| `projector.state.batch_ticks` | `ANDARA_PROJECTOR_BATCH_TICKS` | `--batch-ticks` | `10` | ticks replayed between produce flushes and offset commits |
| `projector.state.lag_budget` | `ANDARA_PROJECTOR_LAG_BUDGET` | `--lag-budget` | `5s` | exported as `andara_state_projector_lag_budget_seconds`; `ProjectionStale` fires above it |

Consumer group: `andara-projector-state-<env>`. It is never joined: offsets are committed by
admin call with the tick in each offset's metadata, the way the tick loop checkpoints. A halt on a
divergence commits T−1 with the divergence in the same metadata
(`tick=<T−1>;diverged=<T>:<recorded>:<replayed>`), and that record is what the next start reads.

### Exit codes

| Code | Condition |
|-----:|-----------|
| `0` | clean stop on `SIGTERM`/`SIGINT` |
| `1` | configuration, content, snapshot store, or broker. A store whose pointers name no Zones is a wait, not an exit |
| `2` | digest divergence. The replica's State Hash differs from the recorded one, now or at a tick an earlier run halted on and nobody has cleared with `--rebuild`. `/metrics` stays up for 60 s first, so `StateProjectorDiverged` is scraped. Runbook: `docs/runbooks/state-projector-diverged.md` |
| `3` | log gap: the log no longer holds history the replica needs |
| `4` | a snapshot round or boundary written by a newer `state_version` |
