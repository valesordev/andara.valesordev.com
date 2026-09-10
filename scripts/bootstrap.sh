#!/usr/bin/env bash
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

# install_pinned <binary> <module@version> <version-probe-command...>
# Installs only when the binary is absent or is the wrong version, so the common case is
# a version check and nothing else.
install_pinned() {
  local name="$1" module="$2" want="$3"
  local have=""
  if [[ -x "$BIN/$name" ]]; then
    have="$("$BIN/$name" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
  fi
  if [[ "v${have}" == "$want" ]]; then
    ok "$name" "$have (pinned)"
    return 0
  fi
  echo "  installing $name $want ..."
  GOBIN="$BIN" go install "${module}@${want}" \
    || fail "could not install $name $want; check network access to the Go module proxy"
  ok "$name" "$want (installed)"
}

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

# helm and kubeconform back `make k8s-dry`. It exits 0 with no chart present, so these are
# reported, not required, until AW-INF-003 lands a chart.
command -v helm >/dev/null 2>&1 \
  && ok helm "$(helm version --short 2>/dev/null || echo present)" \
  || ok helm "not installed (needed by \`make k8s-dry\` once AW-INF-003 lands a chart)"
command -v kubeconform >/dev/null 2>&1 \
  && ok kubeconform "present" \
  || ok kubeconform "not installed (manifest schema validation will be skipped)"

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

mkdir -p .local/data
echo "bootstrap: ok"
