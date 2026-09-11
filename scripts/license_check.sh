#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Verify the licensing declaration (LICENSING.md) mechanically:
#   1. `reuse lint` — every tracked file has copyright and license information, from a
#      header or from REUSE.toml, and every license referenced has its text in LICENSES/.
#   2. Every hand-written Go, Python, shell, and protobuf source carries an SPDX header.
#      REUSE.toml already covers these files, so reuse alone would not notice a missing
#      header; the header is what travels with a file copied out of the repo.
# Exit 1 on either failure, naming the files.

set -euo pipefail
cd "$(dirname "$0")/.."
PY="${PY:-python3}"

"$PY" -m reuse --version >/dev/null 2>&1 \
  || { echo "license-check: reuse is not installed; run 'make bootstrap'" >&2; exit 1; }

"$PY" -m reuse lint -q || { "$PY" -m reuse lint; exit 1; }

tag="SPDX-License-Identifier"   # split from its colon so reuse does not read this line as a tag
missing="$(git ls-files -- '*.go' '*.py' '*.sh' '*.proto' ':!gen/' \
  | xargs -r grep -L "$tag:" || true)"
if [ -n "$missing" ]; then
  echo "license-check: source files without an SPDX header (see LICENSING.md):" >&2
  printf '  %s\n' $missing >&2
  exit 1
fi
echo "license-check: REUSE compliant, headers present"
