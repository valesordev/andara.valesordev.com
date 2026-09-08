# cmd/andara-cli

`main` for `andara-cli`. Owned by Cursor (CLAUDE.md §2).

Command tree only; the packages it wires up live under `admin/`. `andara-cli` talks to the server over
the same versioned gRPC service as every other client (ADR-0003) — no privileged back door, no direct
datastore access.

First story to put source here: `AW-CLI-001`.
