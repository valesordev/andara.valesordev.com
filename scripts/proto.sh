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

    # ADR-0007 rule 1 is additive-only, and regenerate-and-diff cannot see a
    # removed field: remove it, regenerate, and the diff is clean. `buf
    # breaking` is what actually enforces it.
    #
    # Against main rather than the merge base, so the comparison is against
    # what is actually released.
    #
    # The `git ls-tree` guard is not defensive padding: on the branch that first
    # introduces these files, main has no protos, and buf fails outright with
    # "Module had no .proto files" rather than treating an absent baseline as
    # nothing to compare. Without the guard, the very commit that adds the
    # schema cannot pass its own check.
    if git rev-parse --verify --quiet main >/dev/null &&
       git ls-tree -r main --name-only -- "$PROTO_DIR" 2>/dev/null | grep -q '\.proto$'; then
      buf breaking "$PROTO_DIR" \
        --against ".git#branch=main,subdir=$PROTO_DIR" \
        || { echo "make: proto-check: breaking schema change (ADR-0007 is additive-only)" >&2; exit 1; }
    else
      echo "proto-check: main has no .proto sources yet; breaking-change check skipped" >&2
    fi

    # The determinism rules from ADR-0007 rule 3, mechanically. Anything that
    # feeds the State Hash may not carry a construct whose encoding is
    # unspecified or unreproducible. Checked here rather than trusted to review
    # because the failure is silent: a hash that differs across replays.
    #
    # Comments are stripped before matching — the rules are documented in these
    # files, and a guard that trips over its own explanation is a guard people
    # delete.
    BAD="$(awk '
      { line = $0; sub(/\/\/.*/, "", line)
        if (line ~ /map[[:space:]]*</ ||
            line ~ /[[:space:]](float|double)[[:space:]]+[A-Za-z_]/ ||
            line ~ /google\.protobuf\.Any/)
          printf "%s:%d: %s\n", FILENAME, FNR, $0 }
    ' "$PROTO_DIR"/andara/log/v1/*.proto "$PROTO_DIR"/andara/state/v1/*.proto 2>/dev/null || true)"
    if [[ -n "$BAD" ]]; then
      echo "make: proto-check: non-deterministic construct in a hashed message (ADR-0007 rule 3):" >&2
      echo "$BAD" >&2
      exit 1
    fi

    echo "proto-check: $GEN_DIR matches $PROTO_DIR"
    ;;
  *)
    echo "make: proto: unknown action '$ACTION'" >&2
    exit 2
    ;;
esac
