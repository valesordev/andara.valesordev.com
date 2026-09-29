#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `make cli-release-publish TAG=<cli-dev|v*>` — attach dist/'s archives and SHA256SUMS to a
# GitHub release (AW-INF-020). CI runs it after `make cli-release`, on merge to main for
# `cli-dev` and on a pushed `v*` tag for that tag. Needs `gh` authenticated with
# contents: write.
#
# cli-dev is one rolling pre-release. Its tag is moved to HEAD, the new archives are
# uploaded over the old, and archives no longer in SHA256SUMS (the previous version's names)
# are deleted last, so a download never meets an empty release. The tag moves only when HEAD
# is still origin/main's head: GitHub serializes runs in a concurrency group but doesn't
# order them, so an older run admitted after a newer one would otherwise move cli-dev
# backwards (AC-2). That is the guard image_publish.sh applies to :dev.
#
# A v* tag gets a normal release, created once and never replaced (AC-5). cli-dev isn't
# touched.
set -euo pipefail

TAG="${1:-}"
[[ -n "$TAG" ]] || { echo "usage: cli_release_publish.sh <cli-dev|v*>" >&2; exit 2; }
REPO=valesordev/andara.valesordev.com
DIST=dist

die() { echo "cli-release-publish: $*" >&2; exit 1; }

command -v gh >/dev/null 2>&1 || die "gh not found"
[[ -s "$DIST/SHA256SUMS" ]] || die "$DIST/SHA256SUMS is missing; run make cli-release first"
mapfile -t archives < <(awk '{ print $2 }' "$DIST/SHA256SUMS")
[[ "${#archives[@]}" -eq 5 ]] || die "$DIST/SHA256SUMS lists ${#archives[@]} archives, not 5"
for a in "${archives[@]}"; do [[ -f "$DIST/$a" ]] || die "$DIST/$a is missing"; done

head="$(git rev-parse HEAD)"
short="$(git rev-parse --short=12 HEAD)"

summary() {
  [[ -n "${GITHUB_STEP_SUMMARY:-}" ]] || return 0
  {
    echo "### andara-cli \`$TAG\` → \`$short\`"
    echo
    echo "| Archive | SHA-256 |"
    echo "|---|---|"
    awk '{ printf "| `%s` | `%s` |\n", $2, $1 }' "$DIST/SHA256SUMS"
  } >>"$GITHUB_STEP_SUMMARY"
}

case "$TAG" in
  cli-dev)
    git fetch -q origin main
    if [[ "$head" != "$(git rev-parse origin/main)" ]]; then
      echo "cli-release-publish: HEAD is not origin/main's head ($(git rev-parse --short=12 origin/main)); cli-dev left to that commit's run"
      exit 0
    fi
    git push -q -f origin "$head:refs/tags/cli-dev" || die "could not move the cli-dev tag to $short"
    notes="andara-cli built from main at $head. Replaced on every merge to main; SHA256SUMS is the index."
    if gh release view cli-dev -R "$REPO" >/dev/null 2>&1; then
      gh release edit cli-dev -R "$REPO" --prerelease --title "andara-cli dev ($short)" --notes "$notes" >/dev/null
    else
      gh release create cli-dev -R "$REPO" --prerelease --verify-tag --title "andara-cli dev ($short)" --notes "$notes" >/dev/null
    fi
    gh release upload cli-dev -R "$REPO" --clobber "${archives[@]/#/$DIST/}" || die "upload of the archives failed"
    gh release upload cli-dev -R "$REPO" --clobber "$DIST/SHA256SUMS" || die "upload of SHA256SUMS failed"
    while read -r name; do
      [[ "$name" == SHA256SUMS ]] && continue
      printf '%s\n' "${archives[@]}" | grep -qxF "$name" && continue
      gh release delete-asset cli-dev "$name" -R "$REPO" --yes || die "could not delete stale asset $name"
    done < <(gh release view cli-dev -R "$REPO" --json assets -q '.assets[].name')
    ;;
  v*)
    [[ "$(git rev-parse "refs/tags/$TAG^{commit}" 2>/dev/null)" == "$head" ]] \
      || die "HEAD is not the commit tag $TAG names"
    gh release create "$TAG" -R "$REPO" --verify-tag --title "andara-cli $TAG" \
      --notes "andara-cli $TAG. SHA256SUMS is the index." \
      "${archives[@]/#/$DIST/}" "$DIST/SHA256SUMS" || die "could not create release $TAG"
    ;;
  *)
    echo "usage: cli_release_publish.sh <cli-dev|v*>" >&2
    exit 2
    ;;
esac

summary
echo "cli-release-publish: $TAG -> $short (${#archives[@]} archives and SHA256SUMS)"
