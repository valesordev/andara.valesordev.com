#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Print the manifest digest a ghcr tag names right now, anonymously (AW-INF-013).
#
# helm_install.sh pins a moving tag (`:dev`) to this digest, so a rerun renders a new
# PodTemplateSpec exactly when the tag has moved: `pullPolicy: Always` only re-resolves a
# tag when a container starts, and never restarts one that is running an older build.
#
# Exit: 0 printed · 1 the registry has no such tag, or refused · 3 not a ghcr repository.
set -euo pipefail

REPOSITORY="${1:?usage: image_digest.sh <ghcr.io/owner/name> <tag>}"
TAG="${2:?}"
[[ "$REPOSITORY" == ghcr.io/* ]] || { echo "image-digest: $REPOSITORY is not on ghcr.io" >&2; exit 3; }
NAME="${REPOSITORY#ghcr.io/}"

token="$(curl -fsS "https://ghcr.io/token?scope=repository:${NAME}:pull" \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')" \
  || { echo "image-digest: ghcr refused an anonymous token for $NAME" >&2; exit 1; }
digest="$(curl -fsSI \
  -H "Authorization: Bearer ${token}" \
  -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json" \
  "https://ghcr.io/v2/${NAME}/manifests/${TAG}" \
  | tr -d '\r' | awk 'tolower($1)=="docker-content-digest:"{print $2}')" \
  || { echo "image-digest: no $REPOSITORY:$TAG (not published, or the package is private)" >&2; exit 1; }
[[ "$digest" == sha256:* ]] || { echo "image-digest: $REPOSITORY:$TAG returned no digest" >&2; exit 1; }
echo "$digest"
