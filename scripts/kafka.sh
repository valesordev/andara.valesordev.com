#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Apache Kafka on the box, via Strimzi (AW-INF-014): one cluster in andara-shared, shared by
# local and dev, each with its own prefixed topics (ADR-0013 decisions 3–5, AW-INF-055).
#
#   kafka.sh up                make shared-up: the application namespaces with their
#                              andara.valesor/env label, deploy/k8s/kafka/*.yaml in andara-shared,
#                              wait Ready, the KafkaUsers rendered from topics.yaml (AW-INF-056),
#                              both environments' topics from deploy/kafka/topics.yaml,
#                              the platform topics and the schema registry (AW-INF-057), then
#                              the shared object store (`make objectstore-up` and each
#                              environment's `objectstore-install`, AW-INF-058).
#   kafka.sh install <env>     make kafka-install: the same Kafka, then only <env>'s topics.
#   kafka.sh bounce <env>      make kafka-broker-bounce: lose one broker, prove <env>'s server never
#                              left service while it was gone (AW-INF-014 AC-5)
#
# The Strimzi operator is `make platform-up`'s (AW-INF-054), and must watch andara-shared.
# Nothing here installs a per-namespace Kafka any more, and prod's is not on this cluster
# (ADR-0013 decision 10), so the environments are local and dev.
#
# Exit: 0 ok · 1 a step failed, or the bounce degraded the server · 2 bad usage or env ·
# 3 a tool or the cluster context is missing.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

KAFKA="andara-log"
PY="${PY:-python3}"
# Every call names its cluster: ANDARA_KUBE_CONTEXT, default kind-andara (AW-INF-053).
. scripts/kube.sh

die() { echo "make: $1: $2" >&2; exit "${3:-1}"; }

need() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || die "$CMD" "$tool not found" 3
  done
}

# The shared Kafka's namespace: ANDARA_KAFKA_NAMESPACE, as topics.py reads it (ADR-0013
# amendment 23).
KAFKA_NS="${ANDARA_KAFKA_NAMESPACE:-andara-shared}"
# The application namespaces the listener admits, and the label value each carries.
ENVS=(local dev)
ENV_LABEL="andara.valesor/env"

check_env() {
  case "${1:-}" in
    local|dev) ;;
    prod) die "$CMD" "prod's Kafka is not on this cluster (ADR-0013 decision 10); ENV=local or ENV=dev" 2 ;;
    *) die "$CMD" "usage: make $CMD ENV=<local|dev>" 2 ;;
  esac
}

# Each application namespace exists and carries its environment label, which is what the
# listener's NetworkPolicy admits (kafka.yaml). It also lets AW-INF-066 restore dev's
# certificate Secret into an empty andara-dev before Argo CD syncs. Idempotent: an existing
# namespace keeps everything but the label's value.
ensure_env_namespaces() {
  local env ns
  for env in "${ENVS[@]}"; do
    ns="andara-$env"
    kube_kubectl get namespace "$ns" >/dev/null 2>&1 || kube_kubectl create namespace "$ns" >/dev/null
    kube_kubectl label namespace "$ns" "$ENV_LABEL=$env" --overwrite >/dev/null
    echo "shared: namespace $ns labelled $ENV_LABEL=$env"
  done
}

# Strimzi is the platform's (AW-INF-054): `make platform-up` is its only installer, watching
# andara-shared alone. An operator that doesn't watch the namespace would never reconcile the
# Kafka applied there, and this would wait out its 15 minutes, so it refuses first.
require_operator() {
  local watching
  watching="$(kube_helm get values strimzi -n strimzi -o json 2>/dev/null \
    | "$PY" -c 'import json,sys; v=json.load(sys.stdin) or {}; print(" ".join(v.get("watchNamespaces") or []))' 2>/dev/null || true)"
  [[ -n "$watching" ]] || die "$CMD" "no Strimzi release in namespace strimzi; run \`make platform-up\`" 3
  [[ " $watching " == *" $KAFKA_NS "* ]] \
    || die "$CMD" "Strimzi watches $watching, not $KAFKA_NS; run \`make platform-up\`" 3
}

# The broker moving to SASL (AW-INF-056) while the schema registry still runs on the
# unauthenticated listener: stop the registry first, so the switch never denies a running client;
# schema_registry starts it again on SASL. Nothing on a broker already on SASL, or a new one.
sasl_switch() {
  local auth
  kube_kubectl -n "$KAFKA_NS" get "kafka/$KAFKA" >/dev/null 2>&1 || return 0
  auth="$(kube_kubectl -n "$KAFKA_NS" get "kafka/$KAFKA" \
    -o jsonpath='{.spec.kafka.listeners[?(@.port==9092)].authentication.type}')"
  [[ -z "$auth" ]] || return 0
  kube_kubectl -n "$KAFKA_NS" get deploy/andara-schema-registry >/dev/null 2>&1 || return 0
  echo "shared: stopping the schema registry while kafka/$KAFKA moves to SASL (AW-INF-056); it starts again on SASL below"
  kube_kubectl -n "$KAFKA_NS" scale deploy/andara-schema-registry --replicas=0 >/dev/null
  REGISTRY_STOPPED=1
}
REGISTRY_STOPPED=0

# The Kafka itself, idempotent: a second run applies nothing new and waits on a Ready cluster.
shared_kafka() {
  need helm kubectl "$PY"
  kube_require "$CMD"
  require_operator
  kube_kubectl get namespace "$KAFKA_NS" >/dev/null 2>&1 \
    || die "$CMD" "no namespace $KAFKA_NS; run \`make platform-up\`, which creates it before Strimzi" 3
  ensure_env_namespaces
  sasl_switch
  kube_kubectl -n "$KAFKA_NS" apply -f deploy/k8s/kafka/ >/dev/null
  echo "shared: applied deploy/k8s/kafka/ to $KAFKA_NS; waiting for kafka/$KAFKA (first boot pulls images and forms the KRaft quorum)"
  kube_kubectl -n "$KAFKA_NS" wait "kafka/$KAFKA" --for=condition=Ready --timeout=15m >/dev/null \
    || die "$CMD" "kafka/$KAFKA in $KAFKA_NS did not become Ready: kubectl --context $ANDARA_KUBE_CONTEXT -n $KAFKA_NS describe kafka $KAFKA"
  kube_kubectl -n "$KAFKA_NS" rollout status deploy/andara-kafka-tools --timeout=5m >/dev/null \
    || die "$CMD" "the rpk toolbox in $KAFKA_NS did not become Available"
  # Every principal's credential and ACLs, before anything authenticates: the operator's first,
  # which every topic apply below uses.
  ANDARA_KAFKA_NAMESPACE="$KAFKA_NS" "$PY" scripts/kafka_users.py apply \
    || die "$CMD" "the KafkaUsers from deploy/kafka/topics.yaml were not all Ready in $KAFKA_NS"
}

brokers() {
  echo "shared: $KAFKA_NS brokers:"
  kube_kubectl -n "$KAFKA_NS" get pods -l "strimzi.io/cluster=$KAFKA,strimzi.io/pool-name=broker" \
    -o custom-columns=POD:.metadata.name,NODE:.spec.nodeName,READY:.status.containerStatuses[0].ready --no-headers
}

topics_for() {
  local env="$1" broker=()
  [[ "$env" == local ]] && broker=(--broker cluster)
  "$PY" scripts/topics.py apply --env "$env" "${broker[@]}"
}

# The schema registry (AW-INF-057): its platform topic first, then Karapace, waited on.
schema_registry() {
  "$PY" scripts/topics.py apply --platform
  kube_kubectl -n "$KAFKA_NS" apply -f deploy/k8s/schema-registry/ >/dev/null
  kube_kubectl -n "$KAFKA_NS" rollout status deploy/andara-schema-registry --timeout=5m >/dev/null \
    || die "$CMD" "the schema registry in $KAFKA_NS did not become Available: kubectl --context $ANDARA_KUBE_CONTEXT -n $KAFKA_NS logs deploy/andara-schema-registry"
  echo "shared: schema registry Ready at http://andara-schema-registry.$KAFKA_NS.svc:8081"
}

# The shared object store (AW-INF-058): the gateway, then each environment's user, bucket and Secret.
objectstore_for_all() {
  local env
  "$PY" scripts/objectstore.py up
  for env in "${ENVS[@]}"; do "$PY" scripts/objectstore.py install "$env"; done
}

up() {
  shared_kafka
  local env
  for env in "${ENVS[@]}"; do topics_for "$env"; done
  brokers
  schema_registry
  objectstore_for_all
}

install() {
  local env="$1"
  check_env "$env"
  shared_kafka
  topics_for "$env"
  brokers
  # sasl_switch stopped the registry for the move to SASL: start it again, on SASL.
  if [[ "$REGISTRY_STOPPED" == 1 ]]; then schema_registry; fi
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
  # As andara-operator, the password on stdin (AW-INF-056).
  out="$(ANDARA_KAFKA_NAMESPACE="$1" "$PY" scripts/kafka_auth.py broker-exec "$KAFKA-broker-1" -- \
    /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --under-replicated-partitions 2>/dev/null)" \
    || { echo "?"; return; }
  grep -c 'Topic:' <<<"$out" || true
}

bounce() {
  # ns is the server's; the brokers are the shared ones in $KAFKA_NS, which a bounce disturbs
  # for every environment on them (ADR-0013; the drill lock is AW-INF-063's).
  local env="$1" ns victim="$KAFKA-broker-0"
  check_env "$env"
  # local's server runs the sim and the Account store in memory until AW-INF-060, so nothing
  # it does touches the brokers, and a bounce would prove nothing while passing.
  [[ "$env" != local ]] \
    || die kafka-broker-bounce "local's server doesn't use the broker yet (sim.source: memory, AW-INF-060); ENV=dev" 2
  ns="andara-$env"
  need kubectl
  kube_require kafka-broker-bounce
  read -r ready0 since0 restarts0 < <(ready_since "$ns") \
    || die kafka-broker-bounce "no andara-0 in $ns to observe; run \`make helm-install ENV=$env\` first"
  [[ "$ready0" == "True" ]] || die kafka-broker-bounce "andara-0 in $ns is not Ready before the bounce"
  local unavail0 uid0 t0
  unavail0="$(unavailable_count "$ns")"
  [[ "$unavail0" != "?" ]] || die kafka-broker-bounce "could not read andara-0's /metrics before the bounce"
  uid0="$(kube_kubectl -n "$KAFKA_NS" get pod "$victim" -o jsonpath='{.metadata.uid}')" \
    || die kafka-broker-bounce "no $victim in $KAFKA_NS; run \`make shared-up\` first"
  t0="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  kube_kubectl -n "$KAFKA_NS" delete pod "$victim" --wait=false >/dev/null
  echo "kafka-broker-bounce: deleted $victim at $t0; waiting for the checkpoint"

  # Rule 1 of docs/specs/testing/live-assertions.md: poll to a deadline, and only for the
  # checkpoint — the replacement broker Ready and every partition back in sync. Nothing
  # about the server is sampled in this loop.
  local deadline=$((SECONDS + 600)) uid ready urp
  while :; do
    uid="$(kube_kubectl -n "$KAFKA_NS" get pod "$victim" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
    ready="$(kube_kubectl -n "$KAFKA_NS" get pod "$victim" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || true)"
    if [[ -n "$uid" && "$uid" != "$uid0" && "$ready" == "true" ]]; then
      urp="$(under_replicated "$KAFKA_NS")"
      [[ "$urp" == "0" ]] && break
    fi
    ((SECONDS < deadline)) || die kafka-broker-bounce "no checkpoint within 10 minutes: $victim ready=${ready:-?}, under-replicated=${urp:-?}"
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
  up) CMD=shared-up; up ;;
  install) CMD=kafka-install; install "${2:-}" ;;
  bounce) CMD=kafka-broker-bounce; bounce "${2:-}" ;;
  *) echo "usage: kafka.sh <up|install|bounce> [env]" >&2; exit 2 ;;
esac
