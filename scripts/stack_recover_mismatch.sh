#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# RecoveryStateMismatch observed firing on the local stack (AW-SRV-007 §8, AC-14).
#
# A recovery that refuses its round holds /metrics and /livez up for recovery.mismatch_linger
# (compose: 60 s) with andara_recovery_state_hash_match at 0, so Prometheus can scrape the 0 and
# the alert can fire. The cluster can't do this (its scrape is Ready-only; AW-INF-009 adds a second
# clause), so this is the one place the rule is seen to fire.
#
# The refusal is made without touching a byte of the snapshot store: the server is restarted with
# another ANDARA_SIM_SEED than the one it runs on, so recovery refuses the newest round with exit 6 and reason=seed (the round's
# recorded_seed differs; docs/runbooks/recovery-state-mismatch.md). That exit lingers and sets the
# gauge to 0 the same way a hash or content mismatch does. A byte-flipped round, re-signed so it
# verifies, needs the Go codec, which this script doesn't carry.
#   1. A complete round exists and RecoveryStateMismatch is loaded, with no series in ALERTS.
#   2. SIGKILL, then start with the other seed. During the linger: /metrics reads the gauge 0,
#      /livez is 200, /readyz isn't, and the error line names reason=seed.
#   3. Prometheus has RecoveryStateMismatch firing.
#   4. The linger ends and the server exits 6 (docker's die event).
#   5. The server is stopped, so nothing restarts it. 25 s on, the target is stale (the gauge has no
#      series) and the alert is still firing: keep_firing_for carries it past the process.
#   6. The server starts with its own seed again, recovers from that round with a matching State
#      Hash, and the stack is left ready.
#
# The alert stays in ALERTS for keep_firing_for (15 min) after step 6, so this runs last in the
# `stack` workflow's recovery steps, and a second run inside that window stops at step 1.
#
# Requires a stack: `make up` first, and `make build`. Exit codes: 0 every step held; 1 an
# assertion failed or a precondition is missing.
#
# Environment: ANDARA_HTTP_PORT, ANDARA_METRICS_PORT, ANDARA_BOOTSTRAP_OPERATOR,
# ANDARA_TLS_CA_FILE, STACK_RECOVER_RTO (seconds the recovered server has to be ready; 120).

set -euo pipefail
cd "$(dirname "$0")/.."

say() { echo "stack-recover-mismatch: $*"; }
[[ -f .local/cli.yaml ]] || { echo "stack-recover-mismatch: no .local/cli.yaml; run \`make up\` first" >&2; exit 1; }
[[ -x bin/andara-cli ]] || { echo "stack-recover-mismatch: no bin/andara-cli; run \`make build\` first" >&2; exit 1; }

HTTP_PORT="${ANDARA_HTTP_PORT:-8080}"
METRICS="http://localhost:${HTTP_PORT}/metrics"
READYZ="http://localhost:${HTTP_PORT}/readyz"
LIVEZ="http://localhost:${HTTP_PORT}/livez"
PROM="http://localhost:${ANDARA_METRICS_PORT:-9090}"
OPERATOR="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}"
# `up` recreates the container, and the keyring it reads is 0600 and the host user's, so the uid
# `make up` exports has to be exported again (compose's own default is the image's user).
export ANDARA_UID="${ANDARA_UID:-$(id -u)}" ANDARA_GID="${ANDARA_GID:-$(id -g)}"
COMPOSE=(docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server)
RTO="${STACK_RECOVER_RTO:-120}"
ORIG_SEED=""   # the running container's configured ANDARA_SIM_SEED, read before the kill
SEED=""        # the temporary one, never equal to it
ALERT=RecoveryStateMismatch

WORK="$(mktemp -d -t stack-recover-mismatch.XXXXXX)"
export ANDARA_CONFIG="$PWD/.local/cli.yaml"
export ANDARA_TLS_CA_FILE="${ANDARA_TLS_CA_FILE:-$PWD/.local/tls/ca.pem}"
export XDG_CONFIG_HOME="$WORK/config"
export XDG_STATE_HOME="$WORK/state"

# A server this run left stopped or on the wrong seed must not stay that way for the steps after it.
TOUCHED=""
cleanup() {
  if [[ -n "$TOUCHED" ]] && ! curl -sf "$READYZ" >/dev/null 2>&1; then
    ANDARA_SIM_SEED="${ORIG_SEED:-0}" "${COMPOSE[@]}" up -d --no-deps andara-server >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
  echo "stack-recover-mismatch: $*" >&2
  echo "--- andara-server, last 50 lines:" >&2
  "${COMPOSE[@]}" logs --no-color --no-log-prefix --tail 50 andara-server >&2 2>&1 || true
  exit 1
}

# metric <name> [label=value ...]: the sum of the samples of <name> whose labels include the
# pairs; empty when the server doesn't answer or the series is absent.
metric() {
  { curl -sf "$METRICS" || true; } | python3 -c '
import re, sys
name, want = sys.argv[1], dict(a.split("=", 1) for a in sys.argv[2:])
total, seen = 0.0, False
for line in sys.stdin:
    m = re.match(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(.*)\})? (\S+)", line)
    if not m or m.group(1) != name:
        continue
    labels = dict(re.findall(r"(\w+)=\"((?:[^\"\\]|\\.)*)\"", m.group(3) or ""))
    if all(labels.get(k) == v for k, v in want.items()):
        total += float(m.group(4)); seen = True
if seen:
    print(int(total) if total == total and abs(total) != float("inf") and total == int(total) else total)
' "$@"
}

# prom <query>: the number of series Prometheus returns for an instant query; empty on an error.
prom() {
  curl -sf --get "$PROM/api/v1/query" --data-urlencode "query=$1" \
    | python3 -c 'import json, sys; d = json.load(sys.stdin); print(len(d["data"]["result"]) if d["status"] == "success" else "")' 2>/dev/null || true
}

# await <seconds> <description> <command...>: poll a command to a deadline.
await() {
  local secs="$1" what="$2"; shift 2
  for _ in $(seq 1 "$(( secs * 2 ))"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  fail "after ${secs}s: $what"
}

gauge_is() { [[ "$(metric andara_recovery_state_hash_match)" == "$1" ]]; }
alert_firing() { [[ "$(prom "ALERTS{alertname=\"$ALERT\",alertstate=\"firing\"}")" -ge 1 ]]; }
target_stale() { [[ "$(prom 'andara_recovery_state_hash_match')" == "0" ]]; }
ready() { curl -sf "$READYZ" >/dev/null 2>&1; }
seed_error_logged() {
  "${COMPOSE[@]}" logs --no-log-prefix --since "$SINCE" andara-server 2>/dev/null \
    | grep 'recovery restore mismatch' | grep '"reason":"seed"' >/dev/null
}
# docker's own record of the deaths since SINCE, which a restart loop never hides. `grep >/dev/null`,
# not `grep -q`: a grep that quits at its match SIGPIPEs the producer, and pipefail reads that as no match.
exit6_seen() {
  docker events --since "$SINCE" --until "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --filter event=die \
    --filter label=com.docker.compose.service=andara-server --format '{{.Actor.Attributes.exitCode}}' | grep -x 6 >/dev/null
}

# 1. Preconditions.
ready || { echo "stack-recover-mismatch: andara-server isn't ready" >&2; exit 1; }
curl -sf "$PROM/-/ready" >/dev/null || { echo "stack-recover-mismatch: Prometheus isn't answering on $PROM" >&2; exit 1; }
rules="$(curl -sf "$PROM/api/v1/rules" | python3 -c '
import json, sys
d = json.load(sys.stdin)
print(sum(1 for g in d["data"]["groups"] for r in g["rules"] if r["name"] == sys.argv[1]))' "$ALERT")"
[[ "$rules" == "1" ]] || fail "Prometheus has $rules rules named $ALERT, want 1 (a long-lived stack needs \`curl -X POST $PROM/-/reload\` after a rules change)"
now="$(prom "ALERTS{alertname=\"$ALERT\"}")"
[[ "$now" == "0" ]] || fail "$ALERT already has $now series in ALERTS, so a firing seen now would prove nothing: wait out keep_firing_for (15m) from an earlier run, or make down"
printf '%s\n' "${OPERATOR#*:}" | bin/andara-cli auth login --username "${OPERATOR%%:*}" --password-stdin >/dev/null || fail "auth login failed"
rounds="$(bin/andara-cli snapshot list)" || fail "andara-cli snapshot list failed"
# Not `snapshot list | awk ... exit`: awk quitting at the first match SIGPIPEs the CLI, and pipefail reads that as a failure.
complete="$(awk 'NR > 1 && $4 == "true" { print $1; exit }' <<<"$rounds")"
[[ -n "$complete" ]] || fail "no complete snapshot round yet; the stack needs one interval of play before this run"
say "newest complete round $complete; $ALERT loaded and absent from ALERTS"

# The seed the stack runs on, as the container is configured (0 derives it): restored exactly at the
# end, and the temporary one is chosen to differ from it. A stack started with its own
# ANDARA_SIM_SEED=1 would otherwise be "restarted" on the seed it already has.
cid="$("${COMPOSE[@]}" ps -q andara-server)" || fail "docker compose ps failed"
[[ -n "$cid" ]] || fail "no andara-server container"
ORIG_SEED="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$cid" | sed -n 's/^ANDARA_SIM_SEED=//p')"
ORIG_SEED="${ORIG_SEED:-0}"
[[ "$ORIG_SEED" =~ ^[0-9]+$ ]] || fail "the container's ANDARA_SIM_SEED is '$ORIG_SEED', not a number"
SEED=1
[[ "$ORIG_SEED" != "$SEED" ]] || SEED=2
say "the server runs on sim.seed ${ORIG_SEED/#0/0 (derived)}; the refusal will use $SEED"

# 2. The refused recovery.
SINCE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
TOUCHED=1
say "SIGKILL, then start with ANDARA_SIM_SEED=$SEED ..."
"${COMPOSE[@]}" kill -s KILL andara-server >/dev/null || fail "docker compose kill failed"
ANDARA_SIM_SEED="$SEED" "${COMPOSE[@]}" up -d --no-deps andara-server >/dev/null 2>&1 || fail "docker compose up with ANDARA_SIM_SEED=$SEED failed"
await 40 "andara_recovery_state_hash_match never read 0 on the server's /metrics" gauge_is 0
curl -sf "$LIVEZ" >/dev/null || fail "/livez isn't 200 during the linger"
! ready || fail "/readyz is 200 during the linger"
await 20 "no error line names the seed mismatch" seed_error_logged
say "the server is up in its linger: gauge 0, /livez 200, /readyz not, error line reason=seed"

# 3. The alert fires.
await 60 "$ALERT is not firing in Prometheus" alert_firing
say "$ALERT firing in Prometheus"

# 4. The exit code.
await 90 "no die event with exit code 6 since the restart" exit6_seen
say "the linger ended and the server exited 6"

# 5. The alert outlives the process: stop the restart loop, let the target go stale.
"${COMPOSE[@]}" stop andara-server >/dev/null || fail "docker compose stop failed"
sleep 25
target_stale || fail "andara_recovery_state_hash_match still has a series in Prometheus 25s after the stop"
alert_firing || fail "$ALERT stopped firing with the target gone; keep_firing_for isn't carrying it"
say "the target is stale and $ALERT is still firing"

# 6. Back to the server's own seed: the same round recovers.
say "starting with the server's own seed ($ORIG_SEED) ..."
ANDARA_SIM_SEED="$ORIG_SEED" "${COMPOSE[@]}" up -d --no-deps andara-server >/dev/null 2>&1 || fail "docker compose up with ANDARA_SIM_SEED=$ORIG_SEED failed"
await "$RTO" "the server is not ready ${RTO}s after the restart with its own seed" ready
await 30 "andara_recovery_state_hash_match is not 1 after the recovery with its own seed" gauge_is 1
say "recovered with a matching State Hash. $ALERT stays in ALERTS for its keep_firing_for (15m)"
say "$ALERT — fired on a refused recovery, outlived the process, cleared by the right seed — passes"
