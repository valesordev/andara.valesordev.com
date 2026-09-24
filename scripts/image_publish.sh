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
# linux/amd64 only: the box's nodes. CI runs this on merge to main after `docker login`;
# run by hand, it publishes whatever HEAD is, so it refuses a dirty tree.
set -euo pipefail

REPO_IMAGE="${1:?usage: image_publish.sh <registry-image> <version> <commit> <revision>}"
VERSION="${2:?}"
COMMIT="${3:?}"
REVISION="${4:?}"

command -v docker >/dev/null 2>&1 || { echo "make: image-publish: docker not found" >&2; exit 3; }
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "make: image-publish: the tree has uncommitted changes; an image must be a commit" >&2
  exit 1
fi

SHA_TAG="sha-$(git rev-parse --short=12 HEAD)"

docker build -f deploy/compose/Dockerfile.server --platform linux/amd64 \
  --build-arg VERSION="$VERSION" --build-arg COMMIT="$COMMIT" --build-arg REVISION="$REVISION" \
  -t "$REPO_IMAGE:$SHA_TAG" .
docker push "$REPO_IMAGE:$SHA_TAG"
docker tag "$REPO_IMAGE:$SHA_TAG" "$REPO_IMAGE:dev"
docker push "$REPO_IMAGE:dev"

echo "image-publish: $REPO_IMAGE:$SHA_TAG and :dev ($VERSION, $REVISION)"
