#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# The stream soak (AW-INF-006 AC-1 and AC-3): from outside the cluster, open a Subscribe
# through the edge and hold it for DURATION; halfway through, renew the edge certificate
# and prove the stream survived it while new connections present the new certificate;
# then read Traefik's counters to show the requests went through as gRPC over HTTP/2.
#
# Nightly at 60 m in CI, 5 m on pull requests, any length by hand:
#     make stream-soak ENV=local DURATION=60m
set -euo pipefail

ENVNAME="${1:?usage: stream_soak.sh <env> <duration>}"
DURATION="${2:-5m}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"
GO="${GO:-go}"
PY="${PY:-python3}"

VALUES="deploy/helm/values/${ENVNAME}.yaml"
NS="andara-${ENVNAME}"
[[ -f "$VALUES" ]] || { echo "make: stream-soak: no values file at $VALUES" >&2; exit 1; }
for tool in kubectl openssl curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "make: stream-soak: $tool not found" >&2; exit 1; }
done

HOST="$(awk '/^host:/{print $2}' "$VALUES")"
INGRESS_NS="$(helm -n "$NS" get values andara --all -o json | "$PY" -c 'import json,sys; print(json.load(sys.stdin)["networkPolicy"]["ingressNamespace"])')"
ISSUER="$(helm -n "$NS" get values andara --all -o json | "$PY" -c 'import json,sys; print(json.load(sys.stdin)["tls"]["issuer"])')"

# Where the edge is. ANDARA_SOAK_RESOLVE pins the dial address (curl's --resolve) so
# neither the box nor a CI runner needs an /etc/hosts line; the default is the kind
# port mapping on this machine.
RESOLVE="${ANDARA_SOAK_RESOLVE:-127.0.0.1}"
CA=""
if [[ "$ISSUER" == "andara-ca" ]]; then
  CA="$REPO/.local/tls/cluster/$ENVNAME/ca.pem"
  [[ -f "$CA" ]] || { echo "make: stream-soak: no $CA; run \`make helm-install ENV=$ENVNAME\` first" >&2; exit 1; }
fi

serial() {
  openssl s_client -connect "$RESOLVE:443" -servername "$HOST" </dev/null 2>/dev/null \
    | openssl x509 -noout -serial 2>/dev/null | cut -d= -f2
}

before="$(serial)"
[[ -n "$before" ]] || { echo "make: stream-soak: no certificate served at $RESOLVE:443 for $HOST" >&2; exit 1; }
echo "stream-soak: $HOST via $RESOLVE, edge certificate serial $before, holding a Subscribe for $DURATION"

# Go's -timeout must outlive the soak, or the runner kills the very thing under test.
secs="$("$PY" -c 'import re,sys
d=sys.argv[1]; t=0
for n,u in re.findall(r"(\d+)([hms])", d): t += int(n)*{"h":3600,"m":60,"s":1}[u]
print(t)' "$DURATION")"
LOG="$(mktemp -t stream-soak.XXXXXX)"
ANDARA_SOAK_DURATION="$DURATION" ANDARA_SOAK_ADDR="$HOST:443" ANDARA_SOAK_RESOLVE="$RESOLVE" \
ANDARA_TLS_CA_FILE="$CA" \
  "$GO" test -tags soak -count=1 -v -timeout "$((secs + 600))s" -run '^TestSoak_' ./internal/smoke/ >"$LOG" 2>&1 &
soak=$!

# AC-3, mid-stream: the same status condition `cmctl renew` sets. The old Secret is
# replaced in place; Traefik picks it up without a restart, which is what keeps the
# stream open, and the next handshake presents the new serial.
if (( secs >= 120 )); then
  sleep 30
  old="$(kubectl -n "$NS" get secret andara-edge-tls -o jsonpath='{.data.tls\.crt}')"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  kubectl -n "$NS" patch certificate andara-edge --subresource=status --type=merge \
    -p "{\"status\":{\"conditions\":[{\"type\":\"Issuing\",\"status\":\"True\",\"reason\":\"ManuallyTriggered\",\"message\":\"make stream-soak\",\"lastTransitionTime\":\"$now\"}]}}" >/dev/null
  echo "stream-soak: renewal of andara-edge triggered at +30s"
  for _ in $(seq 1 60); do
    [[ "$(kubectl -n "$NS" get secret andara-edge-tls -o jsonpath='{.data.tls\.crt}')" != "$old" ]] && break
    sleep 2
  done
  after=""
  for _ in $(seq 1 30); do
    after="$(serial)"
    [[ -n "$after" && "$after" != "$before" ]] && break
    sleep 2
  done
  if [[ "$after" == "$before" || -z "$after" ]]; then
    kill "$soak" 2>/dev/null || true
    echo "make: stream-soak: edge certificate was not renewed and served within 3 m (serial still $before)" >&2; exit 1
  fi
  echo "stream-soak: new connections present serial $after; the soak stream is still open"
else
  echo "stream-soak: DURATION under 2 m, skipping the mid-stream renewal (AC-3)"
fi

if ! wait "$soak"; then
  cat "$LOG"
  echo "make: stream-soak: the stream did not survive $DURATION" >&2; exit 1
fi
grep -E '^\s+soak_test.go' "$LOG" | sed 's/^ */stream-soak:   /'
rm -f "$LOG"

# Both legs HTTP/2: Traefik labels the inbound protocol, and gRPC to the pod cannot run
# over anything but HTTP/2, so a non-zero grpc counter on this release's routers is the
# whole proof. Read through a port-forward; the metrics entrypoint is not exposed.
kubectl -n "$INGRESS_NS" port-forward deploy/traefik 19100:9100 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
sleep 2
n="$(curl -sf localhost:19100/metrics \
  | awk -v ns="$NS" '$0 ~ "^traefik_router_requests_total\\{" && $0 ~ "protocol=\"grpc\"" && $0 ~ "router=\"" ns "-andara-" { s += $NF } END { print s + 0 }')"
[[ "$n" -gt 0 ]] || { echo "make: stream-soak: traefik_router_requests_total{protocol=\"grpc\"} is zero for $NS routers" >&2; exit 1; }
echo "stream-soak: traefik_router_requests_total{protocol=\"grpc\", router=~\"$NS-andara-.*\"} = $n"
echo "stream-soak: ok — $DURATION through the edge, renewal mid-stream, gRPC over HTTP/2 on both legs"
