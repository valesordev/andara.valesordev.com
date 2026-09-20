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

The server does not yet export Session-seconds directly; the SLI is measured from the pieces that
exist, as the ratio of server-ended streams to stream-seconds served:

```promql
1 - (
  sum(rate(andara_session_egress_drops_total{reason!="client_gone"}[5m]))
  /
  sum(avg_over_time(andara_stream_subscribers[5m]))
)
```

A `client_gone` drop is the client closing its stream or its connection: not a server-side
availability loss, and excluded. `buffer_full` and `draining` are the server ending a stream the
client wanted open — the former because the client could not keep up, which is still the player's
experience of losing the stream; the latter a deploy. Both count.

| | |
|---|---|
| **Target** | 99.5% |
| **Window** | rolling 28 days |
| **Error budget** | **3.4 hours of Session-seconds per 28 days**, spread across every Session |

The budget is in Session-seconds, not wall-clock: a drop that costs one player thirty seconds costs
thirty Session-seconds, whatever the other five hundred players were doing. That is what makes a
per-player failure and a whole-World failure commensurable.

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
sum(rate(andara_session_egress_drops_total{reason=~"buffer_full|draining"}[5m])) * 60
  > 0.01 * sum(avg_over_time(andara_stream_subscribers[5m]))
```

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
- A Session-seconds counter on the server, which would make the SLI direct rather than a ratio.
