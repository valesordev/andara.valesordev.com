#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# `helm upgrade --install` for one environment, idempotent (AW-INF-003).
#
# local: the image comes from `make image && make kind-load` (pullPolicy Never). dev and
# prod pull the image .github/workflows/publish.yaml pushes to ghcr (AW-INF-013); a TAG= on
# the command line pins one, and scripts/helm_image_args.sh is the rule.
#
# The broker: local and dev use the shared Kafka `andara-log` in andara-shared (AW-INF-014,
# AW-INF-055, AW-INF-059, AW-INF-060), which `make shared-up` creates; this script refuses to touch
# the release until the broker the values file names is Ready. CI's lifecycle cluster has no shared
# Kafka: it installs local with the overlay values/ci.yaml (`make helm-install ENV=local PROFILE=ci`,
# ADR-0013 decision 8), which keeps the sim and the Account store in memory, and the check is skipped.
#
# Content: local and dev read Zone Definitions from a ConfigMap the chart renders out of
# testdata/content/valid (contentVolume.render), as compose reads a directory, until
# AW-SRV-012 serves content from the store. prod has neither yet; its deploy path is AW-INF-007.
#
# Every environment (AW-INF-006): the issuers are checked Ready ahead of the chart and the
# private CA exported for `andara-cli`, because a chart that renders a Certificate from an
# issuer that does not exist yet waits ten minutes and then fails on a pod that cannot mount
# its Secret.
set -euo pipefail

ENVNAME="${1:?usage: helm_install.sh <env> <image> <tag> [image_set] [tag_set] [profile]}"
IMAGE="${2:-andara-server}"
TAG="${3:-dev}"
IMAGE_SET="${4:-}"
TAG_SET="${5:-}"
PROFILE="${6:-}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

CHART="deploy/helm/andara"
VALUES="deploy/helm/values/${ENVNAME}.yaml"
NS="andara-${ENVNAME}"

[[ -f "$VALUES" ]] || { echo "make: helm-install: no values file at $VALUES" >&2; exit 1; }
# PROFILE=ci layers values/ci.yaml after local's, as helm merges them: the CI lifecycle job's
# overlay (host andara.local, memory sources, no broker), in the same namespace andara-local
# (AW-INF-060 AC-10). It is the only profile, and only local has it.
VALUES_ARGS=(--values "$VALUES")
case "$PROFILE" in
  "") ;;
  ci)
    [[ "$ENVNAME" == "local" ]] \
      || { echo "make: helm-install: PROFILE=ci is an overlay on local; ENV=$ENVNAME has none" >&2; exit 2; }
    VALUES_ARGS+=(--values deploy/helm/values/ci.yaml) ;;
  *) echo "make: helm-install: PROFILE=$PROFILE: must be ci, or unset" >&2; exit 2 ;;
esac
# One key of the merged values files, as helm merges them (a later file wins, a null deletes);
# empty when unset.
vget() {
  "${PY:-python3}" "$REPO/scripts/values_get.py" "$1" "${VALUES_ARGS[@]}"
}
# main's values/dev.yaml names `:dev`, which nothing moves since AW-INF-059: dev runs what the Git
# tag `dev` pins. Installing dev by hand (the take-back path) names that build explicitly.
if [[ "$ENVNAME" == "dev" ]] && { [[ -z "$TAG_SET" ]] || ! [[ "$TAG" =~ ^sha-[0-9a-f]{12}(@sha256:[0-9a-f]{64})?$ ]]; }; then
  echo "make: helm-install: ENV=dev needs TAG=sha-<12>@sha256:<digest>, the build \`make argocd-status\` or \`make promote-log\` shows; :dev no longer moves (AW-INF-059)" >&2
  exit 2
fi
for tool in helm kubectl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "make: helm-install: $tool not found" >&2; exit 1; }
done
# Every call names its cluster: ANDARA_KUBE_CONTEXT, default kind-andara (AW-INF-053).
. scripts/kube.sh
kube_require helm-install

# One owner per namespace (AW-INF-019 AC-7): once Argo CD deploys it, a helm upgrade here
# would fight its sync. `make argocd-uninstall ENV=<env>` hands it back.
if kube_kubectl get crd applications.argoproj.io >/dev/null 2>&1 \
   && kube_kubectl -n argocd get application "$NS" >/dev/null 2>&1; then
  echo "make: helm-install: $NS is deployed by Argo CD (application $NS); see make argocd-status" >&2
  exit 1
fi

# Every install but CI's lifecycle one runs the sim and the Account store on Kafka (AW-INF-014
# AC-7; the chart's default for both is kafka). Without it the pod would crash-loop on an
# unreachable broker for the full --wait, so the release is not touched until the broker is
# Ready. The broker is the one the values file names: andara-log-kafka-bootstrap.<namespace>.svc
# (local's and dev's are the shared one in andara-shared), or a bare name in the environment's
# own namespace (prod).
USES_BROKER=""
for key in server.sim.source server.auth.store; do
  [[ "$(vget "$key")" == memory ]] || USES_BROKER=1
done
if [[ -n "$USES_BROKER" ]]; then
  KNS="$(vget server.kafka.brokers)"
  KNS="${KNS%%:*}"
  if [[ "$KNS" == *.* ]]; then KNS="${KNS#*.}"; KNS="${KNS%%.*}"; else KNS="$NS"; fi
  # A bare name is a broker in the environment's own namespace: prod's, which isn't on this
  # cluster (ADR-0013 decision 10), so nothing here installs it.
  if [[ "$KNS" == "$NS" ]]; then khint="nothing on this cluster installs a Kafka in $NS (ADR-0013 decision 10)"; else khint="run \`make shared-up\` first"; fi
  kready="$(kube_kubectl -n "$KNS" get kafka andara-log \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  [[ "$kready" == "True" ]] \
    || { echo "make: helm-install: no Ready kafka/andara-log in $KNS; $khint" >&2; exit 1; }
  echo "helm-install: kafka/andara-log in $KNS is Ready"
fi

# The platform the chart is written for: the cluster's Traefik owns the edge (AC-8) and
# cert-manager issues every certificate. `make platform-up` installs both and the ClusterIssuers
# (AW-INF-054); nothing here applies them, because an apply of andara-ca.yaml before platform-up's
# CA root restore mints a new root. The failure mode otherwise is an Ingress nobody serves and a
# Certificate nobody signs, so each is checked.
kube_kubectl get ingressclass traefik >/dev/null 2>&1 \
  || { echo "make: helm-install: no IngressClass 'traefik'; run \`make platform-up\`" >&2; exit 1; }
kube_kubectl get crd clusterissuers.cert-manager.io >/dev/null 2>&1 \
  || { echo "make: helm-install: cert-manager CRDs absent; run \`make platform-up\`" >&2; exit 1; }

# The issuers this environment needs: the private CA for the server certificate's second leg,
# always, and tls.issuer for the edge.
EDGE_ISSUER="$(vget tls.issuer)"
EDGE_ISSUER="${EDGE_ISSUER:-andara-ca}"
for issuer in andara-ca $([[ "$EDGE_ISSUER" != andara-ca ]] && echo "$EDGE_ISSUER"); do
  ready="$(kube_kubectl get clusterissuer "$issuer" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  [[ "$ready" == "True" ]] \
    || { echo "make: helm-install: ClusterIssuer $issuer is not Ready; run \`make platform-up\`" >&2; exit 1; }
  echo "helm-install: clusterissuer $issuer is ready"
done

kube_kubectl get namespace "$NS" >/dev/null 2>&1 || kube_kubectl create namespace "$NS" >/dev/null
echo "helm-install: namespace $NS"
# The shared Kafka's listener admits an andara pod only from a namespace carrying this label
# (deploy/k8s/kafka/kafka.yaml, as `make shared-up` sets it). A namespace env-destroy removed and
# this script recreated would otherwise wait out --wait on a broker it can't reach.
if [[ -n "$USES_BROKER" && "${KNS:-$NS}" != "$NS" ]]; then
  kube_kubectl label namespace "$NS" "andara.valesor/env=$ENVNAME" --overwrite >/dev/null
  echo "helm-install: namespace $NS labelled andara.valesor/env=$ENVNAME"
fi

# The Secrets the values name that other targets write. A missing one holds the pod at FailedMount
# for the whole --wait, so each is checked here and names its writer (AW-INF-056, AW-INF-058).
pairs=("secrets.kafkaCreds.secretName=kafka-creds" "secrets.snapshotS3.secretName=objectstore-install")
# The projector's own principal only when it runs; local and dev name it either way.
[[ "$(vget projectors.state.enabled)" == True ]] && pairs+=("projectors.state.kafkaCreds.secretName=kafka-creds")
for pair in "${pairs[@]}"; do
  secret="$(vget "${pair%%=*}")"
  [[ -z "$secret" ]] && continue
  kube_kubectl -n "$NS" get secret "$secret" >/dev/null 2>&1 \
    || { echo "make: helm-install: no secret $secret in $NS (${pair%%=*}); run \`make ${pair#*=} ENV=$ENVNAME\` first" >&2; exit 1; }
done

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
    if kube_kubectl -n "$NS" get configmap "$cm" >/dev/null 2>&1 \
       && [[ -z "$(kube_kubectl -n "$NS" get configmap "$cm" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}')" ]]; then
      kube_kubectl -n "$NS" label configmap "$cm" app.kubernetes.io/managed-by=Helm --overwrite >/dev/null
      kube_kubectl -n "$NS" annotate configmap "$cm" meta.helm.sh/release-name=andara meta.helm.sh/release-namespace="$NS" --overwrite >/dev/null
      echo "helm-install: configmap $cm handed to the release (the chart renders it now)"
    fi
  done

  # The session-token keyring (AW-SRV-008), same per-machine key `make up` uses, under the
  # name values/<env>.yaml gives secrets.tokenKey. A real deployment provisions this
  # Secret from its secret store; the rotation procedure is in server/README.md.
  "$REPO/scripts/auth_keys.sh" >/dev/null
  AUTH_DIR="${ANDARA_AUTH_DIR:-$REPO/.local/auth}"
  kube_kubectl -n "$NS" create secret generic andara-server-token-key \
    --from-file=token.keys="$AUTH_DIR/token-keys" \
    --dry-run=client -o yaml | kube_kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: secret andara-server-token-key from $AUTH_DIR"

  # The first operator: local's is the value `make up` uses; others were required above.
  kube_kubectl -n "$NS" create secret generic andara-server-bootstrap \
    --from-literal=bootstrap-operator="${ANDARA_BOOTSTRAP_OPERATOR:-operator:andara-local}" \
    --dry-run=client -o yaml | kube_kubectl -n "$NS" apply -f - >/dev/null
  echo "helm-install: secret andara-server-bootstrap (operator:andara-local unless ANDARA_BOOTSTRAP_OPERATOR is set)"
fi

mapfile -t IMAGE_ARGS < <("$REPO/scripts/helm_image_args.sh" "$ENVNAME" "$IMAGE" "$TAG" "$IMAGE_SET" "$TAG_SET")

# A tag without a digest is pinned to the digest it names now. `pullPolicy: Always` only
# re-resolves a tag when a container starts, so without this a rerun after the tag moved
# renders an identical pod and leaves the old build running. With it, the pod rolls
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

kube_helm upgrade --install andara "$CHART" \
  --namespace "$NS" \
  "${VALUES_ARGS[@]}" \
  "${IMAGE_ARGS[@]}" \
  --wait --timeout 10m

echo "helm-install: andara in $NS is serving ($(kube_kubectl -n "$NS" get statefulset andara -o jsonpath='{.spec.template.spec.containers[?(@.name=="server")].image}'))"
kube_kubectl -n "$NS" get statefulset andara
kube_kubectl -n "$NS" get pvc -l app=andara

# The trust bundle for anything outside the cluster: the CA that signed the edge
# certificate, read from the server certificate's Secret (cert-manager writes the
# issuing CA into ca.crt). Its own path, not .local/tls/ca.pem — that is the compose
# stack's CA (`make tls`), and the two must not overwrite each other.
HOST="$(vget host)"
# The edge's port (ADR-0013 amendment 24): the default is the public name through the workstation's
# nginx, and CI's cluster publishes its edge on the same port. ANDARA_EDGE_PORT=8443 is the kind
# edge itself, for a `--resolve <name>:8443:127.0.0.1` dial.
PORT="${ANDARA_EDGE_PORT:-443}"
ISSUER="$(kube_helm -n "$NS" get values andara --all -o json | "${PY:-python3}" -c 'import json,sys; print(json.load(sys.stdin)["tls"]["issuer"])')"
CA_DIR="$REPO/.local/tls/cluster/$ENVNAME"
mkdir -p "$CA_DIR"
kube_kubectl -n "$NS" wait --for=condition=Ready certificate/andara-server --timeout=120s >/dev/null
kube_kubectl -n "$NS" get secret andara-server-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > "$CA_DIR/ca.pem"
if kube_kubectl -n "$NS" get certificate andara-edge >/dev/null 2>&1; then
  kube_kubectl -n "$NS" wait --for=condition=Ready certificate/andara-edge --timeout=120s >/dev/null
  echo "helm-install: edge https://$HOST (issuer $ISSUER); private CA written to $CA_DIR/ca.pem"
  if [[ "$ISSUER" == "andara-ca" ]]; then
    echo "helm-install:   andara-cli --server-address $HOST:$PORT --tls-ca $CA_DIR/ca.pem ..."
  else
    echo "helm-install:   andara-cli --server-address $HOST:$PORT ...   (public issuer; the system trust store suffices)"
  fi
  # The line above dials by DNS. A name with no DNS yet (CI's andara.local) reaches the edge only
  # through 127.0.0.1, and so does an overridden port: the kind edge's 8443 listens on 127.0.0.1
  # only (deploy/kind/cluster.yaml), and the public name resolves to the tailnet, where only nginx
  # listens (ADR-0013 amendment 24). Then name the dial that does connect.
  ADDRS="$(getent ahostsv4 "$HOST" 2>/dev/null | awk '{print $1}' | sort -u)"
  if [[ -z "$ADDRS" ]]; then
    echo "helm-install:   $HOST does not resolve here; dial it with curl --resolve $HOST:$PORT:127.0.0.1"
  elif [[ -n "${ANDARA_EDGE_PORT:-}" ]] && ! grep -qx '127\.0\.0\.1' <<<"$ADDRS"; then
    echo "helm-install:   $HOST does not resolve to 127.0.0.1 here, and $PORT listens only there; dial it with curl --resolve $HOST:$PORT:127.0.0.1"
  fi
else
  echo "helm-install: ingress.enabled=false — no edge; private CA written to $CA_DIR/ca.pem"
fi
