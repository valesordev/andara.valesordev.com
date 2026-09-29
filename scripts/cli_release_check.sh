#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `make cli-release-check [TAG=cli-dev]` — download a published andara-cli the way a Builder
# does, and prove it runs (AW-INF-020, AC-3 and AC-4).
#
# Anonymous on purpose: curl sends no credentials, so a release a Builder can't reach fails
# here too. SHA256SUMS is the index, because the version is in each archive's name. The
# archive for this machine's platform is found in it, downloaded, checked against it, and
# unpacked, and its `andara-cli version` must name a version and a commit.
#
# The optional second argument replaces the release's download URL. The tests use it with a
# file:// directory; `make` doesn't pass it.
#
# Exit codes: 0 ok; 1 download, checksum or run failure; 2 usage.
set -euo pipefail

TAG="${1:-}"
[[ -n "$TAG" ]] || { echo "usage: cli_release_check.sh <tag> [base-url]" >&2; exit 2; }
BASE="${2:-https://github.com/valesordev/andara.valesordev.com/releases/download/$TAG}"

die() { echo "cli-release-check: $*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW*|MSYS*|CYGWIN*) os=windows ;;
  *) die "no published archive for $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "no published archive for $(uname -m)" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$1" | cut -d' ' -f1; }
else
  sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fetch() { curl -fsSL --retry 3 -o "$work/$1" "$BASE/$1" || die "could not download $BASE/$1"; }

fetch SHA256SUMS
line="$(grep -E "  andara-cli_[^ ]+_${os}_${arch}\.(tar\.gz|zip)$" "$work/SHA256SUMS" || true)"
[[ -n "$line" ]] || die "SHA256SUMS in $TAG lists no archive for ${os}_${arch}"
[[ "$(wc -l <<<"$line")" -eq 1 ]] || die "SHA256SUMS in $TAG lists more than one archive for ${os}_${arch}"
want="${line%% *}"
file="${line##* }"

fetch "$file"
[[ "$(sha "$work/$file")" == "$want" ]] || die "checksum mismatch for $file"

mkdir "$work/x"
exe=andara-cli
if [[ "$file" == *.zip ]]; then
  unzip -q "$work/$file" -d "$work/x" || die "could not unpack $file"
  exe=andara-cli.exe
else
  tar -C "$work/x" -xzf "$work/$file" || die "could not unpack $file"
fi

out="$("$work/x/$exe" version)" || die "$file: andara-cli version exited non-zero"
version="$(awk '$1 == "version:" { print $2 }' <<<"$out")"
commit="$(awk '$1 == "commit:" { print $2 }' <<<"$out")"
[[ -n "$version" && -n "$commit" ]] || die "$file: andara-cli version names no version and commit: $out"

echo "cli-release-check: andara-cli $version ($commit) ok"
