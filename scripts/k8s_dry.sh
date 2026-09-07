#!/usr/bin/env bash
# Render and validate Kubernetes manifests for one environment.
#
# An empty manifest set is not a failure — it is the state until AW-INF-003 lands,
# which is blocked on ADR-0001.
set -euo pipefail

ENVNAME="${1:-dev}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

CHART="deploy/helm/andara"
VALUES="deploy/helm/values/${ENVNAME}.yaml"

if [[ ! -d "$CHART" ]]; then
  echo "k8s-dry: no chart at $CHART; nothing to validate yet (AW-INF-003)"
  exit 0
fi

command -v helm >/dev/null 2>&1 || {
  echo "make: k8s-dry: helm not found but $CHART exists (run \`make bootstrap\`)" >&2
  exit 1
}
[[ -f "$VALUES" ]] || {
  echo "make: k8s-dry: no values file at $VALUES for ENV=$ENVNAME" >&2
  exit 1
}

helm template andara "$CHART" --values "$VALUES" > /dev/null
echo "k8s-dry: $CHART renders clean for ENV=$ENVNAME"

if command -v kubeconform >/dev/null 2>&1; then
  helm template andara "$CHART" --values "$VALUES" | kubeconform -strict -summary -
else
  echo "k8s-dry: kubeconform not installed; skipped schema validation" >&2
fi
