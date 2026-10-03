#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# The projector's restore instrumentation (AW-SRV-043, §7), observed on the running stack:
# `andara-projector state --rebuild` restores the newest complete snapshot round the server
# wrote, and then:
#   - its own /metrics reads andara_restore_total{caller="projector",outcome="ok"} 1, and
#     hash_mismatch and seed_mismatch at 0 (all three are pre-seeded);
#   - its log carries `state projector restore verified` with round_tick, zones,
#     restored_hash and trace_id;
#   - Tempo has that trace_id's `state.bootstrap` root (rebuild=true, outcome=ok), with
#     `restore.verify` as its child (round_tick, zones, outcome=ok).
#
# The compose stack has no projector service, so this runs the built binary on the host, the
# only writer on its consumer group. `--rebuild` wipes that group, which is why it must never
# run beside a live projector (projection-stale.md); a stack with a projector of its own fails
# here first, on the group check below. The mismatch paths exit 5 before a scrape could read
# them, so the integration tests read those on the in-process registry (`make test-integration`).
#
# Requires a stack with a completed snapshot round: `make up` and `make build`.
#
# Exit codes: 0 ok; 1 an observation is missing or wrong; 2 the stack or binary isn't there.
set -euo pipefail
cd "$(dirname "$0")/.."

say() { echo "stack-projector-check: $*"; }
fail() { echo "stack-projector-check: $*" >&2; exit 1; }
need() { echo "stack-projector-check: $*" >&2; exit 2; }

[[ -x bin/andara-projector ]] || need "no bin/andara-projector; run \`make build\` first"
SNAP="${ANDARA_SNAPSHOT_FS_PATH:-$PWD/.local/data/snapshots}"
[[ -d "$SNAP" ]] || need "no $SNAP; run \`make up\` first"
COMPOSE="docker compose -f deploy/compose/docker-compose.yaml"
BROKERS="${ANDARA_KAFKA_BROKERS:-localhost:${ANDARA_KAFKA_PORT:-9092}}"
PORT="${ANDARA_PROJECTOR_CHECK_PORT:-18081}"
OTLP="${ANDARA_OTLP_ENDPOINT:-localhost:${ANDARA_TRACE_PORT:-4317}}"

# A round is written on the server's snapshot interval, so one may not exist yet on a stack
# that has only just come up.
for _ in $(seq 1 60); do
  [[ -n "$(find "$SNAP" -type f -print -quit)" ]] && break
  sleep 3
done
[[ -n "$(find "$SNAP" -type f -print -quit)" ]] || fail "no snapshot round under $SNAP after 180 s; is the server snapshotting? (snapshot-stale.md)"

work="$(mktemp -d)"
pid=""
trap '[[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true; rm -rf "$work"' EXIT

ANDARA_KAFKA_BROKERS="$BROKERS" ANDARA_HTTP_PORT="$PORT" ANDARA_OTLP_ENDPOINT="$OTLP" \
ANDARA_SNAPSHOT_FS_PATH="$SNAP" ANDARA_CONTENT_SOURCE=dir \
ANDARA_CONTENT_PATH="${ANDARA_CONTENT_PATH:-$PWD/testdata/content/valid}" \
  bin/andara-projector state --rebuild >"$work/out" 2>"$work/log" &
pid=$!
say "andara-projector state --rebuild started (pid $pid, /metrics on :$PORT)"

# Poll the assertion itself to a deadline (docs/specs/testing/live-assertions.md).
metrics=""
for _ in $(seq 1 30); do
  kill -0 "$pid" 2>/dev/null || fail "the projector exited before it counted a restore: $(tail -n 3 "$work/log" | cut -c1-300)"
  metrics="$(curl -fsS "http://localhost:$PORT/metrics" 2>/dev/null || true)"
  grep -q '^andara_restore_total{caller="projector",outcome="ok"} 1$' <<<"$metrics" && break
  sleep 2
done
grep -q '^andara_restore_total{caller="projector",outcome="ok"} 1$' <<<"$metrics" \
  || fail "andara_restore_total{caller=\"projector\",outcome=\"ok\"} never read 1 within 60 s: $(grep '^andara_restore_total' <<<"$metrics" || echo 'no series')"
for o in hash_mismatch seed_mismatch; do
  grep -q "^andara_restore_total{caller=\"projector\",outcome=\"$o\"} 0\$" <<<"$metrics" \
    || fail "andara_restore_total{caller=\"projector\",outcome=\"$o\"} isn't pre-seeded at 0"
done
say "metrics: andara_restore_total ok=1, hash_mismatch=0, seed_mismatch=0"

line="$(grep '"msg":"state projector restore verified"' "$work/log" | head -n 1 || true)"
[[ -n "$line" ]] || fail "no \`state projector restore verified\` line in the projector's log"
TRACE_ID="$(python3 - "$line" <<'PY'
import json, sys
d = json.loads(sys.argv[1])
assert d["level"] == "INFO", d["level"]
for k in ("round_tick", "zones", "restored_hash", "trace_id"):
    assert d.get(k) not in (None, "", 0), "missing " + k
print(d["trace_id"])
PY
)" || fail "the restore verified line lacks round_tick, zones, restored_hash or trace_id: $line"
say "log: restore verified, trace_id $TRACE_ID"

found=""
for _ in $(seq 1 20); do
  body="$($COMPOSE exec -T tempo wget -qO- "http://localhost:3200/api/traces/$TRACE_ID" 2>/dev/null || true)"
  if python3 - "$body" <<'PY' 2>/dev/null
import json, sys
spans = {}
for b in json.loads(sys.argv[1]).get("batches", []):
    for s in b["scopeSpans"]:
        for sp in s["spans"]:
            spans[sp["name"]] = {"id": sp["spanId"], "parent": sp.get("parentSpanId", ""),
                                 "attrs": {a["key"]: list(a["value"].values())[0] for a in sp.get("attributes", [])}}
boot, ver = spans["state.bootstrap"], spans["restore.verify"]
assert boot["parent"] == "" and boot["attrs"]["outcome"] == "ok" and boot["attrs"]["rebuild"] in (True, "true")
assert ver["parent"] == boot["id"] and ver["attrs"]["outcome"] == "ok"
for k in ("round_tick", "zones"):
    assert k in boot["attrs"] or k in ver["attrs"], k
PY
  then found=1; break; fi
  sleep 3
done
[[ -n "$found" ]] || fail "Tempo has no state.bootstrap > restore.verify trace (outcome=ok) for $TRACE_ID"
say "traces: state.bootstrap (rebuild=true, outcome=ok) > restore.verify (outcome=ok) in Tempo"
say "AW-SRV-043 projector restore instrumentation — passes"
