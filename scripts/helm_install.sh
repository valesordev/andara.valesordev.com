#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `helm upgrade --install` for one environment, idempotent (AW-INF-003).
#
# local: the image comes from `make image && make kind-load` (pullPolicy Never). dev and
# prod pull the image .github/workflows/publish.yaml pushes to ghcr (AW-INF-013); a TAG= on
# the command line pins one, and scripts/helm_image_args.sh is the rule.
#
# The broker: local runs without one (accounts and commands in memory). dev and prod use
# their namespace's Kafka `andara-log` (AW-INF-014), which `make kafka-install` creates;
# this script refuses to touch their release until it is Ready.
#
# Content: local and dev read Zone Definitions from a ConfigMap the chart renders out of
# testdata/content/valid (contentVolume.render), as compose reads a directory, until
# AW-SRV-012 serves content from the store. prod has neither yet; its deploy path is AW-INF-007.
#
# Every environment (AW-INF-006): the private CA is bootstrapped ahead of the chart and
# its certificate exported for `andara-cli`, because a chart that renders a Certificate
# from an issuer that does not exist yet waits ten minutes and then fails on a pod that
# cannot mount its Secret.
set -euo pipefail

ENVNAME="${1:?usage: helm_install.sh <env> <image> <tag> [image_set] [tag_set]}"
IMAGE="${2:-andara-server}"
TAG="${3:-dev}"
IMAGE_SET="${4:-}"
TAG_SET="${5:-}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

CHART="deploy/helm/andara"
VALUES="deploy/helm/values/${ENVNAME}.yaml"
NS="andara-${ENVNAME}"

[[ -f "$VALUES" ]] || { echo "make: helm-install: no values file at $VALUES" >&2; exit 1; }
for tool in helm kubectl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "make: helm-install: $tool not found" >&2; exit 1; }
done

# One owner per namespace (AW-INF-019 AC-7): once Argo CD deploys it, a helm upgrade here
# would fight its sync. `make argocd-uninstall ENV=<env>` hands it back.
if kubectl get crd applications.argoproj.io >/dev/null 2>&1 \
   && kubectl -n argocd get application "$NS" >/dev/null 2>&1; then
  echo "make: helm-install: $NS is deployed by Argo CD (application $NS); see make argocd-status" >&2
  exit 1
fi

kubectl get namespace "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS" >/dev/null
echo "helm-install: namespace $NS"

# dev and prod run the sim and the Account store on their namespace's Kafka (AW-INF-014
# AC-7). Without it the pod would crash-loop on an unreachable broker for the full --wait,
# so the release is not touched until the broker is Ready.
if [[ "$ENVNAME" != "local" ]]; then
  kready="$(kubectl -n "$NS" get kafka andara-log \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  [[ "$kready" == "True" ]] \
    || { echo "make: helm-install: no Ready kafka/andara-log in $NS; run \`make kafka-install ENV=$ENVNAME\` first" >&2; exit 1; }
  echo "helm-install: kafka/andara-log in $NS is Ready"
fi

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

# Environments whose content comes from ConfigMaps (server.content.source: dir) and whose
# token key and first operator this script provisions. dev leaves this list when
# AW-SRV-012 serves content from the store.
CONTENT_FROM_CONFIGMAP="local dev"

if [[ " $CONTENT_FROM_CONFIGMAP " == *" $ENVNAME "* ]]; then
  # The first operator's credential. local's is the published default `make up` uses;
  # anywhere else the edge is a public hostname, so a known password is refused and the
  # caller supplies one (the same variable `make stream-soak` reads).
  if [[ "$ENVNAME" != "local" && -z "${ANDARA_BOOTSTRAP_OPERATOR:-}" ]]; then
    echo "make: helm-install: ENV=$ENVNAME needs ANDARA_BOOTSTRAP_OPERATOR=<user>:<password>; the local default is public" >&2
    exit 1
  fi

  # Zone Definitions and the core Template pack are rendered by the chart now
  # (contentVolume.render, AW-INF-019), from the same directories this script used to
  # `kubectl create` them from. ConfigMaps an earlier run created carry no Helm ownership,
  # and `helm upgrade` refuses an object it doesn't own, so they're handed to the release
  # before it runs: one writer, whichever deploy path runs.
  for cm in andara-content andara-content-templates; do
    if kubectl -n "$NS" get configmap "$cm" >/dev/null 2>&1 \
       && [[ -z "$(kubectl -n "$NS" get configmap "$cm" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}')" ]]; then
      kubectl -n "$NS" label configmap "$cm" app.kubernetes.io/managed-by=Helm --overwrite >/dev/null
      kubectl -n "$NS" annotate configmap "$cm" meta.helm.sh/release-name=andara meta.helm.sh/release-namespace="$NS" --overwrite >/dev/null
      echo "helm-install: configmap $cm handed to the release (the chart renders it now)"
    fi
  done

  # The session-token keyring (AW-SRV-008), same per-machine key `make up` uses, under the
  # name values/<env>.yaml gives secrets.tokenKey. A real deployment provisions this
  # Secret from its secret store; the rotation procedure is in server/README.md.
  "$REPO/scripts/auth_keys.sh" >/dev/null
  AUTH_DIR="${ANDARA_AUTH_DIR:-$REPO/.local/auth}"
  kubectl -n "$NS" create secret generic andara-server-token-key \
    --from-file=token.keys="$AUTH_DIR/token-keys" \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: secret andara-server-token-key from $AUTH_DIR"

  # The first operator: local's is the value `make up` uses; others were required above.
  kubectl -n "$NS" create secret generic andara-server-bootstrap \
    --from-literal=bootstrap-operator="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}" \
    --dry-run=client -o yaml | kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: secret andara-server-bootstrap (operator:andara-local unless ANDARA_BOOTSTRAP_OPERATOR is set)"
fi

mapfile -t IMAGE_ARGS < <("$REPO/scripts/helm_image_args.sh" "$ENVNAME" "$IMAGE" "$TAG" "$IMAGE_SET" "$TAG_SET")

# A moving tag (`:dev`) is pinned to the digest it names now. `pullPolicy: Always` only
# re-resolves a tag when a container starts, so without this a rerun after `publish` moved
# the tag renders an identical pod and leaves the old build running. With it, the pod rolls
# exactly when the tag has moved, and the pod spec records which build it runs. `sha-` tags
# are immutable and pass through; local's image is kind-loaded and never resolved.
if [[ "$ENVNAME" != "local" ]]; then
  read -r eff_repo eff_tag < <("${PY:-python3}" - "$VALUES" "$IMAGE" "$TAG" "$IMAGE_SET" "$TAG_SET" <<'PYEOF'
import sys, yaml
values, image, tag, image_set, tag_set = sys.argv[1:6]
v = (yaml.safe_load(open(values)) or {}).get("image", {})
print(image if image_set else v.get("repository", ""), tag if tag_set else v.get("tag", ""))
PYEOF
)
  if [[ "$eff_tag" != sha-* && "$eff_tag" != *@* ]]; then
    digest="$("$REPO/scripts/image_digest.sh" "$eff_repo" "$eff_tag")" \
      || { echo "make: helm-install: cannot resolve $eff_repo:$eff_tag to a digest; is it published? (make image-check ENV=$ENVNAME)" >&2; exit 1; }
    IMAGE_ARGS+=(--set "image.tag=${eff_tag}@${digest}")
    echo "helm-install: $eff_repo:$eff_tag is $digest"
  fi
fi

helm upgrade --install andara "$CHART" \
  --namespace "$NS" \
  --values "$VALUES" \
  "${IMAGE_ARGS[@]}" \
  --wait --timeout 10m

echo "helm-install: andara in $NS is serving ($(kubectl -n "$NS" get statefulset andara -o jsonpath='{.spec.template.spec.containers[?(@.name=="server")].image}'))"
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
