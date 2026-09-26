#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# The linkdead gate (AW-INF-017), scripted against the running stack: the
# linkdead slice of M2, "every linkdead Character rebinds rather than
# despawning".
#   - Two player Accounts each create a Character. B enters town/plaza and
#     watches; A enters and B sees A arrive.
#   - The script SIGKILLs A's `andara-cli play`. No CloseSession is sent, so
#     the stream just ends (AW-SRV-015's transport-close path; the keepalive
#     path, a partition with no close, is AW-SRV-015's integration test).
#   - B reads `<A> goes linkdead.`, and B's `look` lists `<A> (linkdead)`.
#   - A plays the same Character again: no `already_live`, the plaza, and B
#     reads `<A> reconnects.`. B read no despawn line for A in between.
#   - A quits cleanly. B reads `<A> leaves the world.`, and A's body is dormant
#     in the plaza.
#   - The server's /metrics counted the reconnect and the quit, and its
#     linkdead gauge is back where it started.
#
# Grace expiry and the combat extension are AW-SRV-015's integration tests on
# Ticks: a live 180 s wait per CI run is not worth it.
#
# Creates two Accounts and two Characters per run, with random suffixes, as
# stack_play.sh does. `make down VOLUMES=1` clears them.
#
# Requires a stack: `make up` first, and `make build`. CI runs it as a step of
# the `stack` workflow, after `make stack-play`.

set -euo pipefail
cd "$(dirname "$0")/.."

[[ -f .local/tls/ca.pem ]] || { echo "stack-linkdead: no .local/tls/ca.pem; run \`make up\` first" >&2; exit 1; }
[[ -f .local/cli.yaml ]] || { echo "stack-linkdead: no .local/cli.yaml; run \`make up\` first" >&2; exit 1; }
[[ -x bin/andara-cli ]] || { echo "stack-linkdead: no bin/andara-cli; run \`make build\` first" >&2; exit 1; }

HTTP_PORT="${ANDARA_HTTP_PORT:-8080}"
METRICS="http://localhost:${HTTP_PORT}/metrics"
OPERATOR="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}"
COMPOSE="docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server"
# AC-2's deadline: session.linkdead_detect (5 s by default) plus 10 s. SIGKILL
# closes the socket, so the drop is seen at once; the margin covers the
# keepalive path too, if detection ever moves to it.
# The env var is a Go duration (`500ms`, `1m30s`, `1.5s`), parsed here the
# same way and rounded up to whole seconds.
DETECT_S="$(python3 - "${ANDARA_LINKDEAD_DETECT:-5s}" <<'PY'
import math, re, sys
d = sys.argv[1].strip()
units = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1, "m": 60, "h": 3600}
parts = re.findall(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h)", d)
if d == "0":
    print(0)
elif not parts or "".join(n + u for n, u in parts) != d.lstrip("+"):
    sys.exit("stack-linkdead: ANDARA_LINKDEAD_DETECT=%r is not a Go duration" % d)
else:
    print(math.ceil(sum(float(n) * units[u] for n, u in parts)))
PY
)" || exit 1
DROP_DEADLINE=$(( DETECT_S + 10 ))

# An isolated home: the credentials this writes must not land in the developer's.
WORK="$(mktemp -d -t stack-linkdead.XXXXXX)"
export ANDARA_CONFIG="$PWD/.local/cli.yaml"
export ANDARA_TLS_CA_FILE="${ANDARA_TLS_CA_FILE:-$PWD/.local/tls/ca.pem}"
export XDG_CONFIG_HOME="$WORK/config"
export XDG_STATE_HOME="$WORK/state"

APID=""; A2PID=""; BPID=""
AOUT="$WORK/a-out.txt"; AERR="$WORK/a-err.txt"
A2OUT="$WORK/a2-out.txt"; A2ERR="$WORK/a2-err.txt"
BOUT="$WORK/b-out.txt"; BERR="$WORK/b-err.txt"
touch "$AOUT" "$AERR" "$A2OUT" "$A2ERR" "$BOUT" "$BERR"

# No andara-cli child outlives the script, whichever way it exits (AC-6).
cleanup() {
  for p in $APID $A2PID $BPID; do kill -9 "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

transcripts() {
  echo "--- A, first play (killed):" >&2; sed 's/^/  A| /' "$AOUT" "$AERR" >&2
  echo "--- A, second play:" >&2; sed 's/^/ A2| /' "$A2OUT" "$A2ERR" >&2
  echo "--- B, the bystander:" >&2; sed 's/^/  B| /' "$BOUT" "$BERR" >&2
}
fail() {
  echo "stack-linkdead: $*" >&2
  transcripts
  echo "--- andara-server, last 50 lines:" >&2
  $COMPOSE logs --no-color --tail 50 andara-server >&2 2>&1 || true
  exit 1
}

# metric <name> [label=value ...]: the sum of every sample of <name> whose
# labels include all the given pairs, in any order; 0 when there is none.
metric() {
  { curl -sf "$METRICS" || true; } | python3 -c '
import re, sys
name, want = sys.argv[1], dict(a.split("=", 1) for a in sys.argv[2:])
total = 0.0
for line in sys.stdin:
    m = re.match(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(.*)\})? (\S+)", line)
    if not m or m.group(1) != name:
        continue
    labels = dict(re.findall(r"(\w+)=\"((?:[^\"\\]|\\.)*)\"", m.group(3) or ""))
    if all(labels.get(k) == v for k, v in want.items()):
        total += float(m.group(4))
print(total)
' "$@"
}

# poll <file> <pattern> <seconds> <pid>: poll to a deadline for a line in a
# transcript (docs/specs/testing/live-assertions.md rule 1). Fails early if
# the play writing it has exited.
poll() {
  local file="$1" pat="$2" secs="$3" pid="${4:-}"
  for _ in $(seq 1 "$(( secs * 2 ))"); do
    grep -q -- "$pat" "$file" && return 0
    [[ -z "$pid" ]] || kill -0 "$pid" 2>/dev/null || { grep -q -- "$pat" "$file" && return 0; return 1; }
    sleep 0.5
  done
  grep -q -- "$pat" "$file"
}

echo "stack-linkdead: logging in as ${OPERATOR%%:*} ..."
printf '%s\n' "${OPERATOR#*:}" | bin/andara-cli auth login --username "${OPERATOR%%:*}" --password-stdin >/dev/null \
  || fail "auth login failed"

# Two player Accounts, fresh per run: names are reserved forever and a roster
# holds five. Usernames take hex; Character names are letters only.
hex="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
letters() { python3 -c 'import secrets, string; print("".join(secrets.choice(string.ascii_lowercase) for _ in range(8)))'; }
CHAR_A="Dropped$(letters)"
CHAR_B="Watcher$(letters)"
for who in a b; do
  printf 'stack-linkdead-password-1\n' | bin/andara-cli account create --username "linkdead-$who-$hex" --password-stdin >/dev/null \
    || fail "account create linkdead-$who-$hex failed"
  printf 'stack-linkdead-password-1\n' | bin/andara-cli --credentials "$WORK/cred-$who.yaml" auth login --username "linkdead-$who-$hex" --password-stdin >/dev/null \
    || fail "auth login linkdead-$who-$hex failed"
done
A=(--credentials "$WORK/cred-a.yaml")
B=(--credentials "$WORK/cred-b.yaml")
bin/andara-cli "${A[@]}" character create "$CHAR_A" >/dev/null || fail "character create $CHAR_A failed"
bin/andara-cli "${B[@]}" character create "$CHAR_B" >/dev/null || fail "character create $CHAR_B failed"

reconnected_before="$(metric andara_linkdead_outcomes_total outcome=reconnected)"
quits_before="$(metric andara_character_unbinds_total reason=quit outcome=ok)"
linkdead_before="$(metric andara_sessions_linkdead)"

# AC-1. B first, on a held-open FIFO, and in the plaza before A arrives: the
# arrival reaches B only over its stream.
echo "stack-linkdead: $CHAR_B enters town/plaza to watch ..."
mkfifo "$WORK/b-in"
bin/andara-cli "${B[@]}" play --character "$CHAR_B" <"$WORK/b-in" >"$BOUT" 2>"$BERR" &
BPID=$!
exec 4>"$WORK/b-in"
poll "$BOUT" "^-- Connected to [^ ]\+ as linkdead-b-$hex, playing $CHAR_B " 20 "$BPID" || fail "B never connected playing $CHAR_B"
poll "$BOUT" '^Market Plaza$' 20 "$BPID" || fail "B never read the plaza from its automatic look"

echo "stack-linkdead: $CHAR_A enters town/plaza ..."
mkfifo "$WORK/a-in"
bin/andara-cli "${A[@]}" play --character "$CHAR_A" <"$WORK/a-in" >"$AOUT" 2>"$AERR" &
APID=$!
exec 3>"$WORK/a-in"
poll "$AOUT" "^-- Connected to [^ ]\+ as linkdead-a-$hex, playing $CHAR_A " 20 "$APID" || fail "A never connected playing $CHAR_A"
poll "$AOUT" '^Market Plaza$' 20 "$APID" || fail "A never read the plaza from its automatic look"
poll "$BOUT" "^$CHAR_A arrives\.$" 20 "$BPID" || fail "B did not see $CHAR_A arrive"

# AC-2. The drop: SIGKILL, so the client sends nothing on its way out.
echo "stack-linkdead: SIGKILL to $CHAR_A's play ..."
kill -9 "$APID"
wait "$APID" 2>/dev/null || true
APID=""
exec 3>&-
kill_line="$(wc -l <"$BOUT")"
poll "$BOUT" "^$CHAR_A goes linkdead\.$" "$DROP_DEADLINE" "$BPID" \
  || fail "B did not read '$CHAR_A goes linkdead.' within ${DROP_DEADLINE}s of the kill"
# The marker is the answer to a look sent after the linkdead line, so a Here:
# line that has it can only be that answer.
printf 'look\n' >&4
poll "$BOUT" "^Here: \(.*, \)\?$CHAR_A (linkdead)\(,\|$\)" 10 "$BPID" \
  || fail "B's look does not list '$CHAR_A (linkdead)' under Here:"
python3 - "$(metric andara_sessions_linkdead)" "$linkdead_before" <<'PY' || fail "andara_sessions_linkdead did not count $CHAR_A"
import sys
now, before = (float(x) for x in sys.argv[1:])
assert now >= before + 1, "andara_sessions_linkdead %s -> %s" % (before, now)
PY
echo "stack-linkdead: $CHAR_B read $CHAR_A go linkdead, and look marks the body"

# AC-3. A plays the same Character again, inside the grace.
echo "stack-linkdead: $CHAR_A plays again ..."
mkfifo "$WORK/a2-in"
bin/andara-cli "${A[@]}" play --character "$CHAR_A" <"$WORK/a2-in" >"$A2OUT" 2>"$A2ERR" &
A2PID=$!
exec 3>"$WORK/a2-in"
poll "$A2OUT" "^-- Connected to [^ ]\+ as linkdead-a-$hex, playing $CHAR_A " 20 "$A2PID" \
  || fail "A's second play never connected playing $CHAR_A$(grep -q 'already live' "$A2ERR" && echo ': refused already_live')"
! grep -q 'already live' "$A2ERR" || fail "A's second play was refused already_live"
poll "$A2OUT" '^Market Plaza$' 20 "$A2PID" || fail "A's second play did not read the plaza"
poll "$BOUT" "^$CHAR_A reconnects\.$" 20 "$BPID" || fail "B did not read '$CHAR_A reconnects.'"
# Absence, anchored (live-assertions.md rule 3): B's stream is in order, so
# once the reconnect is in B's transcript any despawn before it is too.
reconnect_line="$(grep -n "^$CHAR_A reconnects\.$" "$BOUT" | head -1 | cut -d: -f1)"
! sed -n "${kill_line},${reconnect_line}p" "$BOUT" | grep -q "^$CHAR_A \(leaves\|fades from\) the world\.$" \
  || fail "B read a despawn for $CHAR_A between the kill and the reconnect"
echo "stack-linkdead: $CHAR_A reconnected into the plaza; the body never left"

# AC-4. A clean quit: EOF on stdin is play's quit, and it sends CloseSession.
echo "stack-linkdead: $CHAR_A quits ..."
exec 3>&-
set +e; wait "$A2PID"; a2code=$?; set -e
A2PID=""
[[ "$a2code" == "0" ]] || fail "A's second play exited $a2code at quit, want 0"
poll "$BOUT" "^$CHAR_A leaves the world\.$" 20 "$BPID" || fail "B did not read '$CHAR_A leaves the world.'"
list="$(bin/andara-cli "${A[@]}" character list)"
grep -q "^$CHAR_A \+dormant \+town/plaza$" <<<"$list" \
  || { echo "$list" >&2; fail "character list does not show $CHAR_A dormant in town/plaza"; }

exec 4>&-
set +e; wait "$BPID"; bcode=$?; set -e
BPID=""
[[ "$bcode" == "0" ]] || fail "B's play exited $bcode, want 0"

# AC-5. The server's own instruments, polled to a deadline.
echo "stack-linkdead: the server's /metrics ..."
ok=""
for _ in $(seq 1 30); do
  if python3 - "$reconnected_before" "$(metric andara_linkdead_outcomes_total outcome=reconnected)" \
      "$quits_before" "$(metric andara_character_unbinds_total reason=quit outcome=ok)" \
      "$linkdead_before" "$(metric andara_sessions_linkdead)" <<'PY'
import sys
rb, ra, qb, qa, lb, la = (float(x) for x in sys.argv[1:])
sys.exit(0 if ra >= rb + 1 and qa >= qb + 1 and la == lb else 1)
PY
  then ok=1; break; fi
  sleep 0.5
done
[[ -n "$ok" ]] || fail "after 15s: linkdead_outcomes{reconnected} $reconnected_before -> $(metric andara_linkdead_outcomes_total outcome=reconnected), character_unbinds{quit,ok} $quits_before -> $(metric andara_character_unbinds_total reason=quit outcome=ok), sessions_linkdead $linkdead_before -> $(metric andara_sessions_linkdead) (want +1, +1, unchanged)"

sed 's/^/  B| /' "$BOUT"
echo "stack-linkdead: linkdead gate — dropped, marked, reconnected, quit — passes"
