#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# edge_hint.sh <host> <port>: helm-install's advice after the `andara-cli` line, which dials
# <host>:<port> by DNS. A name with no DNS (CI's andara.local) reaches the edge only through
# 127.0.0.1, and so does an overridden ANDARA_EDGE_PORT: the kind edge's 8443 listens on 127.0.0.1
# only (deploy/kind/cluster.yaml), and the public name resolves to the tailnet, where only nginx
# listens (ADR-0013 amendment 24). Then it names the dial that does connect. Advice only: it
# never fails the install.
set -euo pipefail

HOST="${1:?usage: edge_hint.sh <host> <port>}"
PORT="${2:?usage: edge_hint.sh <host> <port>}"

# getent exits 2 for a name it can't resolve; under pipefail that would end the script.
ADDRS="$(getent ahostsv4 "$HOST" 2>/dev/null | awk '{print $1}' | sort -u || true)"
if [[ -z "$ADDRS" ]]; then
  echo "helm-install:   $HOST does not resolve here; dial it with curl --resolve $HOST:$PORT:127.0.0.1"
elif [[ -n "${ANDARA_EDGE_PORT:-}" ]] && ! grep -qx '127\.0\.0\.1' <<<"$ADDRS"; then
  echo "helm-install:   $HOST does not resolve to 127.0.0.1 here, and $PORT listens only there; dial it with curl --resolve $HOST:$PORT:127.0.0.1"
fi
