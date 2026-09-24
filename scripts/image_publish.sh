#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `make image-publish` — build the server image and push it to ghcr (AW-INF-013).
#
# Two tags per commit: `sha-<12 hex>`, immutable, which a deploy or a rollback names, and
# `dev`, which moves to the latest main and is what values/dev.yaml pulls. The sha tag is
# pushed first so that `dev` never names a digest the registry does not hold under a
# name that will outlive it.
#
# linux/amd64 only: the box's nodes. CI runs this on merge to main after `docker login`.
#
# The build context is `git archive HEAD`, not the working directory, so a `sha-` tag is
# exactly its commit: an untracked or ignored file cannot reach the image. A dirty tree is
# still refused, because VERSION would say `-dirty` about an image that is not.
#
# `:dev` moves only when HEAD is still origin/main's head at push time. GitHub does not
# order runs in a concurrency group, only serializes them, so an older run admitted after a
# newer one would otherwise move `:dev` backwards; with the check it pushes its `sha-` tag
# and leaves `:dev` to the newer run. Run by hand on any other commit, it does the same.
set -euo pipefail

REPO_IMAGE="${1:?usage: image_publish.sh <registry-image> <version> <commit> <revision>}"
VERSION="${2:?}"
COMMIT="${3:?}"
REVISION="${4:?}"

command -v docker >/dev/null 2>&1 || { echo "make: image-publish: docker not found" >&2; exit 3; }
if [[ -n "$(git status --porcelain)" ]]; then
  echo "make: image-publish: the tree has uncommitted or untracked changes; an image must be a commit" >&2
  exit 1
fi

SHA_TAG="sha-$(git rev-parse --short=12 HEAD)"

git archive --format=tar HEAD \
  | docker build -f deploy/compose/Dockerfile.server --platform linux/amd64 \
      --build-arg VERSION="$VERSION" --build-arg COMMIT="$COMMIT" --build-arg REVISION="$REVISION" \
      -t "$REPO_IMAGE:$SHA_TAG" -
docker push "$REPO_IMAGE:$SHA_TAG"
echo "image-publish: $REPO_IMAGE:$SHA_TAG ($VERSION, $REVISION)"

git fetch -q origin main
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
  echo "image-publish: HEAD is not origin/main's head ($(git rev-parse --short=12 origin/main)); :dev left to that commit's run"
  exit 0
fi
docker tag "$REPO_IMAGE:$SHA_TAG" "$REPO_IMAGE:dev"
docker push "$REPO_IMAGE:dev"
echo "image-publish: $REPO_IMAGE:dev -> $SHA_TAG"
