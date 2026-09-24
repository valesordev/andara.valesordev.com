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

### Bootstrap

| Situation | What it does |
|-----------|--------------|
| A committed checkpoint at tick C, and the newest complete round is at or before C | Restores the round and replays to C silently: the topic already holds it. Then produces from C+1. |
| No checkpoint (first start, or `--rebuild`), or the round is newer than C | Restores the newest round (tick 0 with no round), writes every aggregate, tombstones every key the topic holds that the state does not, commits, and follows. |

### Configuration

Beyond the shared keys it reads as the server does (`content.*`, `kafka.brokers`, `sim.seed`,
`snapshot.store` and its `fs_path`/`s3_*`, `http.port`, `telemetry.*` except the service name,
which is always `andara-projector-state`):

| Key | Env | Flag | Default | Notes |
|-----|-----|------|---------|-------|
| `projector.state.batch_ticks` | `ANDARA_PROJECTOR_BATCH_TICKS` | `--batch-ticks` | `10` | ticks replayed between produce flushes and offset commits |
| `projector.state.lag_budget` | `ANDARA_PROJECTOR_LAG_BUDGET` | `--lag-budget` | `5s` | exported as `andara_state_projector_lag_budget_seconds`; `ProjectionStale` fires above it |

Consumer group: `andara-projector-state-<env>`. It is never joined: offsets are committed by
admin call with the tick in each offset's metadata, the way the tick loop checkpoints.

### Exit codes

| Code | Condition |
|-----:|-----------|
| `0` | clean stop on `SIGTERM`/`SIGINT` |
| `1` | configuration, content, snapshot store, or broker |
| `2` | digest divergence. The replica's State Hash differs from the recorded one. `/metrics` stays up for 60 s first, so `StateProjectorDiverged` is scraped. Runbook: `docs/runbooks/state-projector-diverged.md` |
| `3` | log gap: the log no longer holds history the replica needs |
| `4` | a snapshot round or boundary written by a newer `state_version` |
