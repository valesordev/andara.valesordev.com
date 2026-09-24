#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `make image-check ENV=<env> [TAG=dev] [REGISTRY_ONLY=1]` — can a published server image
# actually be pulled? (AW-INF-013 AC-2, AC-3)
#
#   1. Anonymously, from here: `docker pull` under an empty DOCKER_CONFIG, so no stored
#      login can make a private package look public. A `sha-` tag must carry the commit
#      it names in its org.opencontainers.image.revision label.
#   2. From the cluster: a throwaway pod in andara-<env> running the image's /bin/true.
#      It exercises the kubelet's pull and nothing of the server's, so no broker, content,
#      or Secret is involved.
#
# Exit: 0 pulled both ways · 1 a pull failed or the label disagrees · 3 docker or kubectl
# missing, or no ENV for the cluster half.
set -euo pipefail

ENVNAME="${1:-}"
REPO_IMAGE="${2:?usage: image_check.sh <env> <registry-image> <tag> [registry_only]}"
TAG="${3:?}"
REGISTRY_ONLY="${4:-}"
REF="$REPO_IMAGE:$TAG"

command -v docker >/dev/null 2>&1 || { echo "image-check: docker not found; cannot verify" >&2; exit 3; }
if [[ -z "$REGISTRY_ONLY" ]]; then
  command -v kubectl >/dev/null 2>&1 || { echo "image-check: kubectl not found; cannot verify the cluster pull" >&2; exit 3; }
  [[ -n "$ENVNAME" ]] || { echo "image-check: usage: make image-check ENV=<env> [TAG=<tag>]" >&2; exit 3; }
fi

anon="$(mktemp -d)"
trap 'rm -rf "$anon"' EXIT
if ! err="$(DOCKER_CONFIG="$anon" docker pull -q --platform linux/amd64 "$REF" 2>&1 >/dev/null)"; then
  # "denied" is a private package or one never published; "not found" is a tag that is not
  # there. Both read the same from outside, so say both, and show what the registry said.
  echo "image-check: FAIL anonymous pull of $REF: ${err##*$'\n'}" >&2
  echo "image-check: not published yet, or the package is private (github.com/orgs/valesordev/packages)" >&2
  exit 1
fi
rev="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$REF")"
if [[ "$TAG" == sha-* && "$rev" != "${TAG#sha-}"* ]]; then
  echo "image-check: FAIL $REF carries revision '$rev', not the commit its tag names" >&2
  exit 1
fi
echo "image-check: ok anonymous pull of $REF (revision $rev)"

[[ -z "$REGISTRY_ONLY" ]] || exit 0

NS="andara-$ENVNAME"
POD="andara-image-check-$$"
kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS" >/dev/null
trap 'rm -rf "$anon"; kubectl -n "$NS" delete pod "$POD" --ignore-not-found --wait=false >/dev/null 2>&1 || true' EXIT
kubectl -n "$NS" run "$POD" --image="$REF" --image-pull-policy=Always --restart=Never \
  --labels=app.kubernetes.io/component=image-check --command -- /bin/true >/dev/null
if ! kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$POD" --timeout=180s >/dev/null 2>&1; then
  why="$(kubectl -n "$NS" get pod "$POD" -o jsonpath='{.status.phase} {.status.containerStatuses[0].state}' 2>/dev/null || true)"
  echo "image-check: FAIL the cluster could not pull and run $REF in $NS: $why" >&2
  exit 1
fi
echo "image-check: ok $NS pulled and ran $REF"
