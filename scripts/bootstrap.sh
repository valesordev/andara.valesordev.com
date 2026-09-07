#!/usr/bin/env bash
# Install and verify the toolchain `make check` needs. Idempotent: running it
# twice leaves the machine in the same state and exits 0 both times.
set -euo pipefail

fail() { echo "make: bootstrap: $*" >&2; exit 1; }
ok()   { printf '  %-22s %s\n' "$1" "$2"; }

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

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

if [[ ! -f go.mod ]]; then
  fail "go.mod is missing; this repo should not be in that state"
fi

# golangci-lint is only required once Go sources exist. Until then, note it and move on
# rather than forcing an install nobody needs yet.
if command -v golangci-lint >/dev/null 2>&1; then
  ok golangci-lint "$(golangci-lint version 2>/dev/null | head -1 | awk '{print $4}')"
elif find . -name '*.go' -not -path './.git/*' -print -quit | grep -q .; then
  fail "golangci-lint not found and Go sources exist; install it (https://golangci-lint.run) and re-run"
else
  ok golangci-lint "not installed (not needed until Go sources exist)"
fi

if command -v docker >/dev/null 2>&1; then
  ok docker "$(docker --version | awk '{print $3}' | tr -d ,)"
else
  ok docker "not installed (needed by \`make up\`, not by \`make check\`)"
fi

# Git hooks: run the cheap checks before a commit lands, not after CI says so.
if [[ -d .git ]]; then
  mkdir -p .git/hooks
  cat > .git/hooks/pre-commit <<'HOOK'
#!/usr/bin/env bash
set -euo pipefail
make validate-stories backlog-check
HOOK
  chmod +x .git/hooks/pre-commit
  ok "git hooks" "pre-commit installed"
fi

mkdir -p .local/data
echo "bootstrap: ok"
