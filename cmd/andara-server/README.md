# cmd/andara-server

`main` for the world simulation server. Implementation lane (CLAUDE.md §2 — it runs *in* the game).

Wiring only: flag/env parsing, telemetry setup, dependency construction, signal handling. Everything
it constructs lives under `server/`. The simulation core is `server/sim`, which the depguard rule in
`.golangci.yml` keeps dependency-free.

```
ANDARA_CONTENT_SOURCE=dir ANDARA_CONTENT_PATH=./testdata/content/valid \
  ./bin/andara-server --validate-only ; echo $?   # 0

ANDARA_CONTENT_SOURCE=dir ANDARA_CONTENT_PATH=./testdata/content/dangling \
  ./bin/andara-server --validate-only ; echo $?   # 1

# Serving needs TLS material; `make tls` provisions it. Without it: exit 1, no plaintext mode.
make tls && ANDARA_CONTENT_SOURCE=dir ANDARA_CONTENT_PATH=./testdata/content/valid \
  ANDARA_TLS_CERT_FILE=.local/tls/server.pem ANDARA_TLS_KEY_FILE=.local/tls/server-key.pem \
  ./bin/andara-server                              # :8443 gRPC/TLS, :8080 health and metrics
```

Exit codes: `0` on a clean drain after `SIGTERM`/`SIGINT`; `1` on a configuration error
(including missing or unloadable TLS material), a fatal content finding, or a listener failure.
Configuration keys are in `server/README.md`.
