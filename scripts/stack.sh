#!/usr/bin/env bash
# Local stack driver. The compose definition itself is AW-INF-002; this script is the
# stable interface `make up/down/logs/ps` calls, so those targets do not change when
# the stack lands.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"
COMPOSE="deploy/compose/docker-compose.yaml"

not_yet() {
  echo "make: $1: no local stack yet — implement AW-INF-002 (docs/stories/AW-INF-002-local-stack-one-command-up.md)" >&2
  exit 1
}

[[ -f "$COMPOSE" ]] || not_yet "${1:-up}"

command -v docker >/dev/null 2>&1 || {
  echo "make: ${1:-up}: docker not found (run \`make bootstrap\`)" >&2; exit 1;
}

case "${1:-up}" in
  up)
    docker compose -f "$COMPOSE" --profile "${2:-full}" up -d --wait
    ;;
  down)
    if [[ "${2:-0}" == "1" ]]; then
      docker compose -f "$COMPOSE" down --volumes
    else
      docker compose -f "$COMPOSE" down
    fi
    ;;
  logs)
    if [[ -n "${2:-}" ]]; then docker compose -f "$COMPOSE" logs -f "$2";
    else docker compose -f "$COMPOSE" logs -f; fi
    ;;
  ps)
    docker compose -f "$COMPOSE" ps
    ;;
  *)
    echo "make: stack: unknown action '$1'" >&2; exit 2
    ;;
esac
