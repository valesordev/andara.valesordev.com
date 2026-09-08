#!/usr/bin/env bash
# Protobuf codegen and freshness check.
#
# ADR-0007: protobuf is the schema authority and generated code is committed, so a schema
# change and its blast radius land in the same diff. `check` verifies that what is
# committed still matches the .proto sources.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

PROTO_DIR="docs/specs/protocol"
GEN_DIR="gen"
ACTION="${1:-gen}"

have_protos() {
  find "$PROTO_DIR" -name '*.proto' -print -quit 2>/dev/null | grep -q .
}

if ! have_protos; then
  # The state until AW-SRV-005 lands. Not a failure — an empty schema is a valid schema.
  echo "proto: no .proto sources in $PROTO_DIR yet; skipping (AW-SRV-005)"
  exit 0
fi

command -v buf >/dev/null 2>&1 || {
  echo "make: proto: buf not found but .proto sources exist (run \`make bootstrap\`)" >&2
  exit 1
}

case "$ACTION" in
  gen)
    buf lint "$PROTO_DIR"
    buf generate "$PROTO_DIR"
    echo "proto: regenerated $GEN_DIR from $PROTO_DIR"
    ;;
  check)
    buf lint "$PROTO_DIR"
    TMP="$(mktemp -d)"
    trap 'rm -rf "$TMP"' EXIT
    buf generate "$PROTO_DIR" --output "$TMP"
    # gen/README.md is hand-written and never appears in a regenerated tree. Without
    # this exclusion, `make check` would go red the day the first .proto lands, blaming
    # stale codegen for a file that codegen does not produce.
    if ! diff -rq -x README.md "$TMP/$GEN_DIR" "$GEN_DIR" >/dev/null 2>&1; then
      STALE="$(diff -rq -x README.md "$TMP/$GEN_DIR" "$GEN_DIR" 2>&1 | head -5)"
      echo "make: proto-check: generated code is stale (run \`make proto\`):" >&2
      echo "$STALE" >&2
      exit 1
    fi
    echo "proto-check: $GEN_DIR matches $PROTO_DIR"
    ;;
  *)
    echo "make: proto: unknown action '$ACTION'" >&2
    exit 2
    ;;
esac
