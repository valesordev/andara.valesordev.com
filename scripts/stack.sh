#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Local stack driver (AW-INF-002). `make up/down/logs/ps` call this; nothing calls
# `docker compose` directly, because everything around the compose invocation — TLS,
# port checks, health waiting, topic application — is what makes the stack honest.
#
# `make up` returns only when the stack is usable, not when the containers have started.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

COMPOSE_FILE="deploy/compose/docker-compose.yaml"
DATA_DIR="${ANDARA_DATA_DIR:-$REPO/.local/data}"
TLS_DIR="${ANDARA_TLS_DIR:-$REPO/.local/tls}"

fail() { echo "make: ${ACTION:-up}: $*" >&2; exit 1; }

ACTION="${1:-up}"

command -v docker >/dev/null 2>&1 || fail "docker not found (run \`make bootstrap\`)"
docker compose version >/dev/null 2>&1 || fail "the docker compose plugin is not installed"
[[ -f "$COMPOSE_FILE" ]] || fail "missing $COMPOSE_FILE"

dc() { docker compose -f "$COMPOSE_FILE" "$@"; }

# The server profile is enabled only once there is a server to build. Before AW-SRV-005
# landed, `make up` brought up everything the server would need and said so, rather than
# failing on a build context with no main package in it. The detection stays: it is what
# keeps a fresh checkout that has not built yet from failing, and it costs one `find`.
server_profile_args() {
  if find cmd/andara-server -name '*.go' -print -quit 2>/dev/null | grep -q .; then
    echo "--profile server"
  fi
}

# port env-var-name description
#
# Every port has an override, and `make up` names the variable rather than making the
# developer find it (AC-9).
declare -a PORTS_MIN=(
  "${ANDARA_KAFKA_PORT:-9092}|ANDARA_KAFKA_PORT|Redpanda Kafka API"
  "${ANDARA_SCHEMA_REGISTRY_PORT:-8081}|ANDARA_SCHEMA_REGISTRY_PORT|schema registry"
  "${ANDARA_REDPANDA_ADMIN_PORT:-9644}|ANDARA_REDPANDA_ADMIN_PORT|Redpanda admin and metrics"
  "${ANDARA_REDIS_PORT:-6379}|ANDARA_REDIS_PORT|Redis hot projection"
)
declare -a PORTS_FULL=(
  "${ANDARA_POSTGRES_PORT:-5432}|ANDARA_POSTGRES_PORT|Postgres tabular projection"
  "${ANDARA_TRACE_PORT:-4317}|ANDARA_TRACE_PORT|OTLP gRPC receiver"
  "${ANDARA_OTLP_HTTP_PORT:-4318}|ANDARA_OTLP_HTTP_PORT|OTLP HTTP receiver"
  "${ANDARA_METRICS_PORT:-9090}|ANDARA_METRICS_PORT|Prometheus"
  "${ANDARA_DASHBOARD_PORT:-3000}|ANDARA_DASHBOARD_PORT|Grafana"
  "${ANDARA_LOKI_PORT:-3100}|ANDARA_LOKI_PORT|Loki"
)
declare -a PORTS_SERVER=(
  "${ANDARA_GRPC_PORT:-8443}|ANDARA_GRPC_PORT|andara-server gRPC (TLS)"
  "${ANDARA_HTTP_PORT:-8080}|ANDARA_HTTP_PORT|andara-server health and metrics"
)

port_in_use() {
  "${PY:-python3}" - "$1" <<'PY'
import socket, sys
s = socket.socket()
s.settimeout(0.4)
sys.exit(0 if s.connect_ex(("127.0.0.1", int(sys.argv[1]))) == 0 else 1)
PY
}

preflight_ports() {
  local profile="$1"
  local -a wanted=("${PORTS_MIN[@]}")
  [[ "$profile" == "full" ]] && wanted+=("${PORTS_FULL[@]}")
  [[ -n "$(server_profile_args)" ]] && wanted+=("${PORTS_SERVER[@]}")

  for entry in "${wanted[@]}"; do
    IFS='|' read -r port var desc <<< "$entry"
    if port_in_use "$port"; then
      fail "port $port is already in use ($desc). Override it with $var=<port>, or stop whatever holds it."
    fi
  done
}

case "$ACTION" in
  up)
    PROFILE="${2:-full}"
    [[ "$PROFILE" == "min" || "$PROFILE" == "full" ]] \
      || fail "PROFILE must be 'min' or 'full', got '$PROFILE'"

    mkdir -p "$DATA_DIR"

    # A stack that is already up is not restarted, and its ports are not re-checked —
    # they are held by the stack itself (AC-2).
    RUNNING="$(dc ps -q 2>/dev/null | wc -l | tr -d ' ')"
    if [[ "$RUNNING" == "0" ]]; then
      preflight_ports "$PROFILE"
    fi

    "$REPO/scripts/tls.sh"
    "$REPO/scripts/auth_keys.sh"

    echo "up: starting the $PROFILE stack (this blocks until every service is healthy)"
    dc --profile "$PROFILE" up -d --wait --remove-orphans \
      || fail "one or more services did not become healthy; \`make logs\` shows why"

    # Topics come from deploy/kafka/topics.yaml, applied by AW-INF-004's tool. The compose
    # file deliberately holds no topic configuration of its own: one declaration, applied
    # to local and production alike, or they drift.
    ANDARA_ENV=local "${PY:-python3}" "$REPO/scripts/topics.py" apply --env local

    # Subjects come from deploy/kafka/schemas.yaml the same way (AW-INF-004), so a local
    # registry holds what production holds rather than whatever the first producer wrote.
    ANDARA_ENV=local "${PY:-python3}" "$REPO/scripts/schemas.py" apply --env local

    # The server starts only after the topics exist: it replays andara.accounts.v1 at
    # boot and refuses to run against a topic it would have had to create (AW-SRV-008).
    # It runs as the host uid so the 0600 keyring bind-mounted from .local/auth is
    # readable inside the container.
    if [[ -n "$(server_profile_args)" ]]; then
      export ANDARA_UID="$(id -u)" ANDARA_GID="$(id -g)"
      dc --profile "$PROFILE" --profile server up -d --wait andara-server \
        || fail "andara-server did not become ready; \`make logs SVC=andara-server\` shows why"
    fi

    # A ready-to-use CLI config, so "the CA is trusted out of the box" (AW-INF-002 AC-5)
    # is one export rather than two flags on every invocation. Written into the repo's
    # .local/ rather than the developer's global config, because a local stack has no
    # business editing $XDG_CONFIG_HOME.
    mkdir -p "$REPO/.local"
    cat > "$REPO/.local/cli.yaml" <<CLICONF
# Generated by \`make up\`. Not committed; regenerated on every up.
# Use with: export ANDARA_CONFIG=$REPO/.local/cli.yaml
server:
  address: localhost:${ANDARA_GRPC_PORT:-8443}
  tls_ca: $TLS_DIR/ca.pem
CLICONF

    if [[ -z "$(server_profile_args)" ]]; then
      echo
      echo "up: andara-server is not running — no Go sources found under cmd/andara-server."
      echo "    Everything it depends on is up and waiting for it."
    fi

    echo
    echo "up: the stack is ready"
    printf '  %-24s %s\n' "Kafka API"        "localhost:${ANDARA_KAFKA_PORT:-9092}"
    printf '  %-24s %s\n' "Schema registry"  "http://localhost:${ANDARA_SCHEMA_REGISTRY_PORT:-8081}"
    printf '  %-24s %s\n' "Redpanda admin"   "http://localhost:${ANDARA_REDPANDA_ADMIN_PORT:-9644}/public_metrics"
    printf '  %-24s %s\n' "Redis"            "localhost:${ANDARA_REDIS_PORT:-6379}"
    if [[ "$PROFILE" == "full" ]]; then
      printf '  %-24s %s\n' "Postgres"       "postgres://andara@localhost:${ANDARA_POSTGRES_PORT:-5432}/andara"
      printf '  %-24s %s\n' "OTLP receiver"  "localhost:${ANDARA_TRACE_PORT:-4317}"
      printf '  %-24s %s\n' "Prometheus"     "http://localhost:${ANDARA_METRICS_PORT:-9090}"
      printf '  %-24s %s\n' "Loki"           "http://localhost:${ANDARA_LOKI_PORT:-3100}"
      printf '  %-24s %s\n' "Grafana"        "http://localhost:${ANDARA_DASHBOARD_PORT:-3000}/d/andara-tick-health"
    fi
    if [[ -n "$(server_profile_args)" ]]; then
      printf '  %-24s %s\n' "andara-server"  "localhost:${ANDARA_GRPC_PORT:-8443} (gRPC, TLS)"
      printf '  %-24s %s\n' "server health"  "http://127.0.0.1:${ANDARA_HTTP_PORT:-8080}/readyz"
      printf '  %-24s %s\n' "bootstrap operator" "${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local} (local only; ANDARA_BOOTSTRAP_OPERATOR overrides)"
    fi
    printf '  %-24s %s\n' "TLS CA"           "$TLS_DIR/ca.pem"
    echo
    echo "  andara-cli: export ANDARA_CONFIG=$REPO/.local/cli.yaml"
    ;;

  down)
    # Stopping an already-stopped stack is not an error (AC-3).
    if [[ "${2:-0}" == "1" ]]; then
      dc --profile min --profile full --profile server down --volumes --remove-orphans
      rm -rf "$DATA_DIR"
      echo "down: stack stopped, volumes and $DATA_DIR removed; the next \`make up\` starts from an empty log"
    else
      dc --profile min --profile full --profile server down --remove-orphans
      echo "down: stack stopped; volumes retained"
    fi
    ;;

  logs)
    if [[ -n "${2:-}" ]]; then dc logs -f "$2"; else dc logs -f; fi
    ;;

  ps)
    dc --profile min --profile full --profile server ps
    ;;

  *)
    fail "unknown action '$ACTION'"
    ;;
esac
