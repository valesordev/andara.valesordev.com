#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# The M1 gate, scripted against the running stack, in two halves.
#
# The Protocol half (AW-SRV-014): two players create a Character each and
# enter the World over the generated client — the Room after look, the move
# north through Redpanda and the tick on both Event streams, a post-log
# rejection as prose, the quit seen by the other player, the body woken
# where it went dormant — with the instruments AW-SRV-010, -011, and -031
# could only drive in their integration suites scraped from the server. It
# lives in Go (internal/smoke, TestLive_M1Gate) for the reason stack_smoke.sh
# gives: the generated client is the only thing that speaks the Protocol.
#
# The client half (AW-CLI-004, AW-CLI-007): `andara-cli play` driving a bound
# Character, with the transcript asserted.
#   - Two player Accounts each create a Character (`character create`, `list`).
#   - A walks: the plaza, `north`, and the Town Hall on the next `look`.
#   - A tries `west` from the Hall: a post-log rejection, as prose.
#   - A types `frobnicate`: a pre-log rejection, as prose.
#   - B, playing alongside, sees A arrive and leave.
#   - A second launch of B's Character is refused `already_live`.
#   - Under --output json, stdout is JSON and nothing else.
#   - A server restart under an open session is announced, and the Character is
#     re-selected on the new Session (AW-CLI-004 AC-7, AW-CLI-007 AC-8).
#
# Transitional (2026-09-25): until AW-CLI-007 (PR #72) is on main,
# bin/andara-cli has no `character` command and play cannot select a body.
# The script then runs the half that exists: every Intent refused "you are not
# in the world". The CLI's own command list chooses the path. `character --help`
# cannot, because it exits 0 on a CLI without the command. The pre-007 path is
# removed by the first architecture PR after #72 merges.
#
# The Protocol half creates two Accounts and two Characters per run, with
# random suffixes: names are reserved forever and a roster holds five, so it
# cannot reuse them. On CI's fresh stack that is two of each; on a developer's
# long-lived stack they accumulate (`smoke-a-*`, `smoke-b-*`, `Smoke*`), which
# is where those Accounts come from. `make down VOLUMES=1` clears them.
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

# The Protocol half first: it is the gate itself, and the play half below
# reads counters it leaves behind, none of which it resets.
echo "stack-play: the M1 gate over the Protocol (AW-SRV-014) ..."
export ANDARA_TLS_CA_FILE="${ANDARA_TLS_CA_FILE:-$PWD/.local/tls/ca.pem}"
export ANDARA_SMOKE_ADDR="${ANDARA_SMOKE_ADDR:-localhost:${ANDARA_GRPC_PORT:-8443}}"
export ANDARA_SMOKE_OPERATOR="$OPERATOR"
export ANDARA_SMOKE_METRICS_ADDR="localhost:${HTTP_PORT}"
"${GO:-go}" test -tags smoke -count=1 -v -run TestLive_M1Gate ./internal/smoke/ || fail "the M1 gate over the Protocol failed"
echo "stack-play: two players entered the World, looked, moved through the log, and left"

echo "stack-play: logging in as ${OPERATOR%%:*} ..."
printf '%s\n' "${OPERATOR#*:}" | bin/andara-cli auth login --username "${OPERATOR%%:*}" --password-stdin >/dev/null \
  || fail "auth login failed"

# Captured, not piped into `grep -q`: under pipefail, grep leaving at the first
# match kills the writer with SIGPIPE and fails the pipeline.
cli_help="$(bin/andara-cli --help)"
if grep -q '^  character ' <<<"$cli_help"; then
  HAS_CHARACTER=1
else
  HAS_CHARACTER=0
  echo "stack-play: bin/andara-cli has no \`character\` command (pre-AW-CLI-007); running the play half that exists"
fi

# PLAY is how every later section launches play. With characters, it plays as
# A, the Account's only Character, so AC-5's default selects it. Without them,
# it plays as the operator, bodiless.
PLAY=(bin/andara-cli play)

if [[ "$HAS_CHARACTER" == 1 ]]; then
  # Two player Accounts, fresh per run. Names are reserved forever and a
  # roster holds five, so nothing is reused. Usernames take hex. Character
  # names are letters only (^[\p{L}][\p{L}' -]{2,23}$), so their suffix is
  # letters.
  hex="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
  letters() { python3 -c 'import secrets, string; print("".join(secrets.choice(string.ascii_lowercase) for _ in range(8)))'; }
  CHAR_A="Walker$(letters)"
  CHAR_B="Watcher$(letters)"
  for who in a b; do
    printf 'stack-play-password-1\n' | bin/andara-cli account create --username "play-$who-$hex" --password-stdin >/dev/null \
      || fail "account create play-$who-$hex failed"
    printf 'stack-play-password-1\n' | bin/andara-cli --credentials "$WORK/cred-$who.yaml" auth login --username "play-$who-$hex" --password-stdin >/dev/null \
      || fail "auth login play-$who-$hex failed"
  done
  A=(--credentials "$WORK/cred-a.yaml")
  B=(--credentials "$WORK/cred-b.yaml")

  # AW-CLI-007 AC-1 and AC-3: create prints the count, list shows the body where it will spawn.
  echo "stack-play: character create $CHAR_A, $CHAR_B; character list ..."
  [[ "$(bin/andara-cli "${A[@]}" character create "$CHAR_A")" == "$CHAR_A (1 of 5)" ]] || fail "character create $CHAR_A did not print '$CHAR_A (1 of 5)'"
  [[ "$(bin/andara-cli "${B[@]}" character create "$CHAR_B")" == "$CHAR_B (1 of 5)" ]] || fail "character create $CHAR_B did not print '$CHAR_B (1 of 5)'"
  list="$(bin/andara-cli "${A[@]}" character list)"
  grep -q "^$CHAR_A \+dormant \+town/plaza$" <<<"$list" \
    || { echo "$list" >&2; fail "character list does not show $CHAR_A dormant in town/plaza"; }
  PLAY=(bin/andara-cli "${A[@]}" play)

  # B plays alongside A for the whole walk, on a held-open FIFO.
  BOUT="$WORK/b-out.txt"; BERR="$WORK/b-err.txt"; BFIFO="$WORK/b-stdin"
  mkfifo "$BFIFO"
  bin/andara-cli "${B[@]}" play --character "$CHAR_B" <"$BFIFO" >"$BOUT" 2>"$BERR" &
  BPID=$!
  exec 4>"$BFIFO"
  for _ in $(seq 1 40); do grep -q '^-- Connected to ' "$BOUT" && break; kill -0 "$BPID" 2>/dev/null || break; sleep 0.5; done
  grep -q "^-- Connected to [^ ]\+ as play-b-$hex, playing $CHAR_B (session [0-9a-f]\{32\}, protocol 1)\.$" "$BOUT" \
    || { cat "$BOUT" "$BERR" >&2; fail "B never connected playing $CHAR_B"; }
  # B's automatic look answered, so B is subscribed and in the plaza before A
  # arrives: the Room reaches B only over its stream. Polled to a deadline, not
  # slept (docs/specs/testing/live-assertions.md): on a loaded stack a fixed
  # delay can pass before the look is answered, and A's arrival then goes unseen.
  for _ in $(seq 1 40); do grep -q '^Market Plaza$' "$BOUT" && break; kill -0 "$BPID" 2>/dev/null || break; sleep 0.5; done
  grep -q '^Market Plaza$' "$BOUT" || { cat "$BOUT" "$BERR" >&2; fail "B never read the plaza from its automatic look"; }
fi

parse_before="$(metric 'andara_ingress_submits_total{outcome="rejected_parse"}')"
authz_before="$(metric 'andara_ingress_submits_total{outcome="rejected_authz"}')"
closed_before="$(metric 'andara_sessions_total{outcome="closed"}')"

OUT="$WORK/transcript.txt"
ERR="$WORK/stderr.txt"
if [[ "$HAS_CHARACTER" == 1 ]]; then
  # The Character is named in lower case: resolution ignores case (AW-CLI-007).
  # The `sleep`s let each Event land before the next line; the assertions do
  # not depend on it, only the transcript's readability.
  echo "stack-play: $CHAR_A: look, north, look, west, frobnicate, quit ..."
  WALK="look north look west frobnicate"
  set +e
  { for l in look north look west frobnicate; do printf '%s\n' "$l"; sleep 1; done; } \
    | bin/andara-cli "${A[@]}" play --character "$(printf '%s' "$CHAR_A" | tr 'A-Z' 'a-z')" --show-protocol >"$OUT" 2>"$ERR"
  code=$?
  set -e
else
  echo "stack-play: look, north, west, frobnicate, quit ..."
  WALK="look north west frobnicate"
  set +e
  printf 'look\nnorth\nwest\nfrobnicate\n' | "${PLAY[@]}" --show-protocol >"$OUT" 2>"$ERR"
  code=$?
  set -e
fi
sed 's/^/  | /' "$OUT"
[[ "$code" == "0" ]] || { cat "$ERR" >&2; fail "play exited $code, want 0"; }
[[ ! -s "$ERR" ]] || { cat "$ERR" >&2; fail "play wrote to stderr in human mode"; }

# The Session opened over TLS and subscribed (AC-1); with a Character, it was
# selected before the Subscribe and the notice names it (AW-CLI-007 AC-4).
if [[ "$HAS_CHARACTER" == 1 ]]; then
  grep -q "^-- Connected to [^ ]\+ as play-a-$hex, playing $CHAR_A (session [0-9a-f]\{32\}, protocol 1)\.$" "$OUT" \
    || fail "no connection notice naming $CHAR_A"
  sel="$(grep -n '» SelectCharacter session_id=[0-9a-f]\{32\} character_id=' "$OUT" | head -1 | cut -d: -f1)"
  sub="$(grep -n '» Subscribe session_id=' "$OUT" | head -1 | cut -d: -f1)"
  [[ -n "$sel" && -n "$sub" && "$sel" -lt "$sub" ]] || fail "SelectCharacter did not precede Subscribe"
else
  grep -q '^-- Connected to [^ ]\+ as operator (session [0-9a-f]\{32\}, protocol 1)\.$' "$OUT" \
    || fail "no connection notice"
fi
grep -q '» Subscribe session_id=[0-9a-f]\{32\} last_event_id=0 world=false' "$OUT" || fail "no Subscribe"

# Every line went out as typed, each with its own client_ref (AW-SRV-031), the
# automatic look first.
for raw in $WALK; do
  grep -q "» Submit session_id=[0-9a-f]\{32\} client_ref=[0-9a-f]\{8\}-[0-9]\+ raw=\"$raw\"" "$OUT" \
    || fail "no Submit for $raw"
done
refs="$(grep -o 'client_ref=[0-9a-f]\{8\}-[0-9]\+ raw=' "$OUT" | sort | uniq -d)"
[[ -z "$refs" ]] || fail "a client_ref was reused: $refs"

# What the player read. Protocol lines are indented and begin with » or «; the
# player's lines are everything else. Refusals read as prose, with nothing
# about where in the pipeline they were made (AC-4, AC-5).
PLAYER="$WORK/player.txt"
grep -v '^  [»«]' "$OUT" > "$PLAYER" || true
if [[ "$HAS_CHARACTER" == 1 ]]; then
  # The M1 gate in play's own transcript: a Room, the move, a different Room.
  # A move describes no Room, so the second one is the answer to the `look`
  # after it (AW-CLI-007 feedback §3).
  python3 - "$PLAYER" "$CHAR_A" <<'PY' || fail "the walk is not in the transcript in order"
import sys
lines = [l.rstrip("\n") for l in open(sys.argv[1])]
a = sys.argv[2]
def at(pred, start, what):
    for i in range(start, len(lines)):
        if pred(lines[i]):
            return i
    sys.exit("missing, in order: %s" % what)
i = at(lambda l: l == "Market Plaza", 0, "Market Plaza")
i = at(lambda l: l == a + " leaves north.", i + 1, a + " leaves north.")
i = at(lambda l: l == "Town Hall", i + 1, "Town Hall after the move")
i = at(lambda l: l.lower().rstrip(".") == "there is no exit west", i + 1, "the no-exit rejection")
at(lambda l: l.startswith("unknown verb"), i + 1, "the unknown-verb rejection")
PY
  ! grep -q '^you are not in the world$' "$PLAYER" || fail "a bound Character was told it is not in the world"
else
  grep -q '^you are not in the world$' "$PLAYER" || fail "the refusal is not in the transcript as the server worded it"
fi
for no in stage offset partition pre_log permission_denied invalid_argument failed_precondition; do
  ! grep -qi "$no" "$PLAYER" || fail "the player's transcript mentions '$no'"
done

if [[ "$HAS_CHARACTER" == 1 ]]; then
  # B saw A come and go, unprompted (AW-CLI-004 AC-3).
  for _ in $(seq 1 20); do grep -q "^$CHAR_A leaves north\.$" "$BOUT" && break; sleep 0.5; done
  grep -q "^$CHAR_A arrives\.$" "$BOUT" || { sed 's/^/  B| /' "$BOUT" >&2; fail "B did not see $CHAR_A arrive"; }
  grep -q "^$CHAR_A leaves north\.$" "$BOUT" || { sed 's/^/  B| /' "$BOUT" >&2; fail "B did not see $CHAR_A leave north"; }

  # AW-CLI-007 AC-7: a second launch while B is live is refused, and nothing is subscribed.
  set +e
  bin/andara-cli "${B[@]}" play --character "$CHAR_B" --show-protocol </dev/null >"$WORK/live-out.txt" 2>"$WORK/live-err.txt"
  lcode=$?
  set -e
  [[ "$lcode" == "1" ]] || { cat "$WORK/live-out.txt" "$WORK/live-err.txt" >&2; fail "a second launch of $CHAR_B exited $lcode, want 1"; }
  grep -q "a character is already live on this account: $CHAR_B" "$WORK/live-err.txt" \
    || { cat "$WORK/live-err.txt" >&2; fail "the already_live refusal is not the server's message"; }
  # Nothing subscribed. The selection is in the protocol record, which shows the
  # record is live, and no Subscribe or Submit follows it.
  grep -q '» SelectCharacter session_id=' "$WORK/live-out.txt" \
    || { cat "$WORK/live-out.txt" >&2; fail "the refused launch shows no SelectCharacter under --show-protocol"; }
  ! grep -q '» \(Subscribe\|Submit\) ' "$WORK/live-out.txt" \
    || { cat "$WORK/live-out.txt" >&2; fail "the refused launch subscribed or submitted"; }

  exec 4>&-
  set +e; wait "$BPID"; bcode=$?; set -e
  sed 's/^/  B| /' "$BOUT"
  [[ "$bcode" == "0" ]] || { cat "$BERR" >&2; fail "B's play exited $bcode, want 0"; }
fi

# A clean quit closed the Session (AC-11), and the server counted the refusals.
grep -q '« CloseSessionResponse' "$OUT" || fail "the Session was not closed on quit"
closed_after="$(metric 'andara_sessions_total{outcome="closed"}')"
authz_after="$(metric 'andara_ingress_submits_total{outcome="rejected_authz"}')"
parse_after="$(metric 'andara_ingress_submits_total{outcome="rejected_parse"}')"
python3 - "$HAS_CHARACTER" "$closed_before" "$closed_after" "$authz_before" "$authz_after" "$parse_before" "$parse_after" <<'PY'
import sys
bound = sys.argv[1] == "1"
cb, ca, ab, aa, pb, pa = (float(x) for x in sys.argv[2:])
assert ca >= cb + 1, "sessions closed did not rise: %s -> %s" % (cb, ca)
assert pa >= pb + 1, "rejected_parse did not rise (frobnicate): %s -> %s" % (pb, pa)
if not bound:
    assert aa >= ab + 3, "rejected_authz did not rise by 3 (look, north, west): %s -> %s" % (ab, aa)
PY
if [[ "$HAS_CHARACTER" == 1 ]]; then
  echo "stack-play: $CHAR_A walked from the plaza to the hall through the log, $CHAR_B watched, and both quit"
else
  echo "stack-play: the Session opened, subscribed, submitted, was refused in prose, and closed"
fi

# AC-10: under --output json, stdout is the Event stream as JSON and nothing
# else; the prose is on stderr as structured log lines.
echo "stack-play: --output json ..."
JOUT="$WORK/json-out.txt"
JERR="$WORK/json-err.txt"
printf 'look\nfrobnicate\n' | "${PLAY[@]}" --output json --log-level info >"$JOUT" 2>"$JERR" \
  || { cat "$JERR" >&2; fail "play --output json exited non-zero"; }
python3 - "$JOUT" "$JERR" "$HAS_CHARACTER" <<'PY'
import json, sys
out, err = open(sys.argv[1]).read(), open(sys.argv[2]).read()
bound = sys.argv[3] == "1"
envs = []
for line in out.splitlines():
    if line.strip():
        env = json.loads(line)          # every stdout line is an envelope
        assert isinstance(env, dict), line
        envs.append(env)
msgs = [json.loads(l)["msg"] for l in err.splitlines() if l.strip()]
assert any(m.startswith("Connected to ") for m in msgs), msgs
assert any(m.startswith("unknown verb") for m in msgs), msgs
if bound:
    assert any("room_described" in e or "roomDescribed" in e for e in envs), "no Room on stdout: %r" % envs
else:
    assert "you are not in the world" in msgs, msgs
for l in err.splitlines():
    rec = json.loads(l)
    for k in ("ts", "level", "msg", "command", "trace_id"):
        assert k in rec, (k, rec)
PY
echo "stack-play: stdout carried only JSON; the prose went to stderr as log lines"

COMPOSE="docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server"
echo "stack-play: restarting andara-server under an open session ..."
ROUT="$WORK/restart-out.txt"
RERR="$WORK/restart-err.txt"
FIFO="$WORK/stdin"
mkfifo "$FIFO"
"${PLAY[@]}" --show-protocol <"$FIFO" >"$ROUT" 2>"$RERR" &
PLAYPID=$!
exec 3>"$FIFO"
wait_for() {  # wait_for <pattern> <seconds>
  for _ in $(seq 1 "$(( $2 * 2 ))"); do
    grep -q "$1" "$ROUT" && return 0
    kill -0 "$PLAYPID" 2>/dev/null || { cat "$RERR" >&2; fail "play exited early"; }
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
wait "$PLAYPID"
rcode=$?
set -e
sed 's/^/  | /' "$ROUT"
[[ "$rcode" == "0" ]] || { cat "$RERR" >&2; fail "play exited $rcode after the restart, want 0"; }
[[ "$(grep -o 'session_id=[0-9a-f]\{32\}' "$ROUT" | sort -u | wc -l)" -ge 2 ]] || fail "the reconnect did not open a new Session"
[[ "$(grep -c '» Submit .*raw="look"' "$ROUT")" -ge 3 ]] || fail "the client did not look again on the new Session"
! grep -q 'Something happened\|not answered\|not sent' "$ROUT" || fail "something false was printed during the restart"
# The stack is back for whatever runs next. Readiness follows recovery, which
# replays the log from its beginning until AW-SRV-006 snapshots it: a stack
# that has run for days takes a minute or more here.
for _ in $(seq 1 180); do
  curl -sf "http://localhost:${HTTP_PORT}/readyz" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://localhost:${HTTP_PORT}/readyz" >/dev/null || fail "andara-server not ready after the restart"
echo "stack-play: the connection loss was announced, a Session reopened within the backoff, and the Room asked for again"

# AC-12: the client says what it speaks. The server's range cannot be moved
# from here; the mismatch itself is unit-tested (TestPlay_VersionMismatch).
play_help="$(bin/andara-cli play --help)"
grep -q -- '--show-protocol' <<<"$play_help" || fail "play --help lacks --show-protocol"

if [[ "$HAS_CHARACTER" == 1 ]]; then
  # AW-CLI-007 AC-8: the reconnect selected the Character again on the new Session.
  [[ "$(grep -c '» SelectCharacter ' "$ROUT")" -ge 2 ]] || fail "the reconnect did not select the Character again"
  echo "stack-play: M1 gate — both halves, a bound Character walking in play's own transcript — passes"
else
  echo "stack-play: M1 gate — the Protocol half whole, the play half up to AW-CLI-007 — passes"
fi
