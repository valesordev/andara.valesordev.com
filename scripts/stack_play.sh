#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# AW-CLI-004's M1 gate, scripted: `andara-cli play` against the running stack —
# log in, connect over TLS, `look`, `north`, `west`, quit — and assert the
# transcript. Until AW-SRV-014 binds a Character to the Session, every Intent is
# refused before the log with "you are not in the world", so what this asserts
# today is the half of the gate that exists: the Session opens and subscribes,
# each line goes out with a client_ref, the refusal reads as prose with nothing
# about stages or offsets in it, the client leaves cleanly with exit 0 and the
# Session closed, and under --output json stdout is JSON and nothing else.
# AW-SRV-014 carries the other half as an inherited line: the Room, the move, the
# second client seeing the departure (AC-3), and the broker stop (AC-6). The
# server restart (AC-7, AC-8's no_history resync) needs no Character and runs here.
#
# Requires a stack: `make up` first, and `make build`. CI runs it as a step of
# the `stack` workflow.

set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "stack-play: $*" >&2; exit 1; }

[[ -f .local/tls/ca.pem ]] || fail "no .local/tls/ca.pem; run \`make up\` first"
[[ -f .local/cli.yaml ]] || fail "no .local/cli.yaml; run \`make up\` first"
[[ -x bin/andara-cli ]] || fail "no bin/andara-cli; run \`make build\` first"

HTTP_PORT="${ANDARA_HTTP_PORT:-8080}"
METRICS="http://localhost:${HTTP_PORT}/metrics"
OPERATOR="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}"

# An isolated home: the credential this writes must not land in the developer's.
WORK="$(mktemp -d -t stack-play.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
export ANDARA_CONFIG="$PWD/.local/cli.yaml"
export XDG_CONFIG_HOME="$WORK/config"
export XDG_STATE_HOME="$WORK/state"

metric() {
  curl -sf "$METRICS" | awk -v m="$1" '$1 == m { print $2; found = 1 } END { if (!found) print 0 }'
}

echo "stack-play: logging in as ${OPERATOR%%:*} ..."
printf '%s\n' "${OPERATOR#*:}" | bin/andara-cli auth login --username "${OPERATOR%%:*}" --password-stdin >/dev/null \
  || fail "auth login failed"

authz_before="$(metric 'andara_ingress_submits_total{outcome="rejected_authz"}')"
parse_before="$(metric 'andara_ingress_submits_total{outcome="rejected_parse"}')"
closed_before="$(metric 'andara_sessions_total{outcome="closed"}')"

echo "stack-play: look, north, west, frobnicate, quit ..."
OUT="$WORK/transcript.txt"
ERR="$WORK/stderr.txt"
set +e
printf 'look\nnorth\nwest\nfrobnicate\n' | bin/andara-cli play --show-protocol >"$OUT" 2>"$ERR"
code=$?
set -e
sed 's/^/  | /' "$OUT"
[[ "$code" == "0" ]] || { cat "$ERR" >&2; fail "play exited $code, want 0"; }
[[ ! -s "$ERR" ]] || { cat "$ERR" >&2; fail "play wrote to stderr in human mode"; }

# The Session opened over TLS and subscribed (AC-1).
grep -q '^-- Connected to [^ ]\+ as operator (session [0-9a-f]\{32\}, protocol 1)\.$' "$OUT" \
  || fail "no connection notice"
grep -q '» Subscribe session_id=[0-9a-f]\{32\} last_event_id=0 world=false' "$OUT" || fail "no Subscribe"

# Every line went out as typed, each with its own client_ref (AW-SRV-031), the
# automatic look first.
for raw in look north west frobnicate; do
  grep -q "» Submit session_id=[0-9a-f]\{32\} client_ref=[0-9a-f]\{8\}-[0-9]\+ raw=\"$raw\"" "$OUT" \
    || fail "no Submit for $raw"
done
refs="$(grep -o 'client_ref=[0-9a-f]\{8\}-[0-9]\+ raw=' "$OUT" | sort | uniq -d)"
[[ -z "$refs" ]] || fail "a client_ref was reused: $refs"

# The refusals read as prose: what the server said and nothing about where in
# the pipeline it was said (AC-4, AC-5). Protocol lines are indented and begin
# with » or «; the player's lines are everything else.
PLAYER="$WORK/player.txt"
grep -v '^  [»«]' "$OUT" > "$PLAYER" || true
grep -q '^you are not in the world$' "$PLAYER" || fail "the refusal is not in the transcript as the server worded it"
for no in stage offset partition pre_log permission_denied invalid_argument; do
  ! grep -qi "$no" "$PLAYER" || fail "the player's transcript mentions '$no'"
done

# A clean quit closed the Session (AC-11), and the server counted the refusals.
grep -q '« CloseSessionResponse' "$OUT" || fail "the Session was not closed on quit"
closed_after="$(metric 'andara_sessions_total{outcome="closed"}')"
authz_after="$(metric 'andara_ingress_submits_total{outcome="rejected_authz"}')"
parse_after="$(metric 'andara_ingress_submits_total{outcome="rejected_parse"}')"
python3 - "$closed_before" "$closed_after" "$authz_before" "$authz_after" "$parse_before" "$parse_after" <<'PY'
import sys
cb, ca, ab, aa, pb, pa = (float(x) for x in sys.argv[1:])
assert ca >= cb + 1, "sessions closed did not rise: %s -> %s" % (cb, ca)
assert aa >= ab + 3, "rejected_authz did not rise by 3 (look, north, west): %s -> %s" % (ab, aa)
assert pa >= pb + 1, "rejected_parse did not rise (frobnicate): %s -> %s" % (pb, pa)
PY
echo "stack-play: the Session opened, subscribed, submitted, was refused in prose, and closed"

# AC-10: under --output json, stdout is the Event stream as JSON and nothing
# else; the prose is on stderr as structured log lines.
echo "stack-play: --output json ..."
JOUT="$WORK/json-out.txt"
JERR="$WORK/json-err.txt"
printf 'look\nfrobnicate\n' | bin/andara-cli play --output json --log-level info >"$JOUT" 2>"$JERR" \
  || fail "play --output json exited non-zero"
python3 - "$JOUT" "$JERR" <<'PY'
import json, sys
out, err = open(sys.argv[1]).read(), open(sys.argv[2]).read()
for line in out.splitlines():
    if line.strip():
        env = json.loads(line)          # every stdout line is an envelope
        assert isinstance(env, dict), line
msgs = [json.loads(l)["msg"] for l in err.splitlines() if l.strip()]
assert any(m.startswith("Connected to ") for m in msgs), msgs
assert "you are not in the world" in msgs, msgs
assert any(m.startswith("unknown verb") for m in msgs), msgs
for l in err.splitlines():
    rec = json.loads(l)
    for k in ("ts", "level", "msg", "command", "trace_id"):
        assert k in rec, (k, rec)
PY
echo "stack-play: stdout carried only JSON; the prose went to stderr as log lines"

# AC-7 and AC-8 against the real server: restart it under an open session. The
# client announces the loss, reopens a Session within its backoff, subscribes
# again, and — a Session dying with its connection until AW-SRV-014/015 — looks
# again on the new one. Nothing false is printed in between: a refused connect
# during the restart is a debug line, not a notice.
COMPOSE="docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server"
echo "stack-play: restarting andara-server under an open session ..."
ROUT="$WORK/restart-out.txt"
RERR="$WORK/restart-err.txt"
FIFO="$WORK/stdin"
mkfifo "$FIFO"
bin/andara-cli play --show-protocol <"$FIFO" >"$ROUT" 2>"$RERR" &
PLAY=$!
exec 3>"$FIFO"
wait_for() {  # wait_for <pattern> <seconds>
  for _ in $(seq 1 "$(( $2 * 2 ))"); do
    grep -q "$1" "$ROUT" && return 0
    kill -0 "$PLAY" 2>/dev/null || { cat "$RERR" >&2; fail "play exited early"; }
    sleep 0.5
  done
  sed 's/^/  | /' "$ROUT" >&2
  fail "play never showed '$1' ($2s)"
}
wait_for '^-- Connected to ' 20
$COMPOSE restart andara-server >/dev/null 2>&1 || fail "could not restart andara-server"
wait_for '^-- Connection lost; reconnecting\.$' 30
# Twice connected: the second is the new Session, and it looked on its own.
for _ in $(seq 1 120); do
  [[ "$(grep -c '^-- Connected to ' "$ROUT")" -ge 2 ]] && break
  sleep 0.5
done
[[ "$(grep -c '^-- Connected to ' "$ROUT")" -ge 2 ]] || { sed 's/^/  | /' "$ROUT" >&2; fail "no reconnect within 60s"; }
wait_for '» Subscribe session_id=' 5
printf 'look\n' >&3
wait_for 'raw="look"' 10
exec 3>&-
set +e
wait "$PLAY"
rcode=$?
set -e
sed 's/^/  | /' "$ROUT"
[[ "$rcode" == "0" ]] || { cat "$RERR" >&2; fail "play exited $rcode after the restart, want 0"; }
[[ "$(grep -o 'session_id=[0-9a-f]\{32\}' "$ROUT" | sort -u | wc -l)" -ge 2 ]] || fail "the reconnect did not open a new Session"
[[ "$(grep -c '» Submit .*raw="look"' "$ROUT")" -ge 3 ]] || fail "the client did not look again on the new Session"
! grep -q 'Something happened\|not answered\|not sent' "$ROUT" || fail "something false was printed during the restart"
# The stack is back for whatever runs next.
for _ in $(seq 1 60); do
  curl -sf "http://localhost:${HTTP_PORT}/readyz" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://localhost:${HTTP_PORT}/readyz" >/dev/null || fail "andara-server not ready after the restart"
echo "stack-play: the connection loss was announced, a Session reopened within the backoff, and the Room asked for again"

# AC-12: the client says what it speaks. The server's range cannot be moved
# from here; the mismatch itself is unit-tested (TestPlay_VersionMismatch).
bin/andara-cli play --help | grep -q -- '--show-protocol' || fail "play --help lacks --show-protocol"

echo "stack-play: M1 gate — the half that exists before AW-SRV-014 — passes"
