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
GOLANGCI_VERSION="v2.13.2"
BUF_VERSION="v1.72.0"
GRPCURL_VERSION="v1.9.4"
HELM_VERSION="v3.22.0"
KUBECONFORM_VERSION="v0.8.0"
# The compose Prometheus's version (deploy/compose/docker-compose.yaml), so the rules are
# checked by the parser that loads them. A release tarball, not a Go module — see below.
PROMETHEUS_VERSION="3.1.0"

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

command -v go >/dev/null 2>&1 || fail "go not found; install Go 1.24+ and re-run"
GOV="$(go env GOVERSION | sed 's/^go//')"
ok go "$GOV"

[[ -f go.mod ]] || fail "go.mod is missing; this repo should not be in that state"

# install_pinned <binary> <module> <version>
# Installs only when the binary is absent or is the wrong version, so the common case is
# a version check and nothing else.
install_pinned() {
  local name="$1" module="$2" want="$3"
  local have=""
  if [[ -x "$BIN/$name" ]]; then
    # The module version stamped into the binary, not `--version` output: kubeconform built
    # by `go install` reports "development", helm prints a struct, and grpcurl says "dev
    # build". `go version -m` reads the build info every Go binary carries, so one check
    # covers every pinned tool.
    have="$(go version -m "$BIN/$name" 2>/dev/null | awk '$1=="mod"{print $3; exit}' || true)"
  fi
  if [[ "${have}" == "$want" ]]; then
    ok "$name" "${have#v} (pinned)"
    return 0
  fi
  echo "  installing $name $want ..."
  GOBIN="$BIN" go install "${module}@${want}" \
    || fail "could not install $name $want; check network access to the Go module proxy"
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
  ok docker "not installed (needed by \`make up\`, not by \`make check\`)"
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

# kind and kubectl are needed by `make helm-install ENV=local`, not by `make check`.
command -v kind >/dev/null 2>&1 \
  && ok kind "$(kind version 2>/dev/null | awk '{print $2}')" \
  || ok kind "not installed (needed by \`make helm-install ENV=local\`, not by \`make check\`)"
command -v kubectl >/dev/null 2>&1 \
  && ok kubectl "$(kubectl version --client 2>/dev/null | head -1 | awk '{print $3}')" \
  || ok kubectl "not installed (needed by \`make helm-install\`, not by \`make check\`)"

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
# until it rebases.
targets="validate-stories"
for t in backlog-check status-check; do
  if grep -q "^$t:" Makefile; then targets="$targets $t"; fi
done
make $targets
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
