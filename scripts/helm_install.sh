#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `helm upgrade --install` for one environment, idempotent (AW-INF-003).
#
# local: the image comes from `make image && make kind-load` (pullPolicy Never) and
# content from a ConfigMap built out of testdata/content/valid, because the local cluster
# has no content store yet (AW-SRV-012). dev and prod pull from the registry and read
# content from the store.
#
# Every environment (AW-INF-006): the private CA is bootstrapped ahead of the chart and
# its certificate exported for `andara-cli`, because a chart that renders a Certificate
# from an issuer that does not exist yet waits ten minutes and then fails on a pod that
# cannot mount its Secret.
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

# The platform the chart is written for: the cluster's Traefik owns :443 (AC-8) and
# cert-manager issues every certificate. Neither is installed here — `make kind-platform`
# does that for a fresh kind cluster — but both are checked, because the failure mode
# otherwise is an Ingress nobody serves and a Certificate nobody signs.
kubectl get ingressclass traefik >/dev/null 2>&1 \
  || { echo "make: helm-install: no IngressClass 'traefik' — run \`make kind-platform\` on a fresh cluster" >&2; exit 1; }
kubectl get crd clusterissuers.cert-manager.io >/dev/null 2>&1 \
  || { echo "make: helm-install: cert-manager CRDs absent — run \`make kind-platform\` on a fresh cluster" >&2; exit 1; }

# The private CA (cluster-scoped, shared by every environment on the cluster). Applied
# every run, waited on every run: idempotent, and a fresh cluster gets a Ready issuer
# before the chart asks it for anything.
kubectl apply -f deploy/k8s/cert-manager/andara-ca.yaml >/dev/null
kubectl -n cert-manager wait --for=condition=Ready certificate/andara-ca --timeout=120s >/dev/null
kubectl wait --for=condition=Ready clusterissuer/andara-ca --timeout=60s >/dev/null
echo "helm-install: clusterissuer andara-ca is ready"

if [[ "$ENVNAME" == "local" ]]; then
  # Zone Definition JSON as a ConfigMap, mounted at /content (values/local.yaml sets
  # server.content.source: dir). Recreated every run so a fixture edit is applied.
  kubectl -n "$NS" create configmap andara-content \
    --from-file=testdata/content/valid \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: configmap andara-content from testdata/content/valid"
  # The core Template pack (AW-SRV-022), mounted at /content/templates: a ConfigMap
  # cannot hold the templates/ subdirectory, and a Character is made from
  # andara.core.Character (AW-SRV-014), so the boot refuses a World without it.
  kubectl -n "$NS" create configmap andara-content-templates \
    --from-file=content/core/templates \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: configmap andara-content-templates from content/core/templates"

  # The session-token keyring (AW-SRV-008), same per-machine key `make up` uses, under the
  # name values/local.yaml gives secrets.tokenKey. A real deployment provisions this
  # Secret from its secret store; the rotation procedure is in server/README.md.
  "$REPO/scripts/auth_keys.sh" >/dev/null
  AUTH_DIR="${ANDARA_AUTH_DIR:-$REPO/.local/auth}"
  kubectl -n "$NS" create secret generic andara-server-token-key \
    --from-file=token.keys="$AUTH_DIR/token-keys" \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: secret andara-server-token-key from $AUTH_DIR"

  # The first operator, local only; the value is the same one `make up` uses.
  kubectl -n "$NS" create secret generic andara-server-bootstrap \
    --from-literal=bootstrap-operator="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}" \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: secret andara-server-bootstrap (operator:andara-local unless ANDARA_BOOTSTRAP_OPERATOR is set)"
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

# The trust bundle for anything outside the cluster: the CA that signed the edge
# certificate, read from the server certificate's Secret (cert-manager writes the
# issuing CA into ca.crt). Its own path, not .local/tls/ca.pem — that is the compose
# stack's CA (`make tls`), and the two must not overwrite each other.
HOST="$(awk '/^host:/{print $2}' "$VALUES")"
ISSUER="$(helm -n "$NS" get values andara --all -o json | "${PY:-python3}" -c 'import json,sys; print(json.load(sys.stdin)["tls"]["issuer"])')"
CA_DIR="$REPO/.local/tls/cluster/$ENVNAME"
mkdir -p "$CA_DIR"
kubectl -n "$NS" wait --for=condition=Ready certificate/andara-server --timeout=120s >/dev/null
kubectl -n "$NS" get secret andara-server-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > "$CA_DIR/ca.pem"
if kubectl -n "$NS" get certificate andara-edge >/dev/null 2>&1; then
  kubectl -n "$NS" wait --for=condition=Ready certificate/andara-edge --timeout=120s >/dev/null
  echo "helm-install: edge https://$HOST (issuer $ISSUER); private CA written to $CA_DIR/ca.pem"
  if [[ "$ISSUER" == "andara-ca" ]]; then
    echo "helm-install:   andara-cli --server-address $HOST:443 --tls-ca $CA_DIR/ca.pem ..."
  else
    echo "helm-install:   andara-cli --server-address $HOST:443 ...   (public issuer; the system trust store suffices)"
  fi
  if ! getent hosts "$HOST" >/dev/null 2>&1; then
    echo "helm-install:   $HOST does not resolve here; add to /etc/hosts:   127.0.0.1 $HOST"
  fi
else
  echo "helm-install: ingress.enabled=false — no edge; private CA written to $CA_DIR/ca.pem"
fi
