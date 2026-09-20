# SessionsDroppingAtRate

**Alert:** server-ended Event streams (`buffer_full`, `draining`) above 1% of open streams per
minute for 5 m. **Severity:** page. **SLO:** `docs/specs/slo/session-availability.md`.
**Ships with:** `AW-SRV-011`.

## What fired, and what the player is experiencing

The server is ending players' Event streams faster than the budget allows. A player whose stream
was ended sees their client stop receiving the World — no arrivals, no departures, no answer to
their last command — until the client reopens the stream. A client built to `AW-CLI-004` does that
on its own and asks to resume from the last Event it saw; if the server still holds those Events
the player sees a pause, and if it does not the player sees a `Resync` and their client re-reads
the Room.

Two reasons end a stream from the server's side, and they mean different things:

| `reason` | What it means |
|----------|---------------|
| `buffer_full` | the client trailed the World by more than `egress.buffer` Events and the server ended the stream rather than skip an Event or hold more memory for the Session. The Session survives; the client reopens. |
| `draining` | the server is shutting down and ended every stream `UNAVAILABLE` (`server draining`). Expected once per deploy, for the drain's duration; sustained, the drain is stuck. |

`client_gone` — the client closing its stream or dropping its connection — is not counted by this
alert. It is the player leaving.

The tick is not affected by any of this, by construction: the fan-out is one enqueue from the tick
and every buffer and every write sits on the far side. If `andara_tick_duration_seconds` moved with
the drops, that is a bug, and the first escalation below.

## How to confirm

```
curl -s http://<server>:8080/metrics | grep -E 'andara_session_egress_drops_total|andara_stream_subscribers'
kubectl -n andara-<env> logs statefulset/andara | grep 'stream ended'        # warn, one per drop: session_id, buffered, last_sent, tick
kubectl -n andara-<env> logs statefulset/andara | grep 'closing the connection'   # the escalation: a client not reading its socket
```

Locally: `make up`, open two `andara-cli play` terminals in one Room, `SIGSTOP` one of them and keep
acting in the other. The stopped one is ended `buffer_full` once it trails by `egress.buffer`; the
other never notices. `SIGCONT` and the client reopens its stream.

## How to mitigate

| Symptom | Action |
|---------|--------|
| `reason="draining"`, a deploy in progress | wait for the drain (`grpc.drain_timeout`, 15 s default). Past it, the pod is being force-closed; see `server-crashlooping.md` if it restarts in a loop. |
| `reason="draining"`, no deploy | something is sending the server SIGTERM — a node drain, an OOM kill, a liveness probe failing. `kubectl describe pod`. |
| `reason="buffer_full"`, many Sessions at once | the World is producing more than the wire carries: an Event storm in one Room (a script, a fight with many participants, a builder's mass change). Find the Room from the affected Sessions' Characters; stop the source. Do not raise `egress.buffer` — it defers the drop and grows memory per Session. |
| `reason="buffer_full"`, a few Sessions, repeatedly the same | those clients cannot keep up: a slow link, a client stuck rendering, a client that opened the stream and stopped reading. Nothing server-side; `AW-CLI-004` owns client behavior. |
| `closing the connection` lines | clients that stopped reading their socket entirely — the reset could not reach them and their connection was closed after `egress.heartbeat_interval`. Their Sessions are torn down; they reconnect. Normal for pulled cables; a burst of them is a network path. |
| `andara_stream_resyncs_total` rising with the drops | clients are reopening but cannot resume: `egress.resume_window` is too small for the drop-to-reopen gap at the current Event rate. Raising the window is the right lever here (it is a count of retained Events; memory per Session is `window × envelope`). |

## How to diagnose

1. `sum by (reason) (rate(andara_session_egress_drops_total[5m]))` — which reason.
2. `andara_stream_buffer_depth` — a histogram of how far streams trail when an Event is appended
   for them. The whole population shifting right is an Event-rate problem; a tail is a client
   problem.
3. `andara_events_emitted_total` by `type`, and `andara_event_fanout_duration_seconds` — is the
   World emitting unusually? A fan-out that got slower with the same Event rate is subscriber
   count.
4. The `stream ended` lines: `session_id` → the Session's Character and Room. Many Sessions in one
   Room is the storm case.
5. `andara_tick_duration_seconds` and `andara_simulation_lag_seconds` — unchanged, or this is not a
   stream problem.
6. `andara_subscriber_drops_total{reason="buffer_full"}` — the *fan-out's* drop, which means the
   egress's own pump goroutine was starved. Above zero is a process problem (CPU starvation, a
   stop-the-world pause), not a client one.

## When to escalate

- The tick moved with the drops: the fan-out is on the tick's path. Engineering, now.
- `andara_subscriber_drops_total{reason="buffer_full"}` above zero: the process is starved.
- `draining` sustained past `grpc.drain_timeout` with no deploy: the process is being killed from
  outside.
