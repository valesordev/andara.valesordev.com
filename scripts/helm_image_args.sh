#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# The `--set` arguments that choose the server image for one environment (AW-INF-013).
# `helm_install.sh` passes them to `helm upgrade`, and `make helm-test` passes the same
# output to `helm template`, so the rule under test is the one that ships.
#
#   local     the kind-loaded IMAGE:TAG always wins over the values file, which says
#             `pullPolicy: Never`: the image only exists because `make kind-load` put it there.
#   others    the values file names the image (ghcr.io/valesordev/andara-server, published by
#             .github/workflows/publish.yaml). IMAGE or TAG override it only when the caller set
#             them explicitly (IMAGE_SET / TAG_SET non-empty). Make's own defaults do not count,
#             because they name the local build, and a registry cannot pull that.
#
# Prints one argument per line; prints nothing when the values file decides.
set -euo pipefail

ENVNAME="${1:?usage: helm_image_args.sh <env> <image> <tag> [image_set] [tag_set]}"
IMAGE="${2:?}"
TAG="${3:?}"
IMAGE_SET="${4:-}"
TAG_SET="${5:-}"

if [[ "$ENVNAME" == "local" ]]; then
  IMAGE_SET=1
  TAG_SET=1
fi
if [[ -n "$IMAGE_SET" ]]; then
  printf -- '--set\nimage.repository=%s\n' "$IMAGE"
fi
if [[ -n "$TAG_SET" ]]; then
  printf -- '--set\nimage.tag=%s\n' "$TAG"
fi
