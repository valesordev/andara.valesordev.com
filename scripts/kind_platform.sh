#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Install the platform pieces the chart assumes into a kind cluster (AW-INF-006): Traefik
# on the host ports and cert-manager with its CRDs — the same versions and values the
# box runs. Idempotent by skipping: a release that already exists is left exactly as it
# is, because on the box those releases are Brian's and serve six other projects; this
# script exists for CI and for a fresh machine, not to reconcile a cluster it did not set up.
#
# The cluster itself must already exist with :80/:443 mapped and an ingress-ready node;
# deploy/kind/config.yaml is that shape, and CI creates its cluster from it. A cluster
# without the mapping gets Traefik Pending forever on the nodeSelector, which is the
# honest failure — there is nothing here to bind a port the cluster did not publish.
set -euo pipefail

CLUSTER="${1:-}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

TRAEFIK_CHART="41.4.0"        # Traefik v3.7 — what `helm -n traefik list` shows on the box
CERT_MANAGER_VERSION="v1.21.1"

for tool in helm kubectl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "make: kind-platform: $tool not found" >&2; exit 1; }
done
[[ -n "$CLUSTER" ]] || { echo "make: kind-platform: no kind cluster found (KIND_CLUSTER=<name>)" >&2; exit 1; }
kubectl config use-context "kind-$CLUSTER" >/dev/null

if ! kubectl get nodes -l ingress-ready=true -o name | grep -q .; then
  echo "make: kind-platform: no node labelled ingress-ready=true; create the cluster from deploy/kind/config.yaml" >&2
  exit 1
fi

helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1 || true
helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
helm repo update traefik jetstack >/dev/null

if helm -n traefik status traefik >/dev/null 2>&1; then
  echo "kind-platform: traefik release present in namespace traefik; leaving it alone"
else
  echo "kind-platform: installing traefik (chart $TRAEFIK_CHART)"
  helm install traefik traefik/traefik \
    --version "$TRAEFIK_CHART" \
    --namespace traefik --create-namespace \
    --values deploy/k8s/traefik/values.yaml \
    --wait --timeout 5m >/dev/null
fi

if helm -n cert-manager status cert-manager >/dev/null 2>&1; then
  echo "kind-platform: cert-manager release present; leaving it alone"
else
  echo "kind-platform: installing cert-manager ($CERT_MANAGER_VERSION)"
  helm install cert-manager jetstack/cert-manager \
    --version "$CERT_MANAGER_VERSION" \
    --namespace cert-manager --create-namespace \
    --set crds.enabled=true \
    --wait --timeout 5m >/dev/null
fi

kubectl get ingressclass traefik >/dev/null
kubectl get crd clusterissuers.cert-manager.io serverstransports.traefik.io middlewares.traefik.io >/dev/null
echo "kind-platform: ok — IngressClass traefik on :80/:443, cert-manager CRDs present (kind/$CLUSTER)"
