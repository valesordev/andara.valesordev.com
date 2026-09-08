#!/usr/bin/env bash
# Provision a local CA and a server certificate for the local stack.
#
# ADR-0003 makes TLS mandatory on the wire. If `make up` did not do this, developers would
# learn to pass an insecure flag, and one of them would eventually ship it — so the local
# stack provisions real certificates and `andara-cli` trusts this CA out of the box.
#
# The CA private key is generated per machine, never committed, and never reused across
# machines. .local/ is gitignored, as is *.pem and *.key.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

TLS_DIR="${ANDARA_TLS_DIR:-$REPO/.local/tls}"
DAYS_CA=3650
DAYS_CERT=825   # the longest a modern client will accept for a leaf certificate

fail() { echo "make: tls: $*" >&2; exit 1; }

command -v openssl >/dev/null 2>&1 || fail "openssl not found (run \`make bootstrap\`)"

mkdir -p "$TLS_DIR"
chmod 700 "$TLS_DIR"

# Idempotent: regenerate only what is missing or expired. `make tls FORCE=1` starts over.
if [[ "${FORCE:-0}" != "1" && -f "$TLS_DIR/ca.pem" && -f "$TLS_DIR/server.pem" ]]; then
  if openssl x509 -in "$TLS_DIR/server.pem" -noout -checkend 604800 >/dev/null 2>&1; then
    echo "tls: certificates in $TLS_DIR are valid (\`make tls FORCE=1\` to regenerate)"
    exit 0
  fi
  echo "tls: server certificate expires within 7 days; reissuing"
fi

echo "tls: provisioning local CA and server certificate in $TLS_DIR"

openssl req -x509 -newkey rsa:4096 -sha256 -days "$DAYS_CA" -nodes \
  -keyout "$TLS_DIR/ca-key.pem" -out "$TLS_DIR/ca.pem" \
  -subj "/O=Andara's World/CN=Andara Local CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null

# SANs cover every name the server answers to: the host, the loopback address, and the
# compose service name that in-network clients resolve.
cat > "$TLS_DIR/server.cnf" <<'CNF'
[req]
distinguished_name = dn
req_extensions     = ext
prompt             = no

[dn]
O  = Andara's World
CN = andara-server

[ext]
basicConstraints = critical,CA:FALSE
keyUsage         = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName   = @san

[san]
DNS.1 = localhost
DNS.2 = andara-server
DNS.3 = andara-server.andara.local
IP.1  = 127.0.0.1
IP.2  = ::1
CNF

openssl req -newkey rsa:2048 -sha256 -nodes \
  -keyout "$TLS_DIR/server-key.pem" -out "$TLS_DIR/server.csr" \
  -config "$TLS_DIR/server.cnf" 2>/dev/null

openssl x509 -req -in "$TLS_DIR/server.csr" -sha256 -days "$DAYS_CERT" \
  -CA "$TLS_DIR/ca.pem" -CAkey "$TLS_DIR/ca-key.pem" -CAcreateserial \
  -out "$TLS_DIR/server.pem" \
  -extfile "$TLS_DIR/server.cnf" -extensions ext 2>/dev/null

rm -f "$TLS_DIR/server.csr"
chmod 600 "$TLS_DIR"/*-key.pem
# The server container runs unprivileged and reads these read-only.
chmod 644 "$TLS_DIR/ca.pem" "$TLS_DIR/server.pem"
chmod 644 "$TLS_DIR/server-key.pem"

openssl verify -CAfile "$TLS_DIR/ca.pem" "$TLS_DIR/server.pem" >/dev/null \
  || fail "generated certificate does not verify against the generated CA"

echo "tls: ca=$TLS_DIR/ca.pem server=$TLS_DIR/server.pem (valid $DAYS_CERT days)"
