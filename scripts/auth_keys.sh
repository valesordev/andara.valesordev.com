#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

# Provision the session-token signing keyring for the local stack (AW-SRV-008).
#
# auth.token_key_file is required to serve: session tokens are HMAC-signed and
# stateless, so a server with no key has no way to say who anyone is. The local
# stack generates one key per machine, never commits it, and never reuses it.
#
# Format is one `key_id: base64` per line, first key signs, the rest verify. Rotation
# for the local stack is `make auth-keys FORCE=1`; the procedure for a real deployment
# is in server/README.md.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

AUTH_DIR="${ANDARA_AUTH_DIR:-$REPO/.local/auth}"
KEY_FILE="$AUTH_DIR/token-keys"

fail() { echo "make: auth-keys: $*" >&2; exit 1; }

command -v openssl >/dev/null 2>&1 || fail "openssl not found (run \`make bootstrap\`)"

mkdir -p "$AUTH_DIR"
# Traversable like .local/tls: the server container reads the file through it.
chmod 755 "$AUTH_DIR"

if [[ "${FORCE:-0}" != "1" && -s "$KEY_FILE" ]]; then
  echo "auth-keys: keyring in $KEY_FILE exists (\`make auth-keys FORCE=1\` to regenerate)"
  exit 0
fi

# The server refuses a keyring readable by group-write or other. The container runs
# as the host uid (docker-compose.yaml `user:`) so 0600 here is readable there.
umask 077
printf 'local-%s: %s\n' "$(date -u +%Y%m%d)" "$(openssl rand -base64 32)" > "$KEY_FILE"
chmod 600 "$KEY_FILE"
echo "auth-keys: wrote a new signing key to $KEY_FILE"
