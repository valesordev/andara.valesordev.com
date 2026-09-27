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
# dev follows main (AW-INF-019): the Application from the catalog's Argo CD schema. The
# ImageUpdater against the CRD of the Image Updater chart scripts/argocd.py pins, not the
# catalog's, which is an older version's (it requires a spec.namespace 1.x dropped).
IU_VERSION="$(sed -n 's/^IMAGE_UPDATER_CHART_VERSION = "\(.*\)".*/\1/p' scripts/argocd.py)"
IU_SCHEMAS="$(mktemp -d)"
trap 'rm -rf "$IU_SCHEMAS"' EXIT
helm template iu argocd-image-updater --repo https://argoproj.github.io/argo-helm --version "$IU_VERSION" \
    --show-only templates/crd-imageupdaters.yaml \
  | "${PY:-python3}" -c '
import json, sys, yaml
crd = yaml.safe_load(sys.stdin)
def strict(n):
    # Unknown fields are errors, as in the catalog schemas (openapi2jsonschema --strict),
    # except where the CRD itself keeps unknown fields.
    if isinstance(n, dict):
        if "properties" in n and "additionalProperties" not in n and not n.get("x-kubernetes-preserve-unknown-fields"):
            n["additionalProperties"] = False
        for c in n.values():
            strict(c)
    elif isinstance(n, list):
        for c in n:
            strict(c)
for v in crd["spec"]["versions"]:
    s = v["schema"]["openAPIV3Schema"]
    strict(s)
    s["properties"]["metadata"] = {"type": "object"}
    name = "%s_%s.json" % (crd["spec"]["names"]["kind"].lower(), v["name"])
    json.dump(s, open(sys.argv[1] + "/" + name, "w"))
' "$IU_SCHEMAS"
kubeconform -strict -summary -kubernetes-version "$KUBE_VERSION" \
  -schema-location default -schema-location "$IU_SCHEMAS/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json" \
  -schema-location "$CRD_SCHEMAS" \
  deploy/argocd/andara-dev.yaml deploy/argocd/andara-dev-image-updater.yaml | sed "s/^/k8s-dry [argocd]: /"
echo "k8s-dry: ${envs[*]} render and validate against Kubernetes $KUBE_VERSION"
