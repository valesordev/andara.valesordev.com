#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Render and validate the chart for one environment, or every environment with ENV=all
# (AW-INF-003 AC-1). Validation is against the pinned Kubernetes version in strict mode,
# and kubeconform is required, not optional: a skipped schema check is a green build that
# proves nothing.
#
# The chart also renders cert-manager and Traefik CRDs (AW-INF-006 AC-7), whose schemas
# are not in the Kubernetes set; they come from the CRDs-catalog, the same way the core
# schemas come from kubernetes-json-schema. Not -ignore-missing-schemas: that is a
# skipped check wearing a green badge.
set -euo pipefail

ENVNAME="${1:-all}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

CHART="deploy/helm/andara"
KUBE_VERSION="1.36.1"   # the kind cluster on Brian's box (decided 2026-09-10)
CRD_SCHEMAS='https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

for tool in helm kubeconform; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "make: k8s-dry: $tool not found (run \`make bootstrap\`)" >&2
    exit 1
  }
done

if [[ "$ENVNAME" == "all" ]]; then
  envs=(local dev prod)
else
  envs=("$ENVNAME")
fi

for e in "${envs[@]}"; do
  values="deploy/helm/values/${e}.yaml"
  [[ -f "$values" ]] || {
    echo "make: k8s-dry: no values file at $values for ENV=$e" >&2
    exit 1
  }
  rendered="$(helm template andara "$CHART" --kube-version "$KUBE_VERSION" --values "$values")"
  printf '%s\n' "$rendered" | kubeconform -strict -summary -kubernetes-version "$KUBE_VERSION" \
    -schema-location default -schema-location "$CRD_SCHEMAS" \
    | sed "s/^/k8s-dry [$e]: /"
done

# The CA bootstrap `make helm-install` applies ahead of the chart is a manifest too.
kubeconform -strict -summary -kubernetes-version "$KUBE_VERSION" \
  -schema-location default -schema-location "$CRD_SCHEMAS" \
  deploy/k8s/cert-manager/andara-ca.yaml | sed "s/^/k8s-dry [cert-manager]: /"

# The broker `make kafka-install` applies per namespace (AW-INF-014): Strimzi's
# kafka.strimzi.io/v1 schemas from the same catalog.
kubeconform -strict -summary -kubernetes-version "$KUBE_VERSION" \
  -schema-location default -schema-location "$CRD_SCHEMAS" \
  deploy/k8s/kafka/*.yaml | sed "s/^/k8s-dry [kafka]: /"
echo "k8s-dry: ${envs[*]} render and validate against Kubernetes $KUBE_VERSION"
