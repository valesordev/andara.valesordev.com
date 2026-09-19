# WorldReadOnly

**Alert:** `max(andara_ingress_degraded) == 1` for 1 m (`AW-INF-005` owns the rule and the SLO).
**Severity:** page. **SLO:** `AW-INF-005`'s Kafka availability SLO, when it lands.
**Ships with:** `AW-SRV-010` (this runbook and the metric); `AW-INF-005` completes it with the
alert, the SLO, and the broker-side mitigations.

## What fired, and what the player is experiencing

`andara-server` cannot reach the Command log. Nothing a player types is accepted: every `Submit`
returns `UNAVAILABLE` at once with reason `world_read_only` and the read-only message. The World is
**read-only, not down** — the tick keeps running, Sessions stay connected, Events keep arriving,
`/readyz` stays 200. A player sees the World move and cannot act in it.

The log's availability bounds the World's by design (ADR-0002): a Command is acknowledged only
when it is durable, so with no broker there is nothing to acknowledge. The degradation is
deliberate, typed, and bounded — there is no queue filling up behind it.

Players whose Submits were in flight when the outage was detected saw something else: every
Submit whose record had already been handed to the producer at that moment is answered
`DEADLINE_EXCEEDED` (reason `produce_deadline`, *outcome unknown*), because the record may have
reached the broker. The ingress drops what the producer still held when it entered the read-only
state, so those Commands land only if the broker had them before the outage; none appears minutes
later. Detection takes at most the probe interval (one second), so that is the window.

## How to confirm

```
curl -s http://<server>:8080/metrics | grep andara_ingress_degraded        # 1 while read-only
kubectl -n andara-<env> logs statefulset/andara | grep 'command log'          # "unreachable" with the broker error; "reachable" on exit
kubectl -n andara-<env> get pods -l app.kubernetes.io/name=redpanda            # or the Redpanda operator's status
rpk cluster health                                                            # from a broker pod
```

Locally: `make up`, `docker compose -f deploy/compose/docker-compose.yaml stop redpanda`, and the
gauge flips within a second; `start redpanda` and it clears within a second of the first
successful ping.

The probe pings a broker once a second whether or not anyone is playing, so the gauge moves
without traffic; a produce failing and a ping then failing flips it sooner.

## How to mitigate

| Symptom | Action |
|---------|--------|
| brokers down or unreachable | bring the broker back; the ingress recovers on its own — no server restart, no client reconnect. Recovery is logged `command log reachable`. |
| brokers up, gauge still 1 | `rpk cluster health` from a broker pod; `min.insync.replicas` unsatisfied looks like this from the producer's side (`acks=all`). `AW-INF-005` owns the replica-side procedure. |
| gauge 0, players still see `UNAVAILABLE` | the reason is different: `in_transit` is a stuck cross-Zone handoff (`AW-SRV-028`), not the log. `andara_ingress_held_intents` and the `command rejected` lines say which. |
| gauge flapping | a partition of the network between server and brokers, or a broker restarting in a loop. The probe clears the state on one successful ping and re-enters it on one failure; look at the broker, not the server. |

Do **not** restart `andara-server` to fix this. It would replay recovery for nothing, and the
ingress is already waiting for exactly the thing you would be restarting it to wait for.

## How to diagnose

1. `andara_ingress_degraded` on every replica, or one? One replica is that pod's network; all is
   the brokers.
2. The `command log unreachable` line's `detail`: `connection refused` is a broker not listening,
   `i/o timeout` is a network path, `NOT_ENOUGH_REPLICAS` is ISR.
3. `andara_ingress_submits_total{outcome="unavailable"}` — how many players hit it.
4. `andara_ingress_produce_duration_seconds` before the flip: a rising p99 was the warning.
5. `andara_ingress_produce_retries_total` — transport retries climbing before the flip say the
   path was flaky first.
6. The tick: `andara_tick_duration_seconds` and `andara_sim_lag_seconds` should be unaffected. If
   they are not, this is not an ingress problem — see `simulation-lagging.md`.

## When to escalate

The server side of this is complete when the broker is back. If the broker cannot be brought
back within the SLO's error budget, that is `AW-INF-005`'s escalation: the Kafka availability SLO
and the replica procedure. If the gauge stays 1 with a healthy cluster, that is a server bug —
open one with the `command log unreachable` line and `rpk cluster health` output.
