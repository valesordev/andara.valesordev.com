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
```
