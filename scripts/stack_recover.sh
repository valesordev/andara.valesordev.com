#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# The M2 gate (AW-INF-032), scripted against the running stack: "`kill -9` the server; the
# World returns within 120 s with a matching State Hash, and every linkdead Character rebinds
# rather than despawning."
#   - Two player Accounts each create a Character. Both spawn in Purgatory (AW-INF-024). A walks
#     `out` to the Market Plaza (Room 1); B stays where it spawned and watches.
#   - The script waits for a complete snapshot round newer than that move, R, so recovery starts
#     from a snapshot and not from offset zero.
#   - A then walks `north` to the Town Hall (Room 2), after R and before the kill. That move is in
#     the log tail only, so A ending in the Town Hall proves the tail was replayed.
#   - SIGKILL to the compose andara-server, then `start`, both here. /readyz must answer within
#     STACK_RECOVER_RTO seconds of the kill (default 120).
#   - The server's /metrics: andara_recovery_state_hash_match is 1 and andara_recovery_round_tick
#     is R. A larger round_tick means a round completed after the tail move, and the run proves
#     nothing about the tail: it exits 1 asking for a rerun.
#   - A's and B's `play` clients reconnect on their own. Neither transcript shows the
#     already-live protocol reason or the waiting line, A's `look` reads the Town Hall, B's reads
#     Purgatory, and neither reads a despawn line.
#   - Both quit cleanly and `character list` shows each dormant where it was before the kill.
#
# Creates two Accounts and two Characters per run, with random suffixes, as stack_linkdead.sh
# does. `make down VOLUMES=1` clears them. The stack is left running and ready.
#
# Requires a stack: `make up` first, and `make build`. CI runs it as a step of the `stack`
# workflow, after `make stack-linkdead`.
#
# Exit codes: 0 the gate held; 1 an assertion failed, a precondition is missing, or the run was
# inconclusive (a round completed after the tail move).
#
# Environment: ANDARA_HTTP_PORT, ANDARA_BOOTSTRAP_OPERATOR, ANDARA_TLS_CA_FILE,
# ANDARA_SNAPSHOT_INTERVAL (read to size the AC-2 deadline; the compose server's own is 60s) and
# STACK_RECOVER_RTO (seconds, default 120; Phase 1 exit lowers it to 60 in the Makefile).

set -euo pipefail
cd "$(dirname "$0")/.."

[[ -f .local/tls/ca.pem ]] || { echo "stack-recover: no .local/tls/ca.pem; run \`make up\` first" >&2; exit 1; }
[[ -f .local/cli.yaml ]] || { echo "stack-recover: no .local/cli.yaml; run \`make up\` first" >&2; exit 1; }
[[ -x bin/andara-cli ]] || { echo "stack-recover: no bin/andara-cli; run \`make build\` first" >&2; exit 1; }

HTTP_PORT="${ANDARA_HTTP_PORT:-8080}"
METRICS="http://localhost:${HTTP_PORT}/metrics"
READYZ="http://localhost:${HTTP_PORT}/readyz"
OPERATOR="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}"
COMPOSE=(docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server)
RTO="${STACK_RECOVER_RTO:-120}"
[[ "$RTO" =~ ^[0-9]+$ && "$RTO" -gt 0 ]] || { echo "stack-recover: STACK_RECOVER_RTO=$RTO is not a whole number of seconds above 0" >&2; exit 1; }

# A Go duration (`500ms`, `1m30s`), rounded up to whole seconds.
duration_s() {
  python3 - "$1" <<'PY'
import math, re, sys
d = sys.argv[1].strip()
units = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1, "m": 60, "h": 3600}
parts = re.findall(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h)", d)
if d == "0":
    print(0)
elif not parts or "".join(n + u for n, u in parts) != d.lstrip("+"):
    sys.exit("stack-recover: %r is not a Go duration" % d)
else:
    print(math.ceil(sum(float(n) * units[u] for n, u in parts)))
PY
}
INTERVAL_S="$(duration_s "${ANDARA_SNAPSHOT_INTERVAL:-60s}")" || exit 1
(( INTERVAL_S > 0 )) || { echo "stack-recover: snapshot.interval is 0, so no round is ever cut and there is nothing to recover from" >&2; exit 1; }
ROUND_DEADLINE=$(( INTERVAL_S + 30 ))

# An isolated home: the credentials this writes must not land in the developer's.
WORK="$(mktemp -d -t stack-recover.XXXXXX)"
export ANDARA_CONFIG="$PWD/.local/cli.yaml"
export ANDARA_TLS_CA_FILE="${ANDARA_TLS_CA_FILE:-$PWD/.local/tls/ca.pem}"
export XDG_CONFIG_HOME="$WORK/config"
export XDG_STATE_HOME="$WORK/state"

APID=""; BPID=""
AOUT="$WORK/a-out.txt"; BOUT="$WORK/b-out.txt"
touch "$AOUT" "$BOUT"

# No andara-cli child outlives the script, whichever way it exits.
cleanup() {
  for p in $APID $BPID; do kill -9 "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# A server a failed run left stopped must not stay that way for the steps after it.
SERVER_KILLED=""
restart_if_killed() {
  if [[ -n "$SERVER_KILLED" ]] && ! curl -sf "$READYZ" >/dev/null 2>&1; then
    "${COMPOSE[@]}" start andara-server >/dev/null 2>&1 || true
  fi
}

fail() {
  echo "stack-recover: $*" >&2
  echo "--- A:" >&2; sed 's/^/  A| /' "$AOUT" >&2
  echo "--- B, the bystander:" >&2; sed 's/^/  B| /' "$BOUT" >&2
  echo "--- andara-server, last 50 lines:" >&2
  "${COMPOSE[@]}" logs --no-color --no-log-prefix --tail 50 andara-server >&2 2>&1 || true
  restart_if_killed
  exit 1
}

# metric <name> [label=value ...]: the sum of every sample of <name> whose labels include all
# the given pairs, in any order; empty when the server doesn't answer or the series is absent.
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
    print(int(total) if total == int(total) else total)
' "$@"
}

# poll <file> <pattern> <seconds> <pid>: poll to a deadline for a line in a transcript
# (docs/specs/testing/live-assertions.md rule 1). Fails early if the play writing it has exited.
poll() {
  local file="$1" pat="$2" secs="$3" pid="${4:-}"
  for _ in $(seq 1 "$(( secs * 2 ))"); do
    grep -q -- "$pat" "$file" && return 0
    [[ -z "$pid" ]] || kill -0 "$pid" 2>/dev/null || { grep -q -- "$pat" "$file" && return 0; return 1; }
    sleep 0.5
  done
  grep -q -- "$pat" "$file"
}

# poll_after <file> <line> <pattern> <seconds> <pid>: like poll, but only lines after <line> count.
poll_after() {
  local file="$1" after="$2" pat="$3" secs="$4" pid="${5:-}"
  for _ in $(seq 1 "$(( secs * 2 ))"); do
    tail -n +"$(( after + 1 ))" "$file" | grep -q -- "$pat" && return 0
    [[ -z "$pid" ]] || kill -0 "$pid" 2>/dev/null || { tail -n +"$(( after + 1 ))" "$file" | grep -q -- "$pat" && return 0; return 1; }
    sleep 0.5
  done
  tail -n +"$(( after + 1 ))" "$file" | grep -q -- "$pat"
}

# newest_round: the tick of the newest round `snapshot list` calls complete, or 0 when none is.
# The table's first column is the tick, newest first; its fourth is COMPLETE.
newest_round() {
  bin/andara-cli snapshot list 2>/dev/null | awk 'NR > 1 && $4 == "true" { print $1; exit }' || true
}

# state_and_room <name> <list>: the Character's STATE and ROOM columns from `character list`.
state_and_room() { awk -v n="$1" '$1 == n { print $2, $3 }' <<<"$2"; }

echo "stack-recover: logging in as ${OPERATOR%%:*} ..."
curl -sf "$READYZ" >/dev/null || { echo "stack-recover: andara-server isn't ready" >&2; exit 1; }
printf '%s\n' "${OPERATOR#*:}" | bin/andara-cli auth login --username "${OPERATOR%%:*}" --password-stdin >/dev/null \
  || fail "auth login failed"

# AC-1. Two player Accounts, fresh per run: names are reserved forever and a roster holds five.
hex="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
letters() { python3 -c 'import secrets, string; print("".join(secrets.choice(string.ascii_lowercase) for _ in range(8)))'; }
CHAR_A="Recovered$(letters)"
CHAR_B="Bystander$(letters)"
for who in a b; do
  printf 'stack-recover-password-1\n' | bin/andara-cli account create --username "recover-$who-$hex" --password-stdin >/dev/null \
    || fail "account create recover-$who-$hex failed"
  printf 'stack-recover-password-1\n' | bin/andara-cli --credentials "$WORK/cred-$who.yaml" auth login --username "recover-$who-$hex" --password-stdin >/dev/null \
    || fail "auth login recover-$who-$hex failed"
done
A=(--credentials "$WORK/cred-a.yaml")
B=(--credentials "$WORK/cred-b.yaml")
bin/andara-cli "${A[@]}" character create "$CHAR_A" >/dev/null || fail "character create $CHAR_A failed"
bin/andara-cli "${B[@]}" character create "$CHAR_B" >/dev/null || fail "character create $CHAR_B failed"

round_before="$(newest_round)"; round_before="${round_before:-0}"
echo "stack-recover: newest complete round before the run: ${round_before/#0/none}"

# --show-protocol puts `reason=already_live` on the transcript; play prints only the waiting line
# for it otherwise (docs/feedback/AW-INF-032-stack-recover.md, PM's 2026-10-03 note).
echo "stack-recover: $CHAR_B spawns in Purgatory and stays to watch ..."
mkfifo "$WORK/b-in"
bin/andara-cli "${B[@]}" play --character "$CHAR_B" --show-protocol <"$WORK/b-in" >"$BOUT" 2>&1 &
BPID=$!
exec 4>"$WORK/b-in"
poll "$BOUT" "^-- Connected to [^ ]\+ as recover-b-$hex, playing $CHAR_B " 20 "$BPID" || fail "B never connected playing $CHAR_B"
poll "$BOUT" '^Purgatory$' 20 "$BPID" || fail "B never read Purgatory from its automatic look"

echo "stack-recover: $CHAR_A spawns in Purgatory and walks out to the Market Plaza ..."
mkfifo "$WORK/a-in"
bin/andara-cli "${A[@]}" play --character "$CHAR_A" --show-protocol <"$WORK/a-in" >"$AOUT" 2>&1 &
APID=$!
exec 3>"$WORK/a-in"
poll "$AOUT" "^-- Connected to [^ ]\+ as recover-a-$hex, playing $CHAR_A " 20 "$APID" || fail "A never connected playing $CHAR_A"
poll "$AOUT" '^Purgatory$' 20 "$APID" || fail "A never read Purgatory from its automatic look"
printf 'out\nlook\n' >&3
poll "$AOUT" '^Market Plaza$' 20 "$APID" || fail "A never read Room 1 (the Market Plaza) from its look after out"

# AC-2. A round newer than A's first move, polled to a deadline of snapshot.interval + 30 s.
echo "stack-recover: waiting up to ${ROUND_DEADLINE}s for a complete round past ${round_before} ..."
R=""
for _ in $(seq 1 "$ROUND_DEADLINE"); do
  n="$(newest_round)"
  if [[ -n "$n" ]] && (( n > round_before )); then R="$n"; break; fi
  sleep 1
done
[[ -n "$R" ]] || fail "no complete snapshot round newer than ${round_before} within ${ROUND_DEADLINE}s; the newest seen was $(newest_round | sed 's/^$/none/')"
echo "stack-recover: round R = $R"

# AC-3. A's second move is after R and before the kill: it can only come back from the log tail.
echo "stack-recover: $CHAR_A walks north to the Town Hall, after the round ..."
printf 'north\nlook\n' >&3
poll "$AOUT" '^Town Hall$' 20 "$APID" || fail "A never read Room 2 (the Town Hall) from its look after north"
latest="$(newest_round)"
[[ "$latest" == "$R" ]] \
  || fail "a round completed after the tail move (newest is ${latest:-none}, R was $R); the run proves nothing, rerun"

a_lines="$(wc -l <"$AOUT")"; b_lines="$(wc -l <"$BOUT")"
echo "stack-recover: SIGKILL to andara-server, then start ..."
SERVER_KILLED=1
t_kill="$(date +%s.%N)"
"${COMPOSE[@]}" kill -s KILL andara-server >/dev/null || fail "docker compose kill andara-server failed"
SINCE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
"${COMPOSE[@]}" start andara-server >/dev/null || fail "docker compose start andara-server failed"
ready=""
for _ in $(seq 1 $(( RTO * 4 ))); do
  if curl -sf "$READYZ" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.25
done
t_ready="$(date +%s.%N)"
[[ -n "$ready" ]] || fail "/readyz did not return 200 within ${RTO}s of the kill"
kill_to_ready="$(python3 -c 'import sys; print(round(float(sys.argv[2]) - float(sys.argv[1]), 1))' "$t_kill" "$t_ready")"
python3 -c 'import sys; sys.exit(0 if float(sys.argv[1]) <= float(sys.argv[2]) else 1)' "$kill_to_ready" "$RTO" \
  || fail "ready ${kill_to_ready}s after the kill, over the ${RTO}s RTO"

# AC-4. The recovery's own instruments, polled to a deadline: the first scrape after ready can
# land before the server has published them.
echo "stack-recover: the server's /metrics ..."
hash_match=""; round_tick=""
for _ in $(seq 1 30); do
  hash_match="$(metric andara_recovery_state_hash_match)"
  round_tick="$(metric andara_recovery_round_tick)"
  [[ "$hash_match" == "1" && -n "$round_tick" ]] && break
  sleep 0.5
done
[[ "$hash_match" == "1" ]] || fail "andara_recovery_state_hash_match is '${hash_match:-absent}' after recovery, want 1"
[[ -n "$round_tick" ]] || fail "andara_recovery_round_tick is absent after recovery"
if (( round_tick > R )); then
  fail "recovery used round $round_tick, newer than R=$R: a round completed between the re-read and the kill; the run proves nothing, rerun"
fi
(( round_tick == R )) || fail "andara_recovery_round_tick is $round_tick, want R=$R: the recovery did not start from that round"
replayed="$(metric andara_recovery_replayed_ticks)"
total_s="$(metric andara_recovery_duration_seconds_sum phase=total)"
restored="$(metric andara_restore_total caller=recovery outcome=ok)"
trace_id="$("${COMPOSE[@]}" logs --no-log-prefix --since "$SINCE" andara-server 2>/dev/null \
  | python3 -c '
import json, sys
tid = ""
for line in sys.stdin:
    try:
        o = json.loads(line)
    except ValueError:
        continue
    if o.get("msg") == "recovery complete":
        tid = o.get("trace_id", "")
print(tid)
')"
echo "stack-recover: ready ${kill_to_ready}s after the kill (RTO ${RTO}s), round ${round_tick}, hash match"
echo "stack-recover: kill-to-ready ${kill_to_ready}s; process-start-to-ready (andara_recovery_duration_seconds{phase=\"total\"}) ${total_s:-n/a}s; replayed ticks ${replayed:-n/a}; restore ok ${restored:-n/a}; recovery.run trace ${trace_id:-n/a}"

# AC-5. The clients reconnect on their own: a second `Connected` line each, then a look.
echo "stack-recover: waiting for both players to rebind ..."
poll_after "$AOUT" "$a_lines" "^-- Connected to [^ ]\+ as recover-a-$hex, playing $CHAR_A " 60 "$APID" \
  || fail "A's play did not reconnect within 60s of the server being ready"
poll_after "$BOUT" "$b_lines" "^-- Connected to [^ ]\+ as recover-b-$hex, playing $CHAR_B " 60 "$BPID" \
  || fail "B's play did not reconnect within 60s of the server being ready"
a_mark="$(wc -l <"$AOUT")"; b_mark="$(wc -l <"$BOUT")"
printf 'look\n' >&3
printf 'look\n' >&4
poll_after "$AOUT" "$a_mark" '^Town Hall$' 20 "$APID" || fail "A's look after the recovery does not read the Town Hall: the tail move was not replayed"
poll_after "$BOUT" "$b_mark" '^Purgatory$' 20 "$BPID" || fail "B's look after the recovery does not read Purgatory"
# Absence, anchored (live-assertions.md rule 3): each stream is in order, so with the answering
# look read, any despawn or refusal before it is in the transcript too.
if tail -n +"$(( a_lines + 1 ))" "$AOUT" | grep -q -e 'reason=already_live' -e 'Waiting for your previous session to end'; then
  fail "A's reconnect was refused already_live"
fi
if tail -n +"$(( b_lines + 1 ))" "$BOUT" | grep -q -e 'reason=already_live' -e 'Waiting for your previous session to end'; then
  fail "B's reconnect was refused already_live"
fi
if tail -n +"$(( a_lines + 1 ))" "$AOUT" | grep -q -e 'leaves the world\.' -e 'fades from the world\.'; then
  fail "A read a despawn line between the kill and the reconnect"
fi
if tail -n +"$(( b_lines + 1 ))" "$BOUT" | grep -q -e 'leaves the world\.' -e 'fades from the world\.'; then
  fail "B read a despawn line between the kill and the reconnect"
fi
echo "stack-recover: both rebound; $CHAR_A reads the Town Hall (the tail move), $CHAR_B reads Purgatory"

# AC-6. A clean quit: EOF on stdin is play's quit, and it sends CloseSession.
echo "stack-recover: both quit ..."
exec 3>&-
exec 4>&-
set +e; wait "$APID"; acode=$?; wait "$BPID"; bcode=$?; set -e
APID=""; BPID=""
[[ "$acode" == "0" ]] || fail "A's play exited $acode at quit, want 0"
[[ "$bcode" == "0" ]] || fail "B's play exited $bcode at quit, want 0"
list_a="$(bin/andara-cli "${A[@]}" character list)"
list_b="$(bin/andara-cli "${B[@]}" character list)"
after_a="$(state_and_room "$CHAR_A" "$list_a")"
after_b="$(state_and_room "$CHAR_B" "$list_b")"
# The Rooms the looks read after the recovery: the roster records a body's Room at its unbind.
[[ "$after_a" == "dormant town/hall" ]] \
  || { echo "$list_a" >&2; fail "character list shows $CHAR_A as '$after_a', want 'dormant town/hall'"; }
[[ "$after_b" == "dormant purgatory/start" ]] \
  || { echo "$list_b" >&2; fail "character list shows $CHAR_B as '$after_b', want 'dormant purgatory/start'"; }

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "| kill-to-ready (s) | process-start-to-ready (s) | replayed ticks | round tick |"
    echo "|---|---|---|---|"
    echo "| ${kill_to_ready} | ${total_s:-n/a} | ${replayed:-n/a} | ${round_tick} |"
  } >>"$GITHUB_STEP_SUMMARY"
fi
echo "stack-recover: M2 gate — killed, recovered from a snapshot, hash matched, both rebound — passes"
