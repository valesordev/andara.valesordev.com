# SLO — Session availability

> **Status: target decided by Brian, 2026-09-20 — 99.5 % over 28 days.** `AW-SRV-011` writes it;
> the number was proposed as a player experience target before an engineering one and accepted as
> such. Validation against first measurement is `AW-SRV-014`'s, the first story with Sessions that
> receive Events.

A player experiences the World through a Session and its Event stream. World write availability
(`world-write-availability.md`) is whether they can *act*; this is whether they can *see* — whether
the stream they opened stays open and keeps delivering. The two fail differently: the log going away
makes the World read-only for everyone at once, while a stream drop is one player, one connection,
one bad network — and the whole point of `AW-SRV-011` is that it stays that way.

## SLI

**Definition:** the fraction of Session-seconds during which the Session was connected and its Event
stream was not in a drop state.

A Session is *in a drop state* from the moment the server ends its stream for a reason the client did
not choose (`buffer_full`, `draining`) until the client reopens a stream or the Session ends. The
server exports that as a gauge, `andara_sessions_in_drop_state`, beside the open-stream gauge, and
the SLI is the ratio of the two integrals over the window:

```promql
1 - (
  sum_over_time(andara_sessions_in_drop_state[28d:1m])
  /
  sum_over_time((andara_stream_subscribers + andara_sessions_in_drop_state)[28d:1m])
)
```

Both sides are subqueries at the same explicit step. *(Amended 2026-09-26 at `AW-CLI-007`'s §8, where
the SLI was first measured.)* The first form summed the numerator's raw samples and the denominator's
subquery points. Those are counted at the scrape interval and the rule-evaluation interval, and the
ratio is only in Session-seconds while the two are equal. They are equal on the compose stack (5 s /
5 s), but nothing holds them equal in Grafana Cloud. A shared step makes each point one minute of one
Session, whatever the scrape interval is. A drop shorter than the step can fall between points: the
SLI undercounts sub-minute drops rather than inventing them. That's acceptable for a 28-day, 99.5 %
target, and the `SessionsDroppingAtRate` alert counts drops by the counter, which misses none.

The denominator is every Session-second that had a stream or was waiting to get one back; a
Session that never subscribed is in neither. `client_gone` — the client closing its stream or its
connection — is not a drop state: the player left. `revoked` — the server closing the Session because
its Account was disabled or its roles changed (`AW-SRV-008` AC-12) — is not one either: the player
may not play. `buffer_full` and `draining` are the server ending a stream the client wanted open —
the former because the client could not keep up, which is still the player's experience of losing
the stream; the latter a deploy. Both count, for as long as the Session stays without a stream.

| | |
|---|---|
| **Target** | 99.5% |
| **Window** | rolling 28 days |
| **Error budget** | **0.5 % of the fleet's Session-seconds: 3.36 hours × the average number of concurrent Sessions, per 28 days** |

The budget is in Session-seconds, not wall-clock: a drop that costs one player thirty seconds costs
thirty Session-seconds, whatever the other five hundred players were doing. With 500 concurrent
Sessions the budget is ~1,680 Session-hours; with one, 3.36 hours. That is what makes a per-player
failure and a whole-World failure commensurable.

**What the measurement does not yet see.** The gauge dies with the process, and a deploy's
`draining` drops are not scraped before it exits (the §8 record for `AW-SRV-011` found the counter
missing), so the query does not see a deploy's Session-seconds today. "Deploys are counted" below
holds by policy, not by measurement, until `AW-INF-007` gives the drain a scrape or a pushed final
sample.

## Why 99.5% and not 99.9%

Deploys are a full-World restart until sharding is activated (`AW-INF-007`), and every Session is
ended `draining` at each one. A deploy costs every connected Session the RTO — 60 s — so the budget
in Session-seconds is spent at `sessions × 60` per deploy, exactly proportional to the same deploy
cadence that constrains write availability. The same reasoning applies: 99.5% pre-launch, tightened
at launch when the deploy question is decided deliberately.

A second reason: `AW-SRV-015`'s linkdead grace makes a dropped stream recoverable without losing
the Character, and `AW-SRV-011`'s resume window makes it recoverable without losing Events. Until
those are measured, a stream drop is worse for the player than it will be, and a tight target now
would be a target on a number that is about to change meaning.

## Alert

**`SessionsDroppingAtRate`** — server-ended streams (`buffer_full` and `draining`) exceed **1% of
open streams per minute**, sustained for 5 minutes:

```promql
sum by (namespace) (rate(andara_session_egress_drops_total{reason=~"buffer_full|draining"}[5m])) * 60
  > 0.01 * sum by (namespace) (avg_over_time(andara_stream_subscribers[5m]))
```

`by (namespace)` since `AW-INF-008`: `dev` and `prod` share one Grafana Cloud tenant, and a ratio
summed across both would let one environment's streams dilute the other's. In compose there is no
`namespace` and it is the plain sum.

A burn at that rate for 5 minutes is 5% of the fleet's Session-seconds — a quarter of the
28-day budget in an afternoon if it continued. `draining` fires only during a deploy that is not
completing (a stuck drain), because a completed drain is a one-minute spike below the `for`.

Runbook: `docs/runbooks/sessions-dropping.md`. Diagnostic order:

1. Which reason. `buffer_full` is clients not keeping up; `draining` is a drain in progress.
2. How many Sessions, and whether they share a client build, a region, or a Room.
3. `andara_stream_buffer_depth` — is the whole population trailing (the server is producing more
   than the wire carries: a Room event storm) or a tail?
4. Whether the tick is affected. It must not be; if `andara_tick_duration_seconds` moved with the
   drops, that is a bug in the fan-out, not a load problem.

## What the SLI does not see

- A client that reads its stream but renders nothing. The server cannot tell.
- A Session that never subscribes. It is connected and has no stream to drop; it does not count
  toward the denominator either.
- A revoked Session. Its stream ends with `SubscriberDropped{reason=revoked}` and
  `PERMISSION_DENIED`; the drop is counted under `revoked` and is not unavailability.
- Resyncs. A `Resync` frame is a stream that stayed up and delivered a typed gap — the honest
  outcome, and a separate signal (`andara_stream_resyncs_total`) that says `egress.resume_window`
  is too small, not that availability was lost.

## Excluded from the budget

Nothing. Deploys are counted, as in `world-write-availability.md`, and for the same reason: a
player disconnected by a deploy is as disconnected as one dropped by a full buffer.

## Error budget exhaustion policy

1. Deploys pause except for fixes to whatever exhausted the budget.
2. If the burn is `draining` — deploy-driven — the same conversation as write availability: ADR-0001's
   sharding activation, with deploy cadence as the argument.
3. If the burn is `buffer_full`, `egress.buffer` is not raised as the first response. A buffer that
   fills is a client that cannot keep up with what it is being sent; the questions are what is
   being sent (an Event storm in one Room is a content or mechanics problem) and what the client
   does with it (`AW-CLI-004`). Raising the buffer defers the drop and grows the memory per Session.

## What would change this document

- `AW-SRV-015` landing: linkdead grace makes the Session outlive the stream, and the SLI should
  then count a Session in its grace window as connected-but-not-delivering rather than dropped.
- `AW-INF-007` making a drain's last samples visible, which would make "deploys are counted" a
  measurement.
