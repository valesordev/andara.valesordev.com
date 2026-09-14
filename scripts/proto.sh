#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

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

    # The freshness check itself: regenerate into a scratch directory and diff against
    # what is committed. buf breaking above catches a removed field, which this cannot
    # see (remove it, regenerate, and the diff is clean); this catches the other
    # direction — a field added, or any other edit, without `make proto` being run —
    # which buf breaking cannot see because an additive change is not breaking. Both
    # are needed. Until 2026-09-14 this step did not exist and the line below asserted
    # a comparison that never happened (AW-INF-001 AC-15).
    #
    # The diff runs per generated package rather than over the whole tree, so the
    # failure names the package a developer has to look at, not just "gen/ differs".
    FRESH="$(mktemp -d)"
    trap 'rm -rf "$FRESH" "${BREAKING_OUT:-}"' EXIT
    # Remote plugins mean this reaches the BSR, so the failure has to distinguish "the
    # schema does not generate" from "the registry refused us" — an unauthenticated
    # client is rate-limited, and a developer who hits that should not go looking for
    # a bug in their .proto.
    GEN_OUT="$(mktemp)"
    if ! buf generate "$PROTO_DIR" -o "$FRESH" >"$GEN_OUT" 2>&1; then
      cat "$GEN_OUT" >&2
      if grep -q 'resource_exhausted\|too many requests' "$GEN_OUT"; then
        echo "make: proto-check: the BSR rate-limited this run; wait a minute and retry (or \`buf registry login\`)" >&2
      else
        echo "make: proto-check: buf generate failed; the schema does not generate cleanly" >&2
      fi
      rm -f "$GEN_OUT"
      exit 1
    fi
    rm -f "$GEN_OUT"
    # `diff -rq` reports each differing or one-sided file; the parent directory of
    # each is the generated package. "Only in gen/..." covers a .proto that was removed
    # without regenerating; "Only in $FRESH/..." covers one that was added.
    #
    # `|| true` because diff exits 1 on a difference, this script runs under
    # `set -eo pipefail`, and an assignment whose substitution fails is itself a
    # failure: without it the script died here silently with no message at all.
    # gen/README.md is the one hand-written file under gen/ — it explains why the
    # directory is committed — and is excluded by name rather than by pattern so that
    # any other stray file in gen/ is still reported.
    DIFF_OUT="$(diff -rq --exclude=README.md "$FRESH/$GEN_DIR" "$GEN_DIR" 2>&1 || true)"
    if [[ -n "$DIFF_OUT" ]]; then
      STALE="$(printf '%s\n' "$DIFF_OUT" \
        | sed -E -e "s#^Files $FRESH/([^ ]+) and .*#\\1#" \
                 -e "s#^Only in $FRESH/([^:]+): (.*)#\\1/\\2#" \
                 -e "s#^Only in ([^:]+): (.*)#\\1/\\2#" \
        | xargs -r -n1 dirname | sort -u | tr '\n' ' ')"
      # The raw lines too, so a CI failure is diagnosable from the log: which files,
      # and whether they differ or exist on one side only.
      printf '%s\n' "$DIFF_OUT" | sed "s#$FRESH/##" >&2
      echo "make: proto-check: generated code is stale in: $STALE(run \`make proto\` and commit gen/)" >&2
      exit 1
    fi

    echo "proto-check: $GEN_DIR matches $PROTO_DIR"
    ;;
  *)
    echo "make: proto: unknown action '$ACTION'" >&2
    exit 2
    ;;
esac
