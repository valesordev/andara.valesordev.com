#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# AW-SRV-026 AC-4, scripted against the running stack: a broker outage longer than the
# delivery timeout loses a Tick Boundary Record, and the server exits into exact recovery
# rather than ticking past a gap.
#   - Redpanda is stopped for STACK_BOUNDARY_OUTAGE seconds (default 90), past the publisher's
#     60 s delivery timeout (tickloop.DeliveryTimeout).
#   - The server logs `tick boundary lost; exiting into recovery` exactly once, and its first
#     death exits 5. While the broker is still down its restarts fail at boot with exit 1, and
#     compose's `restart: on-failure` keeps restarting it.
#   - Once the broker is back, the server recovers to the last delivered boundary: its
#     `recovered from the log` tick is the loss line's `last_delivered_tick`, and
#     `andara_ticks_total` moves again.
#   - The topic is gapless. One more restart that recovers past the loss is the read-back, and what
#     it proves depends on where recovery starts. Recovery refuses a gap (sim.ErrBoundaryGap)
#     in whatever it replays, and `recovered from the log` reports `round_tick` (0 on a cold
#     start), `ticks_replayed` (the tail past the round) and `tick`:
#       * a full replay (`round_tick` absent or 0) replays every boundary, so `ticks_replayed == tick`;
#       * a snapshot recovery replays only (round_tick, tick], so the line must satisfy
#         `round_tick + ticks_replayed == tick`, and the replayed tail covers the loss only if the
#         round precedes it, `round_tick < last_delivered_tick`. A round at or past the loss
#         (one cut after the first recovery, before this restart) proves nothing about the loss,
#         and the step fails loudly and says so instead of passing.
#     It also covers the boundaries published after the first recovery.
#
# The script asserts the loss through its log line, not andara_tick_boundary_lost_total: the
# counter is 0 or 1 in a process that exits seconds later. The §8 record checked the counter
# in Prometheus separately.
#
# Requires a stack: `make up` first. CI runs it as a step of the `stack` workflow, after
# AW-SRV-002's short broker outage (about 30 s at most, well inside the 60 s delivery
# timeout), which must still see no exit.

set -euo pipefail
cd "$(dirname "$0")/.."

HTTP_PORT="${ANDARA_HTTP_PORT:-8080}"
METRICS="http://localhost:${HTTP_PORT}/metrics"
READYZ="http://localhost:${HTTP_PORT}/readyz"
OUTAGE="${STACK_BOUNDARY_OUTAGE:-90}"
COMPOSE=(docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server)
LOSS_LINE='tick boundary lost; exiting into recovery'
RECOVERED_LINE='recovered from the log'

say() { echo "stack-boundary-lost: $*"; }
fail() { echo "stack-boundary-lost: $*" >&2; dump; exit 1; }

EVENTS=""
EVENTS_PID=""
SINCE=""
cleanup() {
  [[ -n "$EVENTS_PID" ]] && kill "$EVENTS_PID" 2>/dev/null || true
  [[ -n "$EVENTS" ]] && rm -f "$EVENTS"
  # Never leave the broker down for the steps after this one.
  "${COMPOSE[@]}" start redpanda >/dev/null 2>&1 || true
}
trap cleanup EXIT

dump() {
  echo "--- container deaths (exit codes) ---" >&2
  [[ -n "$EVENTS" ]] && cat "$EVENTS" >&2 || true
  echo "--- andara-server, since the outage ---" >&2
  [[ -n "$SINCE" ]] && "${COMPOSE[@]}" logs --no-log-prefix --since "$SINCE" andara-server 2>&1 | tail -60 >&2 || true
}

(( OUTAGE > 70 )) || { echo "stack-boundary-lost: STACK_BOUNDARY_OUTAGE=$OUTAGE must exceed the 60 s delivery timeout plus the 10 s drain" >&2; exit 1; }

server_id="$("${COMPOSE[@]}" ps -q andara-server)"
[[ -n "$server_id" ]] || { echo "stack-boundary-lost: andara-server isn't running; run \`make up\` first" >&2; exit 1; }
[[ "$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$server_id")" == on-failure ]] \
  || { echo "stack-boundary-lost: andara-server has no \`restart: on-failure\`; the stack predates AW-SRV-026's compose change, so run \`make up\`" >&2; exit 1; }
curl -sf "$READYZ" >/dev/null || { echo "stack-boundary-lost: andara-server isn't ready" >&2; exit 1; }

ticks() { { curl -sf "$METRICS" 2>/dev/null || true; } | awk '$1 == "andara_ticks_total" { print $2 }'; }
ready() { curl -sf "$READYZ" >/dev/null 2>&1; }
logs_since() { "${COMPOSE[@]}" logs --no-log-prefix --since "$SINCE" andara-server 2>/dev/null || true; }
# field <line-substring> <json-field>: the field from the matching log lines, one per line.
field() {
  logs_since | python3 -c '
import json, sys
want, key = sys.argv[1], sys.argv[2]
for line in sys.stdin:
    try:
        rec = json.loads(line)
    except ValueError:
        continue
    if rec.get("msg") == want and key in rec:
        print(rec[key])
' "$1" "$2"
}

# Every death of the server container from here on, with its exit code. `restart:` hides
# them from `docker inspect`, which shows only the last.
EVENTS="$(mktemp)"
docker events --filter "container=$server_id" --filter event=die --format '{{.Actor.Attributes.exitCode}}' >"$EVENTS" &
EVENTS_PID=$!
SINCE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
sleep 1

before="$(ticks)"
say "server ready at andara_ticks_total=${before}; stopping redpanda for ${OUTAGE}s"
"${COMPOSE[@]}" stop redpanda >/dev/null
stopped_at=$SECONDS

# The loss: polled to a deadline, never read once (live-assertions.md). Delivery timeout
# 60 s, plus up to 10 s while the drain's flush runs out.
lost=""
while (( SECONDS - stopped_at < OUTAGE )); do
  if [[ -s "$EVENTS" ]]; then lost=1; break; fi
  sleep 1
done
[[ -n "$lost" ]] || fail "the server didn't exit within ${OUTAGE}s of the broker going away"
first_exit="$(head -1 "$EVENTS")"
[[ "$first_exit" == 5 ]] || fail "the server's first exit was ${first_exit}, not 5 (a lost boundary)"
say "first exit 5, $(( SECONDS - stopped_at ))s into the outage"

# Hold the broker down for the rest of the outage, so the restart loop is real.
remaining=$(( OUTAGE - (SECONDS - stopped_at) ))
(( remaining > 0 )) && sleep "$remaining"
"${COMPOSE[@]}" start redpanda >/dev/null
say "redpanda started after $(( SECONDS - stopped_at ))s"

# Recovery: ready again, ticking, within a deadline.
deadline=$(( SECONDS + 180 ))
# t1 is the reading that ended the poll, so it's never empty: `ticks` can't fail, and an
# empty t1 would make any later reading count as "moved".
t1=""
until ready && t1="$(ticks)" && [[ -n "$t1" ]]; do
  (( SECONDS < deadline )) || fail "the server didn't recover within 180s of the broker returning"
  sleep 2
done
deadline=$(( SECONDS + 30 ))
until t2="$(ticks)" && [[ -n "$t2" ]] && awk -v a="$t2" -v b="$t1" 'BEGIN { exit !(a > b) }'; do
  (( SECONDS < deadline )) || fail "andara_ticks_total didn't move after recovery (${t1})"
  sleep 1
done

mapfile -t losses < <(field "$LOSS_LINE" last_delivered_tick)
(( ${#losses[@]} == 1 )) || fail "expected one \`${LOSS_LINE}\` line, found ${#losses[@]}"
last_delivered="${losses[0]}"
lost_tick="$(field "$LOSS_LINE" lost_tick | head -1)"
mapfile -t recovered < <(field "$RECOVERED_LINE" tick)
(( ${#recovered[@]} >= 1 )) || fail "no \`${RECOVERED_LINE}\` line after the outage"
[[ "${recovered[-1]}" == "$last_delivered" ]] \
  || fail "recovered to tick ${recovered[-1]}, not the last delivered boundary ${last_delivered}"
mapfile -t exits <"$EVENTS"
for code in "${exits[@]:1}"; do
  [[ "$code" == 1 ]] || fail "a restart during the outage exited ${code}, not 1 (the broker unreachable at boot)"
done
say "lost tick ${lost_tick}; recovered to ${last_delivered}; ${#exits[@]} exit(s): ${exits[*]}; ticking (${t1} -> ${t2})"

# The read-back: one more recovery replays every boundary, including those after the first
# recovery, and refuses a gap. It must land past the loss.
before_restart="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
"${COMPOSE[@]}" restart andara-server >/dev/null
SINCE="$before_restart"
deadline=$(( SECONDS + 180 ))
until ready; do
  if (( SECONDS >= deadline )); then
    # A gap fails the boot (exit 1, then the restart loop), so name it when it's the cause.
    # `logs_since` can't fail (it ends in `|| true`), so a compose EPIPE can't turn this match
    # into a miss under pipefail; grepping to EOF is a second guard.
    if logs_since | grep 'tick boundary gap' >/dev/null; then
      fail "the read-back found a gap in the boundaries"
    fi
    fail "the server didn't come back from the read-back restart within 180s"
  fi
  sleep 2
done
# readback:begin (scripts/tests/test_stack_boundary_lost_readback.py runs this block)
again="$(field "$RECOVERED_LINE" tick | tail -1)"
replayed="$(field "$RECOVERED_LINE" ticks_replayed | tail -1)"
round="$(field "$RECOVERED_LINE" round_tick | tail -1)"
round="${round:-0}"
[[ -n "$again" && -n "$replayed" ]] || fail "the read-back restart logged no \`${RECOVERED_LINE}\` line"
(( round + replayed == again )) \
  || fail "the read-back's \`${RECOVERED_LINE}\` line is inconsistent: round_tick ${round} + ticks_replayed ${replayed} != tick ${again}"
# With no round the sum above is `ticks_replayed == tick`: a full replay, which crosses the loss, and
# `round` is 0, so the next line holds. A snapshot recovery's replayed tail is (round_tick, tick], so
# it crosses the loss only if the round precedes it. snapshot.interval can cut a round between the
# first recovery and this restart; then there is nothing to read back across the loss, and saying so
# beats passing.
(( round < last_delivered )) \
  || fail "the read-back restored the round at tick ${round}, at or past the loss at ${last_delivered}, so the replayed tail (${round}, ${again}] doesn't cross it: nothing read the topic back across the loss (a round was cut after the first recovery; see snapshot.interval)"
awk -v a="$again" -v b="$last_delivered" 'BEGIN { exit !(a > b) }' \
  || fail "the read-back recovered to tick ${again}, not past the loss at ${last_delivered}"
# readback:end
say "read-back recovered to tick ${again}: the boundaries are gapless across the loss"
say "AW-SRV-026 AC-4 — exited 5 on the lost boundary, recovered to it, gapless — passes"
