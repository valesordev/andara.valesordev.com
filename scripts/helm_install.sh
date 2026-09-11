#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `helm upgrade --install` for one environment, idempotent (AW-INF-003).
#
# local: the image comes from `make image && make kind-load` (pullPolicy Never) and
# content from a ConfigMap built out of testdata/content/valid, because the local cluster
# has no content store yet (AW-SRV-012). dev and prod pull from the registry and read
# content from the store.
set -euo pipefail

ENVNAME="${1:?usage: helm_install.sh <env> <image> <tag>}"
IMAGE="${2:-andara-server}"
TAG="${3:-dev}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

CHART="deploy/helm/andara"
VALUES="deploy/helm/values/${ENVNAME}.yaml"
NS="andara-${ENVNAME}"

[[ -f "$VALUES" ]] || { echo "make: helm-install: no values file at $VALUES" >&2; exit 1; }
for tool in helm kubectl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "make: helm-install: $tool not found" >&2; exit 1; }
done

kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS" >/dev/null
echo "helm-install: namespace $NS"

if [[ "$ENVNAME" == "local" ]]; then
  # Zone Definition JSON as a ConfigMap, mounted at /content (values/local.yaml sets
  # server.content.source: dir). Recreated every run so a fixture edit is applied.
  kubectl -n "$NS" create configmap andara-content \
    --from-file=testdata/content/valid \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: configmap andara-content from testdata/content/valid"

  # The server refuses to start without TLS material (AW-SRV-005: no plaintext mode), so
  # the local cluster gets the same locally-issued certificate `make up` uses, as the
  # Secret AW-INF-006 will have cert-manager issue under this exact name. Recreated every
  # run so a reissued certificate is applied; the private key never leaves .local/.
  "$REPO/scripts/tls.sh" >/dev/null
  TLS_DIR="${ANDARA_TLS_DIR:-$REPO/.local/tls}"
  kubectl -n "$NS" create secret tls andara-server-tls \
    --cert="$TLS_DIR/server.pem" --key="$TLS_DIR/server-key.pem" \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: secret andara-server-tls from $TLS_DIR (cert-manager takes this over in AW-INF-006)"
fi

helm upgrade --install andara "$CHART" \
  --namespace "$NS" \
  --values "$VALUES" \
  --set "image.repository=${IMAGE}" \
  --set "image.tag=${TAG}" \
  --wait --timeout 10m

echo "helm-install: andara in $NS is serving ($IMAGE:$TAG)"
kubectl -n "$NS" get statefulset andara
kubectl -n "$NS" get pvc -l app=andara
