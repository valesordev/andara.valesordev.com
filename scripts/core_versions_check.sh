#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# make core-versions-check — content/core/VERSIONS is append-only (AW-INF-029).
#
# VERSIONS maps each andara.core version to the digest of its blobs, and the server and
# andara-cli hold their embedded core to it (AW-SRV-013 AC-19, AW-CLI-002 AC-10). So
# andara.core@N means the same bytes everywhere only while no line, once written, changes.
#
# Compares the working tree's VERSIONS with the merge base of BASE_REF (default origin/main)
# and HEAD. Every base line must be unchanged and in place; each appended line's version must
# be the previous last version plus one (VERSION is monotonic, ADR-0004). When HEAD is already
# on the base (a push to main, or a branch with nothing committed), the merge base is HEAD
# itself, so it checks HEAD's commit against HEAD's first parent, then the working tree against
# HEAD.
#
# CI sets BASE_REF to the push's `before` commit on main, so a multi-commit push is checked
# whole. A shallow clone is refused (exit 2): it can't tell a root commit from an unfetched parent.
#
# Exits (the script's; through make, any failure is make's 2):
#   0  unchanged, or only the next versions appended
#   1  a violation, named on a `core-versions-check:` line
#   2  no base to compare with, or a shallow clone
set -euo pipefail

FILE=content/core/VERSIONS
BASE_REF=${BASE_REF:-origin/main}

say() { echo "core-versions-check: $*"; }

if [ "$(git rev-parse --is-shallow-repository)" = true ]; then
  # A shallow clone can't tell "HEAD has no parent" from "the parent wasn't fetched", and a
  # merge base past the shallow boundary isn't there either. Refuse rather than pass.
  say "shallow clone; run git fetch --unshallow"
  exit 2
fi
if ! base=$(git merge-base "$BASE_REF" HEAD 2>/dev/null); then
  say "no merge base; fetch origin"
  exit 2
fi
# read_lines <array> <rev|-> — VERSIONS at a commit, or the working tree's for `-`.
read_lines() {
  local -n out=$1
  out=()
  if [ "$2" = - ]; then
    if [ -f "$FILE" ]; then mapfile -t out < "$FILE"; fi
  elif git cat-file -e "$2:$FILE" 2>/dev/null; then
    mapfile -t out < <(git show "$2:$FILE")
  fi
}

# check <old rev> <new rev|-> — exits 1, naming the line, unless new is old plus appended
# next versions; otherwise prints nothing and returns.
check() {
  local old new i last v
  read_lines old "$1"
  read_lines new "$2"

  # Every old line unchanged and in place. A deletion shows as the first missing line.
  for i in "${!old[@]}"; do
    if [ "$i" -ge "${#new[@]}" ] || [ "${new[$i]}" != "${old[$i]}" ]; then
      say "line $((i + 1)) changed; VERSIONS is append-only"
      exit 1
    fi
  done

  # Each appended line is the next version. Fields split on whitespace, as content/core reads
  # them (strings.Fields), and numbers are decimal: base 10 forced, so `08` isn't octal.
  last=0
  if [ "${#old[@]}" -gt 0 ]; then
    read -r last _ <<< "${old[-1]}" || true
    if ! [[ "$last" =~ ^[0-9]+$ ]]; then
      say "line ${#old[@]} at the base has no version number; fix the base first"
      exit 1
    fi
    last=$((10#$last))
  fi
  for ((i = ${#old[@]}; i < ${#new[@]}; i++)); do
    v=
    read -r v _ <<< "${new[$i]}" || true
    # Digits only, no leading zero, and the next number.
    if ! [[ "$v" =~ ^[0-9]+$ ]] || [ "$v" != "$((10#$v))" ] || [ "$v" -ne "$((last + 1))" ]; then
      say "line $((i + 1)) is version $v; expected $((last + 1))"
      exit 1
    fi
    last=$((10#$v))
  done
}

head=$(git rev-parse HEAD)
if [ "$base" = "$head" ]; then
  # HEAD is on the base itself (a push to main, or a branch with nothing committed yet). Check
  # HEAD's own commit against its first parent, then the working tree against HEAD: every line
  # HEAD holds is already immutable, so an uncommitted edit of HEAD's appended line is caught.
  # A root commit has no parent, so only the second check runs.
  if parent=$(git rev-parse --verify --quiet "HEAD^1"); then
    check "$parent" "$head"
  fi
  base=$head
fi
check "$base" -

read_lines old "$base"
read_lines new -
if [ "${#new[@]}" -gt "${#old[@]}" ]; then
  say "ok: ${#old[@]} line(s) unchanged, $(( ${#new[@]} - ${#old[@]} )) appended against ${base:0:12}"
else
  say "ok: unchanged against ${base:0:12}"
fi
