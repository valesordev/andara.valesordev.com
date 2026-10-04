#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `make cli-offline-check` — run the built linux/amd64 andara-cli with no network, no HOME and
# no config, and prove `content reference` still works (AW-CLI-009, AC-1).
#
# The container has `--network none`, a HOME that doesn't exist, and no config or credential
# file, so a command that dials out, or reads state it shouldn't, fails here. It needs
# `make cli-release` first, which writes the archive this unpacks.
#
# Exit codes: 0 ok; 1 the command failed or its output broke AC-1; 2 usage or a missing archive.
set -euo pipefail

die() { echo "cli-offline-check: $*" >&2; exit 1; }

dist="${1:-dist}"
shopt -s nullglob
archives=("$dist"/andara-cli_*_linux_amd64.tar.gz)
[[ ${#archives[@]} -eq 1 ]] || { echo "cli-offline-check: want one $dist/andara-cli_*_linux_amd64.tar.gz, found ${#archives[@]}; run make cli-release" >&2; exit 2; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
tar -C "$work" -xzf "${archives[0]}"
[[ -x "$work/andara-cli" ]] || die "${archives[0]} has no andara-cli at its root"

# Pull outside the captured run: the client's progress lines are stderr, and aren't the CLI's.
docker pull -q busybox:stable >/dev/null || die "could not pull busybox:stable"

set +e
docker run --rm --network none -e HOME=/nonexistent -v "$work:/cli:ro" busybox:stable \
  /cli/andara-cli content reference --output json >"$work/out" 2>"$work/err"
code=$?
set -e

[[ $code -eq 0 ]] || die "exited $code: $(head -c 300 "$work/err")"
[[ ! -s "$work/err" ]] || die "wrote to stderr: $(head -c 300 "$work/err")"
python3 - "$work/out" <<'PY' || die "stdout is not one JSON object with the four sections"
import json, sys
d = json.load(open(sys.argv[1]))
assert isinstance(d, dict)
for k in ("directions", "component_types", "core", "diagnostics"):
    assert d.get(k), k
PY
echo "cli-offline-check: content reference ran with no network and no HOME: exit 0, empty stderr, one JSON object — passes"
