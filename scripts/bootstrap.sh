#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Install and verify the toolchain `make check` and `make up` need.
#
# Idempotent by construction: every step either verifies something already correct or
# installs a pinned version into ./bin, which is gitignored and removed by `make clean`.
# Running it twice leaves the machine in the same state and exits 0 both times.
#
# Tools go into ./bin rather than $GOPATH/bin so that a developer's global toolchain and
# this repo's cannot drift apart, and so that the pinned versions below are the versions
# CI and every machine actually run.
set -euo pipefail

# Pinned deliberately. An unpinned `@latest` is how a config that parses today stops
# parsing tomorrow — golangci-lint v1 config against a v2 binary is exactly that failure.
GOLANGCI_VERSION="v2.14.0"
BUF_VERSION="v1.72.0"
GRPCURL_VERSION="v1.9.4"
HELM_VERSION="v3.22.0"
KUBECONFORM_VERSION="v0.8.0"
# actionlint validates .github/workflows/terraform.yaml in `make tf-workflow-check` (AW-INF-068).
ACTIONLINT_VERSION="v1.7.12"
# gcx is the only way scripts read Grafana (ADR-0012 decision 2; AW-INF-075). At least v0.2.13:
# v0.2.11 ignores --context in some operations. The pin moves only with a change that re-records
# scripts/tests/fixtures/gcx/. GCX_SUM is the module's go.sum hash, checked against the binary's
# build info, so a binary built from anything else is replaced.
GCX_VERSION="v1.5.0"
GCX_SUM="h1:oqu4+BSIyADxYqZZrooZNs+2Ea/nnN/E9fPD4I6vfQk="
# The compose Prometheus's version (deploy/compose/docker-compose.yaml), so the rules are
# checked by the parser that loads them. A release tarball, not a Go module — see below.
PROMETHEUS_VERSION="3.1.0"
# mimirtool syncs files/alerts.yaml to the Grafana Cloud ruler (AW-INF-009). Also a release
# binary: Mimir's tags aren't Go module versions, so `go install` can't pin it.
MIMIRTOOL_VERSION="3.2.2"
# Terraform plans and applies the Grafana Cloud roots (AW-INF-046, ADR-0012). The version is
# deploy/terraform/grafana/.terraform-version; the checksums below are that release's
# SHA256SUMS, so a version bump changes both or bootstrap fails.
TERRAFORM_VERSION="1.16.4"
# kind and kubectl back `make cluster-up`, `cluster-down` and `cluster-rebuild` (AW-INF-052), which
# call bin/kind and bin/kubectl, never whatever is on PATH. Release binaries, verified against
# checksums pinned below from kind's <binary>.sha256sum and dl.k8s.io's kubectl.sha256. The node
# image is deploy/platform/versions.yaml's, one kind v0.33.0 lists for itself; kubectl's minor
# stays within one of it, checked below.
KIND_VERSION="v0.33.0"
KUBECTL_VERSION="v1.36.4"
# alloy validates the Fleet Management pipelines in `make pipelines-check` (AW-INF-051). A release
# zip, verified against checksums pinned below from the release's SHA256SUMS.
ALLOY_VERSION="v1.20.1"

fail() { echo "make: bootstrap: $*" >&2; exit 1; }
ok()   { printf '  %-22s %s\n' "$1" "$2"; }

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"
BIN="$REPO/bin"
mkdir -p "$BIN"

echo "bootstrap: verifying toolchain"

command -v git >/dev/null 2>&1 || fail "git not found; install git and re-run"
ok git "$(git --version | awk '{print $3}')"

command -v "${PY:-python3}" >/dev/null 2>&1 || fail "python3 not found; install Python 3.9+ and re-run"
PYV="$(${PY:-python3} -c 'import sys; print("%d.%d" % sys.version_info[:2])')"
${PY:-python3} -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' \
  || fail "python3 is $PYV; this repo needs 3.9+"
ok python3 "$PYV"

# The newest Go any pinned tool below declares in its go.mod (golangci-lint v2.14.0: 1.26.0).
# go.mod's own `go` line can be older (CI's setup-go installs exactly that). Unless GOTOOLCHAIN
# is `local` or a bare pinned version, `go install` can switch to a newer toolchain itself (auto
# downloads one, +path takes one from PATH), so only note it; otherwise it would fail halfway,
# so say so up front.
GO_MIN_MINOR=26
command -v go >/dev/null 2>&1 || fail "go not found; install Go 1.$GO_MIN_MINOR+ and re-run"
GOV="$(go env GOVERSION | sed 's/^go//')"
ok go "$GOV"
# Go 1 only: the minor is the digits after the first dot ("1.27.2-X:nodwarf5" -> 27, "1.26rc1" -> 26).
GOV_REST="${GOV#*.}"
GOV_MINOR="${GOV_REST%%[!0-9]*}"
if [[ "$GOV" != 1.* || -z "$GOV_MINOR" ]] || (( GOV_MINOR < GO_MIN_MINOR )); then
  GOTC="$(go env GOTOOLCHAIN)"
  if [[ "$GOTC" == local || ( "$GOTC" == go* && "$GOTC" != *+* ) ]]; then
    fail "go is $GOV and GOTOOLCHAIN=$GOTC won't switch to a newer one; the pinned tools need Go 1.$GO_MIN_MINOR+ (raise GO_MIN_MINOR when a pin needs more)"
  fi
  echo "  note: go is $GOV; GOTOOLCHAIN=$GOTC may switch to Go 1.$GO_MIN_MINOR+ to build the pinned tools"
fi

[[ -f go.mod ]] || fail "go.mod is missing; this repo should not be in that state"

# install_pinned <binary> <module> <version> [<go.sum hash>]
# Installs only when the binary is absent or is the wrong version, so the common case is
# a version check and nothing else. With a hash, the binary's module hash must match it too.
install_pinned() {
  local name="$1" module="$2" want="$3" sum="${4:-}"
  local have="" have_sum=""
  if [[ -x "$BIN/$name" ]]; then
    # The module version stamped into the binary, not `--version` output: kubeconform built
    # by `go install` reports "development", helm prints a struct, and grpcurl says "dev
    # build". `go version -m` reads the build info every Go binary carries, so one check
    # covers every pinned tool.
    have="$(go version -m "$BIN/$name" 2>/dev/null | awk '$1=="mod"{print $3; exit}' || true)"
    have_sum="$(go version -m "$BIN/$name" 2>/dev/null | awk '$1=="mod"{print $4; exit}' || true)"
  fi
  if [[ "${have}" == "$want" && ( -z "$sum" || "$have_sum" == "$sum" ) ]]; then
    ok "$name" "${have#v} (pinned)"
    return 0
  fi
  echo "  installing $name $want ..."
  GOBIN="$BIN" go install "${module}@${want}" \
    || fail "could not install $name $want; check network access to the Go module proxy"
  if [[ -n "$sum" ]]; then
    have_sum="$(go version -m "$BIN/$name" 2>/dev/null | awk '$1=="mod"{print $4; exit}' || true)"
    [[ "$have_sum" == "$sum" ]] \
      || { rm -f "$BIN/$name"; fail "$name $want built with module hash ${have_sum:-<none>}, not the pinned $sum; removed it"; }
  fi
  ok "$name" "$want (installed)"
}

# Python dependencies beyond the standard library, pinned in scripts/requirements.txt:
# PyYAML backs scripts/values_schema.py and scripts/helm_test.py (AW-INF-003) — rendered
# manifests are real YAML and deserve a real parser — reuse backs `make license-check`, and
# lark backs `make content-grammar-check` (AW-CLI-005 AC-1), which parses the Content
# Language corpus against the grammar rather than taking the grammar's word for it.
# Installed only when an import fails, so a distro-packaged copy is left alone; a refusal
# (PEP 668 externally-managed environments) is reported with the file to install rather
# than forced past.
pydeps_missing=""
for mod in yaml reuse lark; do
  ${PY:-python3} -c "import $mod" >/dev/null 2>&1 || pydeps_missing="$pydeps_missing $mod"
done
if [ -n "$pydeps_missing" ]; then
  echo "  installing$pydeps_missing from scripts/requirements.txt ..."
  ${PY:-python3} -m pip install --quiet --user -r scripts/requirements.txt 2>/dev/null \
    || fail "python modules missing:$pydeps_missing; pip refused to install them (run: python3 -m pip install -r scripts/requirements.txt, or install your distro's packages)"
fi
ok PyYAML "$(${PY:-python3} -c 'import yaml; print(yaml.__version__)')"
ok reuse "$(${PY:-python3} -m reuse --version | awk 'NR==1{print $NF}')"
ok lark "$(${PY:-python3} -c 'import lark; print(lark.__version__)')"

# golangci-lint is needed by `make lint`, which is a no-op until Go sources exist. Install
# it anyway once sources appear; before that, skip the download nobody needs yet.
if find . -name '*.go' -not -path './.git/*' -not -path './bin/*' -print -quit | grep -q .; then
  install_pinned golangci-lint github.com/golangci/golangci-lint/v2/cmd/golangci-lint "$GOLANGCI_VERSION"
else
  ok golangci-lint "deferred (no Go sources yet)"
fi

# buf drives `make proto` and `make proto-check`. Same reasoning: needed once .proto
# sources exist (AW-SRV-005), not before.
if find docs/specs/protocol -name '*.proto' -print -quit 2>/dev/null | grep -q .; then
  install_pinned buf github.com/bufbuild/buf/cmd/buf "$BUF_VERSION"
else
  ok buf "deferred (no .proto sources yet)"
fi

# grpcurl is how the Protocol is poked by hand (ADR-0003: "debugging is grpcurl, not nc")
# and what AW-SRV-005's operator test plan runs. Needed once there is a server to poke.
if find cmd/andara-server -name '*.go' -print -quit 2>/dev/null | grep -q .; then
  install_pinned grpcurl github.com/fullstorydev/grpcurl/cmd/grpcurl "$GRPCURL_VERSION"
else
  ok grpcurl "deferred (no server yet)"
fi

# Needed by `make up`, not by `make check`. Report rather than fail, so a developer who
# only wants to validate stories is not forced to install a container runtime.
if command -v docker >/dev/null 2>&1; then
  ok docker "$(docker --version | awk '{print $3}' | tr -d ,)"
  if docker compose version >/dev/null 2>&1; then
    ok "docker compose" "$(docker compose version --short 2>/dev/null || echo present)"
  else
    ok "docker compose" "MISSING — \`make up\` needs the compose plugin"
  fi
else
  ok docker "not installed (needed by \`make up\` and \`make cluster-up\`, not by \`make check\`)"
fi

# `make tls` provisions the local CA that AW-INF-002 requires the CLI to trust.
if command -v openssl >/dev/null 2>&1; then
  ok openssl "$(openssl version | awk '{print $2}')"
else
  ok openssl "not installed (needed by \`make tls\`, not by \`make check\`)"
fi

# helm and kubeconform back `make k8s-dry`, which validates the chart against the pinned
# Kubernetes version on every `make check` (AW-INF-003 AC-1). Both are Go modules, so they
# pin the same way buf does. The system helm may be a different major; ./bin wins on PATH.
install_pinned helm helm.sh/helm/v3/cmd/helm "$HELM_VERSION"
install_pinned kubeconform github.com/yannh/kubeconform/cmd/kubeconform "$KUBECONFORM_VERSION"
install_pinned actionlint github.com/rhysd/actionlint/cmd/actionlint "$ACTIONLINT_VERSION"
install_pinned gcx github.com/grafana/gcx/cmd/gcx "$GCX_VERSION" "$GCX_SUM"

# promtool backs `make helm-test`'s rule checks (AW-INF-008): `check rules` over the chart's
# alerts.yaml and `test rules` over deploy/helm/tests/alerts_test.yaml. Not `go install`:
# prometheus/prometheus's go.mod carries replace directives, which `go install pkg@version`
# refuses outright. So the upstream release tarball, verified against a checksum pinned here
# from the release's sha256sums.txt, and only promtool extracted from it.
install_promtool() {
  local want="$PROMETHEUS_VERSION" have="" os arch sum
  [[ -x "$BIN/promtool" ]] && have="$("$BIN/promtool" --version 2>/dev/null | awk 'NR==1{print $3}')"
  if [[ "$have" == "$want" ]]; then
    ok promtool "$have (pinned)"
    return 0
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "no pinned promtool for $(uname -m)" ;;
  esac
  case "$os-$arch" in
    linux-amd64)  sum=9a9d1e115d1745826b13aec3f1409780b9fcf1d4206746cb4faee46ca5add70c ;;
    linux-arm64)  sum=c12b9e368006873df0c8865771af0434a3edfc8c8eaeea62b73d3974bff7f2a1 ;;
    darwin-amd64) sum=de958826f9ca20003b30c48fa3f2fcfa67b2a3aebcabd6230e90bb94f92d86e8 ;;
    darwin-arm64) sum=8ebe1d60d8864ae3e10a359c3b543563e972505e5ef6fe1d00586d86a7e813fc ;;
    *) fail "no pinned promtool for $os-$arch" ;;
  esac
  local name="prometheus-$want.$os-$arch" tmp
  tmp="$(mktemp -d)"
  echo "  installing promtool $want ..."
  curl -fsSL -o "$tmp/$name.tar.gz" \
    "https://github.com/prometheus/prometheus/releases/download/v$want/$name.tar.gz" \
    || { rm -rf "$tmp"; fail "could not download prometheus $want; check network access to github.com"; }
  if command -v sha256sum >/dev/null 2>&1; then
    echo "$sum  $tmp/$name.tar.gz" | sha256sum -c --quiet - >/dev/null
  else
    echo "$sum  $tmp/$name.tar.gz" | shasum -a 256 -c --quiet - >/dev/null
  fi || { rm -rf "$tmp"; fail "prometheus $want tarball does not match its pinned checksum"; }
  tar -xzf "$tmp/$name.tar.gz" -C "$tmp" "$name/promtool"
  install -m 0755 "$tmp/$name/promtool" "$BIN/promtool"
  rm -rf "$tmp"
  ok promtool "$want (installed)"
}
install_promtool

# mimirtool backs `make alerts-sync` and `make alerts-diff` (AW-INF-009). The release binary,
# verified against a checksum pinned here from the release's mimirtool-<os>-<arch>-sha-256.
install_mimirtool() {
  local want="$MIMIRTOOL_VERSION" have="" os arch sum
  [[ -x "$BIN/mimirtool" ]] && have="$("$BIN/mimirtool" version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
  if [[ "$have" == "$want" ]]; then
    ok mimirtool "$have (pinned)"
    return 0
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "no pinned mimirtool for $(uname -m)" ;;
  esac
  case "$os-$arch" in
    linux-amd64)  sum=1d70a24d08660b7b2e7432228006919352e352d840ba2f15ab188a34b16c4de1 ;;
    linux-arm64)  sum=2ef14e261955989a69ca1df15b47646d4da1e3c5c53666cb87b6c6f685321087 ;;
    darwin-amd64) sum=e3537b56d65c621e66d844a6aa9c1fc5c744b91c6791f3f2ca1ecb64b378e345 ;;
    darwin-arm64) sum=84b4fd631e44b72659eb25036fdf6ce54a85e75b1de433a2b3aad557d0278ec8 ;;
    *) fail "no pinned mimirtool for $os-$arch" ;;
  esac
  local tmp
  tmp="$(mktemp -d)"
  echo "  installing mimirtool $want ..."
  curl -fsSL -o "$tmp/mimirtool" \
    "https://github.com/grafana/mimir/releases/download/mimir-$want/mimirtool-$os-$arch" \
    || { rm -rf "$tmp"; fail "could not download mimirtool $want; check network access to github.com"; }
  if command -v sha256sum >/dev/null 2>&1; then
    echo "$sum  $tmp/mimirtool" | sha256sum -c --quiet - >/dev/null
  else
    echo "$sum  $tmp/mimirtool" | shasum -a 256 -c --quiet - >/dev/null
  fi || { rm -rf "$tmp"; fail "mimirtool $want does not match its pinned checksum"; }
  install -m 0755 "$tmp/mimirtool" "$BIN/mimirtool"
  rm -rf "$tmp"
  ok mimirtool "$want (installed)"
}
install_mimirtool

# terraform backs `make tf-*` (AW-INF-046): fmt, validate and test in `make check`, plan and
# apply of solo7dev from the dev box. HashiCorp's release zip, verified against a checksum pinned
# here from the release's SHA256SUMS.
install_terraform() {
  local want="$TERRAFORM_VERSION" have="" os arch sum pinned
  pinned="$(tr -d '[:space:]' < deploy/terraform/grafana/.terraform-version)"
  [[ "$pinned" == "$want" ]] \
    || fail "deploy/terraform/grafana/.terraform-version says $pinned but bootstrap pins terraform $want; change both together"
  [[ -x "$BIN/terraform" ]] && have="$("$BIN/terraform" version -json 2>/dev/null | ${PY:-python3} -c 'import json,sys; print(json.load(sys.stdin)["terraform_version"])' 2>/dev/null || true)"
  if [[ "$have" == "$want" ]]; then
    ok terraform "$have (pinned)"
    return 0
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "no pinned terraform for $(uname -m)" ;;
  esac
  case "$os-$arch" in
    linux-amd64)  sum=dc94af0eef1147718ad7c8daea792ed199e3e0492eec180d0adafa2a65a879df ;;
    linux-arm64)  sum=8263f301cb1a24489a4adeed147bf28504053f77237b3ea97a0ef2972659de30 ;;
    darwin-amd64) sum=2ee4b62064086e4b24b0d6cf2e61718fbaf0556feba990f708a5e32557554b3b ;;
    darwin-arm64) sum=42cfdf97ad722f79085fe2279b06d4b8680172de3534b22eeddd9a0fbbe7b8f1 ;;
    *) fail "no pinned terraform for $os-$arch" ;;
  esac
  local name="terraform_${want}_${os}_${arch}.zip" tmp
  tmp="$(mktemp -d)"
  echo "  installing terraform $want ..."
  curl -fsSL -o "$tmp/$name" "https://releases.hashicorp.com/terraform/$want/$name" \
    || { rm -rf "$tmp"; fail "could not download terraform $want; check network access to releases.hashicorp.com"; }
  if command -v sha256sum >/dev/null 2>&1; then
    echo "$sum  $tmp/$name" | sha256sum -c --quiet - >/dev/null
  else
    echo "$sum  $tmp/$name" | shasum -a 256 -c --quiet - >/dev/null
  fi || { rm -rf "$tmp"; fail "terraform $want zip does not match its pinned checksum"; }
  ${PY:-python3} -c 'import sys, zipfile; zipfile.ZipFile(sys.argv[1]).extract("terraform", sys.argv[2])' "$tmp/$name" "$tmp" \
    || { rm -rf "$tmp"; fail "could not unpack terraform $want"; }
  install -m 0755 "$tmp/terraform" "$BIN/terraform"
  rm -rf "$tmp"
  ok terraform "$want (installed)"
}
install_terraform

# sha256_ok <sum> <file>: sha256sum on Linux, shasum on macOS.
sha256_ok() {
  if command -v sha256sum >/dev/null 2>&1; then
    echo "$1  $2" | sha256sum -c --quiet - >/dev/null
  else
    echo "$1  $2" | shasum -a 256 -c --quiet - >/dev/null
  fi
}

# install_binary <name> <version> <have> <url> <sum>: download url to bin/<name> unless have is
# already the version, verifying the pinned checksum before anything lands in bin/.
install_binary() {
  local name="$1" want="$2" have="$3" url="$4" sum="$5" tmp
  if [[ "$have" == "$want" ]]; then
    ok "$name" "${have#v} (pinned)"
    return 0
  fi
  tmp="$(mktemp -d)"
  echo "  installing $name $want ..."
  curl -fsSL -o "$tmp/$name" "$url" \
    || { rm -rf "$tmp"; fail "could not download $name $want from $url; check network access"; }
  sha256_ok "$sum" "$tmp/$name" \
    || { rm -rf "$tmp"; fail "$name $want does not match its pinned checksum"; }
  install -m 0755 "$tmp/$name" "$BIN/$name"
  rm -rf "$tmp"
  ok "$name" "${want#v} (installed)"
}

# kind and kubectl back the cluster targets (AW-INF-052) and `make helm-install ENV=local`.
install_kind_kubectl() {
  local os arch kind_sum kubectl_sum node_minor kubectl_minor have
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "no pinned kind or kubectl for $(uname -m)" ;;
  esac
  case "$os-$arch" in
    linux-amd64)  kind_sum=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d ;;
    linux-arm64)  kind_sum=20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2 ;;
    darwin-amd64) kind_sum=5a99f26f57246dc9319dd294803313197a0f34d33c525b3ea8b655db5916ece0 ;;
    darwin-arm64) kind_sum=0c8c7dbe5e23594a198b786c4bc13dacc101fa6196b0cb0b23a1ca44e61f4b4f ;;
    *) fail "no pinned kind for $os-$arch" ;;
  esac
  case "$os-$arch" in
    linux-amd64)  kubectl_sum=8b8f088da2dab964f853b38464033b1be15ede2839eca751482357c45abdd05a ;;
    linux-arm64)  kubectl_sum=0ecf44450ee6063bf19dd166a103ee6df4a9034455c2abce626e6eea657d73fb ;;
    darwin-amd64) kubectl_sum=71a3aa7c2ee2c974d9fbb462cba0c5c04a4df2e8d85eee94714fd819ea3c4e63 ;;
    darwin-arm64) kubectl_sum=c9e4f713d6fee0043a3d835cca13077cda2bc0973840eb9779360df0b5bdfc69 ;;
    *) fail "no pinned kubectl for $os-$arch" ;;
  esac

  # kubectl's minor within one of the node image's (Kubernetes' version skew policy).
  node_minor="$(sed -nE 's/^ *nodeImage: *kindest\/node:v1\.([0-9]+)\..*/\1/p' deploy/platform/versions.yaml)"
  kubectl_minor="$(echo "$KUBECTL_VERSION" | sed -nE 's/^v1\.([0-9]+)\..*/\1/p')"
  [[ -n "$node_minor" && -n "$kubectl_minor" ]] \
    && (( kubectl_minor - node_minor <= 1 && node_minor - kubectl_minor <= 1 )) \
    || fail "kubectl $KUBECTL_VERSION is not within one minor of deploy/platform/versions.yaml's node image (v1.${node_minor:-?}); change them together"

  have=""
  [[ -x "$BIN/kind" ]] && have="$("$BIN/kind" version 2>/dev/null | awk '{print $2}' || true)"
  install_binary kind "$KIND_VERSION" "$have" \
    "https://github.com/kubernetes-sigs/kind/releases/download/$KIND_VERSION/kind-$os-$arch" "$kind_sum"

  have=""
  [[ -x "$BIN/kubectl" ]] && have="$("$BIN/kubectl" version --client -o json 2>/dev/null \
    | ${PY:-python3} -c 'import json,sys; print(json.load(sys.stdin)["clientVersion"]["gitVersion"])' 2>/dev/null || true)"
  install_binary kubectl "$KUBECTL_VERSION" "$have" \
    "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/$os/$arch/kubectl" "$kubectl_sum"
}
install_kind_kubectl

# alloy backs `make pipelines-check` (AW-INF-051): `alloy validate` over every Fleet pipeline source. A
# release zip, verified against a checksum pinned here from the release's SHA256SUMS; only the binary is
# extracted, as bin/alloy.
install_alloy() {
  local want="$ALLOY_VERSION" have="" os arch sum name tmp
  [[ -x "$BIN/alloy" ]] && have="$("$BIN/alloy" --version 2>/dev/null | awk 'NR==1{print $3}' || true)"
  if [[ "$have" == "$want" ]]; then
    ok alloy "${have#v} (pinned)"
    return 0
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "no pinned alloy for $(uname -m)" ;;
  esac
  case "$os-$arch" in
    linux-amd64)  sum=451fe650e8277d22d69cb8db50bba809f581fe78decba7fce4027ef185457be9 ;;
    linux-arm64)  sum=396827d58158f181c98d5776c908348b38b4099f1d18a1e7554f2fda0fcb9203 ;;
    darwin-amd64) sum=8ae0c7f56e4658093d15e5dea8ca5cfa4ed382bd99fca059418cf3d647ea956e ;;
    darwin-arm64) sum=9709de08e15ef4307ce52dd5c306edf0d115db02119a712c39c00a04e013e88d ;;
    *) fail "no pinned alloy for $os-$arch" ;;
  esac
  name="alloy-$os-$arch"
  tmp="$(mktemp -d)"
  echo "  installing alloy $want ..."
  curl -fsSL -o "$tmp/$name.zip" "https://github.com/grafana/alloy/releases/download/$want/$name.zip" \
    || { rm -rf "$tmp"; fail "could not download alloy $want; check network access to github.com"; }
  sha256_ok "$sum" "$tmp/$name.zip" \
    || { rm -rf "$tmp"; fail "alloy $want zip does not match its pinned checksum"; }
  ${PY:-python3} -c 'import sys, zipfile; zipfile.ZipFile(sys.argv[1]).extract(sys.argv[2], sys.argv[3])' "$tmp/$name.zip" "$name" "$tmp" \
    || { rm -rf "$tmp"; fail "could not unpack alloy $want"; }
  install -m 0755 "$tmp/$name" "$BIN/alloy"
  rm -rf "$tmp"
  ok alloy "${want#v} (installed)"
}
install_alloy

# Git hooks: run the cheap checks before a commit lands, not after CI says so.
#
# --git-common-dir rather than a literal .git, because in a linked worktree .git is a
# *file* pointing at the real git directory, so `[[ -d .git ]]` is false and the hook is
# silently not installed. Hooks live in the common directory and are shared by every
# worktree, which is what we want: the same pre-commit check wherever a commit is made.
HOOKS_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
if [[ -n "$HOOKS_DIR" ]]; then
  HOOKS_DIR="$HOOKS_DIR/hooks"
  mkdir -p "$HOOKS_DIR"
  cat > "$HOOKS_DIR/pre-commit" <<'HOOK'
#!/usr/bin/env bash
set -euo pipefail
# Hooks live in the common git dir, so every worktree gets this the moment one of them
# runs bootstrap — including worktrees on branches whose Makefile predates a target named
# here. Probe rather than assume, or adding a gate breaks commits in every other worktree
# until it rebases. The story checks left the Makefile when stories moved to GitHub
# (SPRINT-05); a branch from before that still runs them, and a branch after runs nothing.
targets=""
for t in validate-stories backlog-check status-check; do
  if grep -q "^$t:" Makefile; then targets="$targets $t"; fi
done
[[ -z "$targets" ]] || make $targets
HOOK
  chmod +x "$HOOKS_DIR/pre-commit"
  ok "git hooks" "pre-commit installed in $(basename "$(dirname "$HOOKS_DIR")")/hooks"
fi

# Commit signing. `main` requires signed commits (GitHub branch protection, 2026-09-22), so
# an unsigned commit is not a style preference — it is a commit that cannot merge, and the
# discovery point would otherwise be a rejected push at the end of a day's work.
#
# SSH signing rather than GPG: the key that already pushes to origin can sign, so there is
# no second key to create, distribute, or lose. Configured per-repo, never globally — this
# script does not get to change how a developer signs in their other repositories.

# Verification needs to know which keys are trusted, or `git log --show-signature` reports
# an error per commit instead of a signature. The file is tracked, so adding a contributor
# is a reviewed change rather than a local edit nobody else sees. Set unconditionally: it
# costs nothing and it is just as useful on a runner inspecting history as on a laptop.
git config --local gpg.ssh.allowedSignersFile .github/allowed_signers

# Everything below is a developer-machine concern. CI verifies the tree; it never commits,
# and a runner has no key in ~/.ssh — demanding one there fails every build for a capability
# the build does not use, which is exactly what it did on the first run of PR #44.
if [ -n "${CI:-}" ]; then
  ok "commit signing" "skipped (CI does not commit)"
else
  SIGNKEY="$(git config --local --get user.signingkey || true)"
  if [ -z "$SIGNKEY" ]; then
    # Prefer a key the developer already uses for origin. ed25519 first: it is what GitHub
    # recommends and what this repo's contributors have.
    for cand in id_ed25519 id_ecdsa id_rsa; do
      if [ -f "$HOME/.ssh/$cand.pub" ]; then SIGNKEY="$HOME/.ssh/$cand.pub"; break; fi
    done
    [ -n "$SIGNKEY" ] || fail "no SSH public key in ~/.ssh to sign with, and commits to this repo must
  be signed. Create one (ssh-keygen -t ed25519), add it to GitHub as a *signing* key
  (Settings > SSH and GPG keys > New SSH key, type 'Signing Key' — an authentication key is
  a separate list and will not verify), add it to .github/allowed_signers, then:
    git config --local gpg.format ssh
    git config --local user.signingkey ~/.ssh/id_ed25519.pub
    git config --local commit.gpgsign true"
    git config --local gpg.format ssh
    git config --local user.signingkey "$SIGNKEY"
  fi
  [ "$(git config --local --get gpg.format || true)" = "ssh" ] || git config --local gpg.format ssh
  [ "$(git config --local --get commit.gpgsign || true)" = "true" ] || git config --local commit.gpgsign true
  [ "$(git config --local --get tag.gpgsign || true)" = "true" ] || git config --local tag.gpgsign true

  # Prove the key actually signs before a commit depends on it. A passphrase-protected key
  # with no agent fails here, where the message can say so, rather than at commit time with
  # the work already staged.
  SIGNKEY="$(git config --local --get user.signingkey)"
  if ! printf 'bootstrap' | ssh-keygen -Y sign -f "$SIGNKEY" -n git - >/dev/null 2>&1; then
    fail "the signing key $SIGNKEY cannot sign unattended.
  If it is passphrase-protected, start an agent and add it:
    eval \"\$(ssh-agent -s)\" && ssh-add ${SIGNKEY%.pub}
  Then re-run bootstrap."
  fi
  ok "commit signing" "ssh, $(basename "$SIGNKEY")"
fi

mkdir -p .local/data
echo "bootstrap: ok"
