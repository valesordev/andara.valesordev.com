# cmd/andara-server

`main` for the world simulation server. Owned by Cursor (CLAUDE.md §2 — it runs *in* the game).

Wiring only: flag/env parsing, telemetry setup, dependency construction, signal handling. Everything
it constructs lives under `server/`. The simulation core is `server/sim`, which the depguard rule in
`.golangci.yml` keeps dependency-free.

First story to put source here: `AW-SRV-005`.
