#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Apache Kafka on the box for dev and prod, via Strimzi (AW-INF-014).
#
#   kafka.sh install <env>     make kafka-install: namespace, deploy/k8s/kafka/*.yaml, wait Ready,
#                              then deploy/kafka/topics.yaml through topics.py. The Strimzi operator
#                              is `make platform-up`'s (AW-INF-054), and must watch the namespace.
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

KAFKA="andara-log"
PY="${PY:-python3}"
# Every call names its cluster: ANDARA_KUBE_CONTEXT, default kind-andara (AW-INF-053).
. scripts/kube.sh

die() { echo "make: kafka-$1: $2" >&2; exit "${3:-1}"; }

need() {
  local tool
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
  kube_kubectl get namespace "$1" >/dev/null 2>&1 || kube_kubectl create namespace "$1" >/dev/null
}

# Strimzi is the platform's (AW-INF-054): `make platform-up` is its only installer, watching
# andara-shared alone. An operator that doesn't watch ns would never reconcile the Kafka applied
# there, and kafka-install would wait out its 15 minutes, so it refuses first.
require_operator() {
  local ns="$1" watching
  watching="$(kube_helm get values strimzi -n strimzi -o json 2>/dev/null \
    | "$PY" -c 'import json,sys; v=json.load(sys.stdin) or {}; print(" ".join(v.get("watchNamespaces") or []))' 2>/dev/null || true)"
  [[ -n "$watching" ]] || die install "no Strimzi release in namespace strimzi; run \`make platform-up\`"
  [[ " $watching " == *" $ns "* ]] \
    || die install "Strimzi watches $watching, not $ns; the shared broker in andara-shared is AW-INF-055's"
}

install() {
  local env="$1" ns
  ns="$(env_ns "$env")"
  need helm kubectl "$PY"
  kube_require kafka-install
  require_operator "$ns"
  ensure_ns "$ns"
  # Never apply to a namespace other than the one ENV names. A bare `ns` in a helper once
  # overwrote this one (bash scoping is dynamic) and applied dev's install to andara-prod.
  [[ "$ns" == "andara-$env" ]] || die install "internal error: target namespace is $ns, not andara-$env"
  kube_kubectl -n "$ns" apply -f deploy/k8s/kafka/ >/dev/null
  echo "kafka-install: applied deploy/k8s/kafka/ to $ns; waiting for kafka/$KAFKA (first boot pulls images and forms the KRaft quorum)"
  kube_kubectl -n "$ns" wait "kafka/$KAFKA" --for=condition=Ready --timeout=15m >/dev/null \
    || die install "kafka/$KAFKA in $ns did not become Ready: kubectl --context $ANDARA_KUBE_CONTEXT -n $ns describe kafka $KAFKA"
  kube_kubectl -n "$ns" rollout status deploy/andara-kafka-tools --timeout=5m >/dev/null \
    || die install "the rpk toolbox in $ns did not become Available"
  "$PY" scripts/topics.py apply --env "$env"
  echo "kafka-install: $ns brokers:"
  kube_kubectl -n "$ns" get pods -l "strimzi.io/cluster=$KAFKA,strimzi.io/pool-name=broker" \
    -o custom-columns=POD:.metadata.name,NODE:.spec.nodeName,READY:.status.containerStatuses[0].ready --no-headers
}

# The unavailable-submit counter from the server's own /metrics: 0 when it has never
# moved, "?" when /metrics could not be read (not a zero).
unavailable_count() {
  local body
  body="$(kube_kubectl -n "$1" exec andara-0 -c server -- wget -qO- localhost:8080/metrics 2>/dev/null)" \
    || { echo "?"; return; }
  awk '/^andara_ingress_submits_total\{.*outcome="unavailable".*\}/{v=$2} END{print v+0}' <<<"$body"
}

ready_since() {
  kube_kubectl -n "$1" get pod andara-0 \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status} {.status.conditions[?(@.type=="Ready")].lastTransitionTime} {.status.containerStatuses[?(@.name=="server")].restartCount}{"\n"}'
}

# Under-replicated partitions, counted by a broker other than the one that was lost.
# "?" when the count could not be taken: an unanswered question is not a zero.
under_replicated() {
  local out
  out="$(kube_kubectl -n "$1" exec "$KAFKA-broker-1" -c kafka -- \
    /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --under-replicated-partitions 2>/dev/null)" \
    || { echo "?"; return; }
  grep -c 'Topic:' <<<"$out" || true
}

bounce() {
  local env="$1" ns victim="$KAFKA-broker-0"
  # dev's broker is the shared one in andara-shared since AW-INF-059 (values/dev.yaml); bouncing
  # it is AW-INF-055's to define, since local's server shares it.
  [[ "$env" != "dev" ]] \
    || die broker-bounce "ENV=dev: dev's broker is the shared andara-log in andara-shared; its bounce is AW-INF-055's. ENV=prod" 2
  ns="$(env_ns "$env")"
  need kubectl
  kube_require kafka-broker-bounce
  read -r ready0 since0 restarts0 < <(ready_since "$ns") \
    || die broker-bounce "no andara-0 in $ns to observe; run \`make helm-install ENV=$env$([[ $env == dev ]] && echo " TAG=<sha-…@sha256:…>")\` first"
  [[ "$ready0" == "True" ]] || die broker-bounce "andara-0 in $ns is not Ready before the bounce"
  local unavail0 uid0 t0
  unavail0="$(unavailable_count "$ns")"
  [[ "$unavail0" != "?" ]] || die broker-bounce "could not read andara-0's /metrics before the bounce"
  uid0="$(kube_kubectl -n "$ns" get pod "$victim" -o jsonpath='{.metadata.uid}')" \
    || die broker-bounce "no $victim in $ns; run \`make kafka-install ENV=$env\` first"
  t0="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  kube_kubectl -n "$ns" delete pod "$victim" --wait=false >/dev/null
  echo "kafka-broker-bounce: deleted $victim at $t0; waiting for the checkpoint"

  # Rule 1 of docs/specs/testing/live-assertions.md: poll to a deadline, and only for the
  # checkpoint — the replacement broker Ready and every partition back in sync. Nothing
  # about the server is sampled in this loop.
  local deadline=$((SECONDS + 600)) uid ready urp
  while :; do
    uid="$(kube_kubectl -n "$ns" get pod "$victim" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
    ready="$(kube_kubectl -n "$ns" get pod "$victim" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || true)"
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
  if ! log="$(kube_kubectl -n "$ns" logs andara-0 -c server --since-time="$t0" 2>&1)"; then
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
  install) install "${2:-}" ;;
  bounce) CMD=broker-bounce; bounce "${2:-}" ;;
  *) echo "usage: kafka.sh <install|bounce> [env]" >&2; exit 2 ;;
esac
