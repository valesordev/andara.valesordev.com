#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `make cli-release` — build andara-cli for every Builder platform into dist/ (AW-INF-020).
#
# One archive per platform, `andara-cli_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows),
# each holding the binary, LICENSE and NOTICE, and a SHA256SUMS over the archives. The
# binaries carry the same -ldflags stamps `make build` gives bin/andara-cli, so
# `andara-cli version` names the version and commit. CGO_ENABLED=0 makes them static: a
# Builder's machine needs no Go toolchain and no libc of a particular age (AC-7).
#
# dist/ is emptied first, so it never holds a previous commit's archives beside this one's,
# and SHA256SUMS lists exactly what a publish would upload. CI runs this target on every
# pull request (AC-6) and on merge to main before publishing.
set -euo pipefail

GO="${1:?usage: cli_release.sh <go> <version> <ldflags>}"
VERSION="${2:?}"
LDFLAGS="${3:?}"

PLATFORMS=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64)
DIST=dist

die() { echo "cli-release: $*" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
  sha() { shasum -a 256 "$@"; }
else
  die "neither sha256sum nor shasum found"
fi
command -v zip >/dev/null 2>&1 || die "zip not found; the Windows archive needs it"

rm -rf "$DIST"
mkdir -p "$DIST"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

archives=()
for p in "${PLATFORMS[@]}"; do
  os="${p%/*}" arch="${p#*/}"
  exe=andara-cli
  [[ "$os" == windows ]] && exe=andara-cli.exe
  dir="$stage/${os}_${arch}"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$GO" build -trimpath -ldflags "$LDFLAGS" \
    -o "$dir/$exe" ./cmd/andara-cli || die "go build failed for $p"
  cp LICENSE NOTICE "$dir/"
  base="andara-cli_${VERSION}_${os}_${arch}"
  if [[ "$os" == windows ]]; then
    (cd "$dir" && zip -q -X "$OLDPWD/$DIST/$base.zip" "$exe" LICENSE NOTICE) || die "zip failed for $p"
    archives+=("$base.zip")
  else
    tar -C "$dir" -czf "$DIST/$base.tar.gz" "$exe" LICENSE NOTICE || die "tar failed for $p"
    archives+=("$base.tar.gz")
  fi
done

(cd "$DIST" && sha "${archives[@]}" > SHA256SUMS)
echo "cli-release: ${#archives[@]} archives and SHA256SUMS in $DIST/ ($VERSION)"
