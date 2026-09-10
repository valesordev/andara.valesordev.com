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
    # Against the merge target rather than the merge base, so the comparison is
    # against what is actually released.
    #
    # origin/main is preferred over the local main branch, which on a developer
    # machine is usually behind — and a stale baseline makes this check skip or
    # pass vacuously exactly when it matters. CI fetches main explicitly; this
    # is what makes the local run agree with it.
    BASE_REF=""
    if git rev-parse --verify --quiet origin/main >/dev/null; then
      BASE_REF="origin/main"
    elif git rev-parse --verify --quiet main >/dev/null; then
      BASE_REF="main"
    fi

    # The `ls-tree` guard is not defensive padding: on the branch that first
    # introduces these files the baseline has no protos, and buf fails outright
    # with "Module had no .proto files" rather than treating an absent baseline
    # as nothing to compare. Without it, the commit adding the schema could not
    # pass its own check.
    if [[ -n "$BASE_REF" ]] &&
       git ls-tree -r "$BASE_REF" --name-only -- "$PROTO_DIR" 2>/dev/null | grep -q '\.proto$'; then
      # Output is captured so the failure can name the SUBJECTS the broken file is
      # carried under, not only the file. A developer reading "log.proto:75" has to go
      # look up what that breaks; "andara.commands.v1-value" is the thing that breaks.
      BREAKING_OUT="$(mktemp)"
      trap 'rm -f "$BREAKING_OUT"' EXIT
      if buf breaking "$PROTO_DIR" \
           --against ".git#ref=$BASE_REF,subdir=$PROTO_DIR" >"$BREAKING_OUT" 2>&1; then
        echo "proto-check: no breaking change vs $BASE_REF"
      else
        cat "$BREAKING_OUT" >&2
        echo "make: proto-check: breaking schema change vs $BASE_REF (ADR-0007 is additive-only)" >&2
        # The registry cannot catch this: measured against Redpanda on 2026-09-10, its
        # BACKWARD check accepts both a removed and a RENUMBERED field. Renumbering would
        # make replay misread every historical record (ADR-0002). buf is the only detector.
        cut -d: -f1 "$BREAKING_OUT" | grep '\.proto$' | sort -u | while read -r f; do
          "${PY:-python3}" "$REPO/scripts/schemas.py" subjects-for "$f" 2>/dev/null | while read -r sub; do
            echo "make: proto-check:   $f is carried by subject $sub" >&2
          done
        done
        exit 1
      fi
    else
      echo "proto-check: ${BASE_REF:-no baseline branch} has no .proto sources yet; breaking-change check skipped" >&2
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
