#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `make image-publish` — build the server image and push it to ghcr (AW-INF-013).
#
# One tag per commit: `sha-<12 hex>`, immutable, which a deploy, a rollback or `make promote`
# names. Nothing moves `:dev` any more: `dev` runs what the Git tag `dev` pins by digest
# (AW-INF-059, ADR-0013 decision 7), so a merge to main publishes an image and runs nothing.
#
# linux/amd64 only: the box's nodes. CI runs this on merge to main after `docker login`.
#
# The build context is `git archive HEAD`, not the working directory, so a `sha-` tag is
# exactly its commit: an untracked or ignored file cannot reach the image. A dirty tree is
# still refused, because VERSION would say `-dirty` about an image that is not.
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
echo "image-publish: digest $(docker inspect --format '{{index .RepoDigests 0}}' "$REPO_IMAGE:$SHA_TAG" | sed 's/.*@//')"
