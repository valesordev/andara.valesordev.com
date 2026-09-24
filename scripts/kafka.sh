#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Apache Kafka on the box for dev and prod, via Strimzi (AW-INF-014).
#
#   kafka.sh operator          make kafka-operator: Strimzi into `strimzi`, once per cluster
#   kafka.sh install <env>     make kafka-install: operator, namespace, deploy/k8s/kafka/*.yaml,
#                              wait Ready, then deploy/kafka/topics.yaml through topics.py
#   kafka.sh bounce <env>      make kafka-broker-bounce: lose one broker, prove the server never
#                              left service while it was gone (AC-5)
#
# local has no cluster broker by design (values/local.yaml runs broker-free), so the
# environment commands take dev or prod only.
#
# Exit: 0 ok · 1 a step failed, or the bounce degraded the server · 2 bad usage or env ·
# 3 a tool is missing.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

STRIMZI_VERSION="1.2.0"                   # Kafka 4.3.1, KRaft only, CRD API v1
STRIMZI_REPO="https://strimzi.io/charts/"
WATCHED=(andara-dev andara-prod)
KAFKA="andara-log"
PY="${PY:-python3}"

die() { echo "make: kafka-$1: $2" >&2; exit "${3:-1}"; }

need() {
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || die "$CMD" "$tool not found" 3
  done
}

env_ns() {
  case "${1:-}" in
    dev|prod) echo "andara-$1" ;;
    local) die "$CMD" "local has no cluster broker (values/local.yaml runs broker-free); ENV=dev or ENV=prod" 2 ;;
    *) die "$CMD" "usage: make kafka-$CMD ENV=<dev|prod>" 2 ;;
  esac
}

ensure_ns() {
  kubectl get namespace "$1" >/dev/null 2>&1 || kubectl create namespace "$1" >/dev/null
}

operator() {
  need helm kubectl
  # The chart binds the operator into each watched namespace, so they must exist first.
  for ns in "${WATCHED[@]}"; do ensure_ns "$ns"; done
  local have
  have="$(helm list -n strimzi -o json 2>/dev/null \
    | "$PY" -c 'import json,sys; r=[x for x in json.load(sys.stdin) if x["name"]=="strimzi"]; print(r[0]["chart"] if r else "")')"
  if [[ "$have" == "strimzi-kafka-operator-$STRIMZI_VERSION" ]]; then
    echo "kafka-operator: strimzi $STRIMZI_VERSION already installed (watching ${WATCHED[*]})"
    return
  fi
  local csv
  csv="$(IFS=,; echo "${WATCHED[*]}")"
  helm upgrade --install strimzi strimzi-kafka-operator \
    --repo "$STRIMZI_REPO" --version "$STRIMZI_VERSION" \
    --namespace strimzi --create-namespace \
    --set "watchNamespaces={$csv}" \
    --wait --timeout 5m >/dev/null
  echo "kafka-operator: strimzi $STRIMZI_VERSION in namespace strimzi, watching ${WATCHED[*]}"
}

install() {
  local env="$1" ns
  ns="$(env_ns "$env")"
  need helm kubectl "$PY"
  operator
  ensure_ns "$ns"
  kubectl -n "$ns" apply -f deploy/k8s/kafka/ >/dev/null
  echo "kafka-install: applied deploy/k8s/kafka/ to $ns; waiting for kafka/$KAFKA (first boot pulls images and forms the KRaft quorum)"
  kubectl -n "$ns" wait "kafka/$KAFKA" --for=condition=Ready --timeout=15m >/dev/null \
    || die install "kafka/$KAFKA in $ns did not become Ready: kubectl -n $ns describe kafka $KAFKA"
  kubectl -n "$ns" rollout status deploy/andara-kafka-tools --timeout=5m >/dev/null \
    || die install "the rpk toolbox in $ns did not become Available"
  "$PY" scripts/topics.py apply --env "$env"
  echo "kafka-install: $ns brokers:"
  kubectl -n "$ns" get pods -l "strimzi.io/cluster=$KAFKA,strimzi.io/pool-name=broker" \
    -o custom-columns=POD:.metadata.name,NODE:.spec.nodeName,READY:.status.containerStatuses[0].ready --no-headers
}

# The unavailable-submit counter from the server's own /metrics: 0 when it has never
# moved, "?" when /metrics could not be read (not a zero).
unavailable_count() {
  local body
  body="$(kubectl -n "$1" exec andara-0 -c server -- wget -qO- localhost:8080/metrics 2>/dev/null)" \
    || { echo "?"; return; }
  awk '/^andara_ingress_submits_total\{.*outcome="unavailable".*\}/{v=$2} END{print v+0}' <<<"$body"
}

ready_since() {
  kubectl -n "$1" get pod andara-0 \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status} {.status.conditions[?(@.type=="Ready")].lastTransitionTime} {.status.containerStatuses[?(@.name=="server")].restartCount}'
}

# Under-replicated partitions, counted by a broker other than the one that was lost.
# "?" when the count could not be taken: an unanswered question is not a zero.
under_replicated() {
  local out
  out="$(kubectl -n "$1" exec "$KAFKA-broker-1" -c kafka -- \
    /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --under-replicated-partitions 2>/dev/null)" \
    || { echo "?"; return; }
  grep -c 'Topic:' <<<"$out" || true
}

bounce() {
  local env="$1" ns victim="$KAFKA-broker-0"
  ns="$(env_ns "$env")"
  need kubectl
  read -r ready0 since0 restarts0 < <(ready_since "$ns") \
    || die broker-bounce "no andara-0 in $ns to observe; run \`make helm-install ENV=$env\` first"
  [[ "$ready0" == "True" ]] || die broker-bounce "andara-0 in $ns is not Ready before the bounce"
  local unavail0 uid0 t0
  unavail0="$(unavailable_count "$ns")"
  [[ "$unavail0" != "?" ]] || die broker-bounce "could not read andara-0's /metrics before the bounce"
  uid0="$(kubectl -n "$ns" get pod "$victim" -o jsonpath='{.metadata.uid}')" \
    || die broker-bounce "no $victim in $ns; run \`make kafka-install ENV=$env\` first"
  t0="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  kubectl -n "$ns" delete pod "$victim" --wait=false >/dev/null
  echo "kafka-broker-bounce: deleted $victim at $t0; waiting for the checkpoint"

  # Rule 1 of docs/specs/testing/live-assertions.md: poll to a deadline, and only for the
  # checkpoint — the replacement broker Ready and every partition back in sync. Nothing
  # about the server is sampled in this loop.
  local deadline=$((SECONDS + 600)) uid ready urp
  while :; do
    uid="$(kubectl -n "$ns" get pod "$victim" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
    ready="$(kubectl -n "$ns" get pod "$victim" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || true)"
    if [[ -n "$uid" && "$uid" != "$uid0" && "$ready" == "true" ]]; then
      urp="$(under_replicated "$ns")"
      [[ "$urp" == "0" ]] && break
    fi
    ((SECONDS < deadline)) || die broker-bounce "no checkpoint within 10 minutes: $victim ready=${ready:-?}, under-replicated=${urp:-?}"
    sleep 3
  done
  local t1
  t1="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "kafka-broker-bounce: checkpoint at $t1 — $victim rejoined, no under-replicated partitions"

  # Rule 3: absence over [t0, t1] is asserted once, after the checkpoint, from records that
  # cannot skip a transition — never from the andara_ingress_degraded gauge.
  local fail=0 ready1 since1 restarts1 unreachable unavail1
  read -r ready1 since1 restarts1 < <(ready_since "$ns")
  if [[ "$ready1" != "True" || "$since1" != "$since0" || "$restarts1" != "$restarts0" ]]; then
    echo "kafka-broker-bounce: FAIL andara-0 left Ready in the window (Ready=$ready1 since $since1, was $since0; restarts $restarts0 -> $restarts1)" >&2
    fail=1
  fi
  local log
  if ! log="$(kubectl -n "$ns" logs andara-0 -c server --since-time="$t0" 2>&1)"; then
    echo "kafka-broker-bounce: FAIL could not read andara-0's log since $t0: $log" >&2
    fail=1
  fi
  unreachable="$(grep -c 'command log unreachable' <<<"$log" || true)"
  if [[ "$unreachable" != "0" ]]; then
    echo "kafka-broker-bounce: FAIL the server logged 'command log unreachable' $unreachable time(s) since $t0" >&2
    fail=1
  fi
  unavail1="$(unavailable_count "$ns")"
  if [[ "$unavail1" != "$unavail0" ]]; then
    echo "kafka-broker-bounce: FAIL andara_ingress_submits_total{outcome=\"unavailable\"} moved $unavail0 -> $unavail1" >&2
    fail=1
  fi
  ((fail == 0)) || exit 1
  echo "kafka-broker-bounce: ok — over [$t0, $t1] andara-0 stayed Ready (since $since0, restarts $restarts0), logged no degradation, refused no Submit"
}

CMD="${1:-}"
case "$CMD" in
  operator) operator ;;
  install) install "${2:-}" ;;
  bounce) CMD=broker-bounce; bounce "${2:-}" ;;
  *) echo "usage: kafka.sh <operator|install|bounce> [env]" >&2; exit 2 ;;
esac
