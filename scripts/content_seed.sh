#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# make content-seed ENV=<env> — publish and activate the dev fixture as pack `town` in ENV's
# content store (AW-INF-021). Idempotent: if `town` has any active version, whoever published
# it, nothing is published, so a Builder who has taken `town` over is never overwritten.
#
# Product commands only (the story's Interface contract; CLAUDE.md §10): `andara-cli content
# history`, `content publish` and `content activate --override`, as the bootstrap operator.
# No topic is written directly.
#
# Steps, each printed as `content-seed: <step>`, ending in exactly one of:
#   content-seed: town@<m> published and active
#   content-seed: town@<m> is already active; nothing published
#   content-seed: ANDARA_BOOTSTRAP_OPERATOR is not set
#
# A version published by an earlier run that stopped before activating isn't published again:
# when `town` has versions but none active, and the newest is the operator's, it's activated.
#
# The route (architecture's ruling on the empty-store deadlock, docs/feedback/
# AW-INF-021-dev-content-store.md): a server whose World has never had content stays up and
# unready, serving Admin, and the Service and the edge route only to Ready pods. So the seed
# dials andara-0 itself through a `kubectl port-forward` to its gRPC port. It verifies the pod's
# certificate under its in-cluster name with `--tls-server-name andara-0.andara.<ns>.svc`,
# against the CA in the server's TLS Secret (its `ca.crt` only). The same route works for a
# Ready server, so there's one path.
#
# Environment: ANDARA_BOOTSTRAP_OPERATOR (user:password), required. CONTENT_SEED_TIMEOUT
# (default 30s), the deadline for `server info` to show the activated version, polled per
# docs/specs/testing/live-assertions.md. ANDARA_SEED_ADDRESS skips the port-forward and dials
# that address instead, with ANDARA_SEED_CA and ANDARA_SEED_SERVER_NAME (tests).
#
# Exit: 0 seeded or already seeded · 1 a precondition or a command failed · 2 usage.

set -euo pipefail
cd "$(dirname "$0")/.."

ENVNAME="${1:-}"
PACK=town
FIXTURE=content/fixtures/town
REASON="dev fixture"

say() { echo "content-seed: $*"; }
usage() { say "$*" >&2; exit 2; }
fail() { say "$*" >&2; exit 1; }

case "$ENVNAME" in
  "") usage "ENV is required: make content-seed ENV=<dev>" ;;
  prod) usage "prod is not seeded with the fixture" ;;
  local) usage "local reads content from the directory (content.source=dir); nothing to seed" ;;
  dev) ;;
  *) usage "unknown ENV '$ENVNAME'; want dev" ;;
esac

# AC-9: no credential, no RPC.
[[ -n "${ANDARA_BOOTSTRAP_OPERATOR:-}" ]] || fail "ANDARA_BOOTSTRAP_OPERATOR is not set"
[[ "$ANDARA_BOOTSTRAP_OPERATOR" == *:* ]] || fail "ANDARA_BOOTSTRAP_OPERATOR must be user:password"
OP_USER="${ANDARA_BOOTSTRAP_OPERATOR%%:*}"
OP_PASS="${ANDARA_BOOTSTRAP_OPERATOR#*:}"

CLI="${ANDARA_CLI:-bin/andara-cli}"
[[ -x "$CLI" ]] || fail "no $CLI; run \`make build\` first"
command -v jq >/dev/null 2>&1 || fail "jq not found"
[[ -d "$FIXTURE" ]] || fail "no $FIXTURE"

NS="andara-$ENVNAME"
SERVER_NAME="${ANDARA_SEED_SERVER_NAME:-andara-0.andara.$NS.svc}"

# An isolated home: the operator's credential must not land in the developer's.
WORK="$(mktemp -d -t content-seed.XXXXXX)"
PF=""
cleanup() { [[ -z "$PF" ]] || kill "$PF" 2>/dev/null || true; rm -rf "$WORK"; }
trap cleanup EXIT
export XDG_CONFIG_HOME="$WORK/config" XDG_STATE_HOME="$WORK/state"
mkdir -p "$XDG_CONFIG_HOME/andara"

if [[ -n "${ANDARA_SEED_ADDRESS:-}" ]]; then
  ADDRESS="$ANDARA_SEED_ADDRESS"
  CA="${ANDARA_SEED_CA:-}"
else
  command -v kubectl >/dev/null 2>&1 || fail "kubectl not found"
  say "reading the server CA from $NS/andara-server-tls"
  CA="$WORK/ca.pem"
  kubectl -n "$NS" get secret andara-server-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > "$CA" 2>/dev/null \
    && [[ -s "$CA" ]] || fail "no ca.crt in $NS/andara-server-tls"
  PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  say "port-forwarding 127.0.0.1:$PORT to $NS/andara-0 (gRPC)"
  kubectl -n "$NS" port-forward pod/andara-0 "$PORT:grpc" >"$WORK/pf.log" 2>&1 &
  PF=$!
  for _ in $(seq 1 30); do
    grep -q '^Forwarding from' "$WORK/pf.log" && break
    kill -0 "$PF" 2>/dev/null || fail "port-forward to $NS/andara-0 failed: $(tail -1 "$WORK/pf.log")"
    sleep 0.5
  done
  grep -q '^Forwarding from' "$WORK/pf.log" || fail "port-forward to $NS/andara-0 never became ready"
  ADDRESS="127.0.0.1:$PORT"
fi
{
  printf 'server:\n  address: %s\n  tls_server_name: %s\n' "$ADDRESS" "$SERVER_NAME"
  [[ -z "$CA" ]] || printf '  tls_ca: %s\n' "$CA"
} > "$WORK/cli.yaml"
export ANDARA_CONFIG="$WORK/cli.yaml"
cli() { "$CLI" --output json "$@"; }

say "logging in to $ADDRESS (verifying $SERVER_NAME) as $OP_USER"
printf '%s\n' "$OP_PASS" | "$CLI" auth login --username "$OP_USER" --password-stdin >/dev/null \
  || fail "login as $OP_USER at $ADDRESS failed"

say "reading $PACK's history"
hist="$(cli content history "$PACK")" || fail "content history $PACK failed: $hist"
active="$(jq -r '.active_version // 0' <<<"$hist")"
if [[ "$active" != "0" ]]; then
  say "$PACK@$active is already active; nothing published"
  exit 0
fi

newest="$(jq -r '[.versions[]?] | max_by(.version) // {} | .version // 0' <<<"$hist")"
newest_author="$(jq -r '[.versions[]?] | max_by(.version) // {} | .author // ""' <<<"$hist")"
operator_id="$(cli auth whoami | jq -r '.account_id // empty' 2>/dev/null || true)"

if [[ "$newest" != "0" && -n "$operator_id" && "$newest_author" == "$operator_id" ]]; then
  version="$newest"
  say "$PACK@$version was published by an earlier run and never activated; activating it"
else
  say "publishing $FIXTURE as $PACK"
  pub="$(cli content publish --path "$FIXTURE" --pack "$PACK")" || fail "content publish failed: $pub"
  version="$(jq -r '.version' <<<"$pub")"
  [[ "$version" =~ ^[0-9]+$ && "$version" != "0" ]] || fail "content publish returned no version: $pub"
  say "$PACK@$version published ($(jq -r '.blobs_uploaded' <<<"$pub") of $(jq -r '.blobs_total' <<<"$pub") blobs uploaded)"
fi

say "activating $PACK@$version with --override (reason: $REASON)"
act="$(cli content activate "$PACK" "$version" --override --reason "$REASON" --yes)" \
  || fail "content activate $PACK $version failed: $act"

# The swap is applied after content.reload_debounce; poll server info to a deadline.
deadline=$(( $(date +%s) + $(sed 's/s$//' <<<"${CONTENT_SEED_TIMEOUT:-30s}") ))
while :; do
  info="$(cli server info 2>/dev/null || true)"
  if jq -e --arg p "$PACK" --argjson v "$version" '.content[]? | select(.pack == $p and .version == $v)' <<<"$info" >/dev/null 2>&1; then
    break
  fi
  (( $(date +%s) < deadline )) || fail "server info did not list $PACK@$version within ${CONTENT_SEED_TIMEOUT:-30s}"
  sleep 1
done
say "$PACK@$version published and active"
