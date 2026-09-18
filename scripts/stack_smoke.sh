#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# AW-SRV-005 §8: "Instrumentation from §7 is emitting and verified against a real
# backend, not just registered." The gateway's Go tests read an in-process registry,
# which proves the counters move; it does not prove anything scrapes them. This opens
# a Session on the running server over TLS, then asks Prometheus what it saw.
#
# Requires a stack: `make up` first. CI runs it as a step of the `stack` workflow.

set -euo pipefail
cd "$(dirname "$0")/.."

GO="${GO:-go}"
METRICS_PORT="${ANDARA_METRICS_PORT:-9090}"
PROM="http://localhost:${METRICS_PORT}"

fail() { echo "stack-smoke: $*" >&2; exit 1; }

[[ -f .local/tls/ca.pem ]] || fail "no .local/tls/ca.pem; run \`make up\` first"

# Open a Session and reject an out-of-range version, against the running server.
# The assertions live in Go rather than here because they are about the Protocol,
# and the generated client is the only thing that speaks it (there is no grpcurl
# in `make bootstrap`, and a hand-rolled HTTP/2 frame would be testing the wrong thing).
# Absolute, because `go test` runs each test in its own package directory and a path
# relative to the repo root would resolve under internal/smoke/.
export ANDARA_TLS_CA_FILE="${ANDARA_TLS_CA_FILE:-$PWD/.local/tls/ca.pem}"
export ANDARA_SMOKE_ADDR="${ANDARA_SMOKE_ADDR:-localhost:${ANDARA_GRPC_PORT:-8443}}"
# AW-SRV-008: the bootstrap operator `make up` configured, and the broker the audit
# record is read back from.
export ANDARA_SMOKE_OPERATOR="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}"
export ANDARA_KAFKA_BROKERS="${ANDARA_KAFKA_BROKERS:-localhost:${ANDARA_KAFKA_PORT:-9092}}"

echo "stack-smoke: opening a Session over TLS ..."
SMOKE_OUT="$(mktemp -t stack-smoke.XXXXXX)"
$GO test -tags smoke -count=1 -v ./internal/smoke/ | tee "$SMOKE_OUT" || fail "the live Session tests failed"
SESSION_ID="$(grep -o 'session_id=[0-9a-f]\{32\}' "$SMOKE_OUT" | head -1 | cut -d= -f2)"
[[ -n "$SESSION_ID" ]] || fail "the smoke test did not print a session_id"

# Prometheus scrapes every 15s by default; the Session above may not be in a
# completed scrape yet. Poll rather than sleep a fixed amount.
query() {
  curl -sf --get "$PROM/api/v1/query" --data-urlencode "query=$1" \
    | "${PY:-python3}" -c 'import json,sys; print(len(json.load(sys.stdin)["data"]["result"]))'
}

# Wait on a series the Session above produced, not on one that exists at boot:
# andara_sessions_total is pre-seeded with all four outcomes, so it is present in the
# first scrape and waiting for it would prove only that Prometheus is running.
# andara_grpc_requests_total has no series until an RPC completes.
echo "stack-smoke: waiting for Prometheus to scrape the gateway ..."
scraped=0
for _ in $(seq 1 30); do
  if [[ "$(query 'andara_grpc_requests_total' 2>/dev/null || echo 0)" != "0" ]]; then
    scraped=1
    break
  fi
  sleep 3
done
[[ "$scraped" == "1" ]] || fail "Prometheus never scraped andara_grpc_requests_total (90s)"

# The target must be up, not merely configured: a scrape config pointing at a dead
# endpoint looks identical to a working one until you ask.
health="$(curl -sf "$PROM/api/v1/targets" \
  | "${PY:-python3}" -c 'import json,sys
t = {x["labels"]["job"]: x["health"] for x in json.load(sys.stdin)["data"]["activeTargets"]}
print(t.get("andara-server", "absent"))')"
[[ "$health" == "up" ]] || fail "Prometheus reports the andara-server target as '$health'"

# Every instrument AW-SRV-005 §7 names, queried through Prometheus rather than read
# off the process. A name that changes without the story changing fails here.
for metric in andara_sessions_active andara_sessions_total andara_session_duration_seconds_count \
              andara_grpc_requests_total andara_grpc_request_duration_seconds_count; do
  n="$(query "$metric")"
  [[ "$n" -gt 0 ]] || fail "$metric has no series in Prometheus"
  printf '  %-45s %s series\n' "$metric" "$n"
done

# The rejected Session must be counted under its own outcome, and the bounded enum
# must be fully populated so a rate() over an outcome that has not happened is zero
# rather than absent.
for outcome in closed dropped rejected_version rejected_auth; do
  n="$(query "andara_sessions_total{outcome=\"$outcome\"}")"
  [[ "$n" -gt 0 ]] || fail "andara_sessions_total{outcome=\"$outcome\"} is absent; the enum is not pre-seeded"
done

rejected="$(curl -sf --get "$PROM/api/v1/query" \
  --data-urlencode 'query=andara_sessions_total{outcome="rejected_version"}' \
  | "${PY:-python3}" -c 'import json,sys
r = json.load(sys.stdin)["data"]["result"]
print(r[0]["value"][1] if r else "0")')"
[[ "${rejected%.*}" -ge 1 ]] || fail "the out-of-range Session was not counted as rejected_version"

echo "stack-smoke: a Session opened over TLS and Prometheus counted it"

# AW-SRV-024 AC-1 and AC-2: the same `session opened` line stderr carries is in Loki
# within 15 s, with the same values, and its trace_id resolves in Tempo to the
# OpenSession trace. This replaces the synthetic push AW-INF-002 AC-7 was recorded against.
LOKI="http://localhost:${ANDARA_LOKI_PORT:-3100}"
echo "stack-smoke: waiting for Loki to carry session $SESSION_ID ..."
LINE=""
for _ in $(seq 1 15); do
  LINE="$(curl -sf --get "$LOKI/loki/api/v1/query_range" \
    --data-urlencode "query={service_name=\"andara-server\"} | session_id=\"$SESSION_ID\"" \
    --data-urlencode 'since=10m' \
    | "${PY:-python3}" -c 'import json,sys
for s in json.load(sys.stdin)["data"]["result"]:
    for v in s["values"]:
        if v[1] == "session opened":
            print(json.dumps(s["stream"])); sys.exit(0)' 2>/dev/null || true)"
  [[ -n "$LINE" ]] && break
  sleep 1
done
[[ -n "$LINE" ]] || fail "Loki never carried the session opened line for $SESSION_ID (15s)"

# The stderr line for the same Session, from the container.
STDERR_LINE="$(docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server logs --no-log-prefix andara-server 2>/dev/null \
  | grep "\"session_id\":\"$SESSION_ID\"" | grep '"msg":"session opened"' | head -1)"
[[ -n "$STDERR_LINE" ]] || fail "no session opened line on stderr for $SESSION_ID"

TRACE_ID="$("${PY:-python3}" - "$LINE" "$STDERR_LINE" <<'PY'
import json, sys
loki, stderr = json.loads(sys.argv[1]), json.loads(sys.argv[2])
# Loki carries the message as the body and the level as severity_text; every other
# field is structured metadata under its own name.
for k in ("session_id", "trace_id", "client_name"):
    assert loki.get(k) == str(stderr[k]), "%s: loki=%r stderr=%r" % (k, loki.get(k), stderr[k])
assert loki.get("severity_text") == stderr["level"], "level: loki=%r stderr=%r" % (loki.get("severity_text"), stderr["level"])
print(loki["trace_id"])
PY
)" || fail "the Loki record and the stderr line disagree"
echo "  session opened line in Loki matches stderr (session_id, trace_id, client_name, level)"

echo "stack-smoke: following the trace link $TRACE_ID into Tempo ..."
found=0
for _ in $(seq 1 20); do
  if docker compose -f deploy/compose/docker-compose.yaml exec -T tempo \
       wget -qO- "http://localhost:3200/api/traces/$TRACE_ID" 2>/dev/null | grep -q 'andara.game.v1.Game/OpenSession'; then
    found=1; break
  fi
  sleep 3
done
[[ "$found" == "1" ]] || fail "Tempo has no OpenSession trace for $TRACE_ID"
echo "stack-smoke: the log line's trace_id resolves to the OpenSession trace in Tempo"
